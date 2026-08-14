package structuredprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type fixedResolver struct {
	addresses []net.IPAddr
	err       error
}

func (resolver fixedResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return append([]net.IPAddr(nil), resolver.addresses...), resolver.err
}

func TestNetworkPolicyRejectsSSRFAndDNSRebinding(t *testing.T) {
	tests := []struct {
		name    string
		ip      string
		scheme  string
		allowed bool
	}{
		{name: "public HTTPS", ip: "198.51.100.10", scheme: "https", allowed: true},
		{name: "private HTTPS", ip: "10.0.0.10", scheme: "https", allowed: true},
		{name: "private HTTP", ip: "192.168.4.1", scheme: "http", allowed: true},
		{name: "public HTTP", ip: "198.51.100.10", scheme: "http", allowed: false},
		{name: "loopback", ip: "127.0.0.1", scheme: "https", allowed: false},
		{name: "metadata", ip: "169.254.169.254", scheme: "https", allowed: false},
		{name: "unspecified", ip: "0.0.0.0", scheme: "https", allowed: false},
		{name: "IPv6 loopback", ip: "::1", scheme: "https", allowed: false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := allowedTargetIP(net.ParseIP(testCase.ip), testCase.scheme); got != testCase.allowed {
				t.Fatalf("allowedTargetIP(%s, %s) = %v", testCase.ip, testCase.scheme, got)
			}
		})
	}

	target, _ := url.Parse("https://usage.example.test/v1")
	resolver := fixedResolver{addresses: []net.IPAddr{
		{IP: net.ParseIP("198.51.100.10")},
		{IP: net.ParseIP("127.0.0.1")},
	}}
	dialed := false
	dial := policyDialer(target, resolver, func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("not reached")
	})
	if _, err := dial(context.Background(), "tcp", "usage.example.test:443"); !errors.Is(err, ErrNetworkPolicy) {
		t.Fatalf("policyDialer() error = %v", err)
	}
	if dialed {
		t.Fatal("DNS rebinding set reached dialer")
	}
}

func TestNetworkPolicyPinsResolvedAddressAndRedirectHost(t *testing.T) {
	target, _ := url.Parse("http://usage.lan:8080/v1")
	resolver := fixedResolver{addresses: []net.IPAddr{{IP: net.ParseIP("10.2.3.4")}}}
	dialAddress := ""
	dial := policyDialer(target, resolver, func(_ context.Context, _, address string) (net.Conn, error) {
		dialAddress = address
		return nil, errors.New("expected test stop")
	})
	_, _ = dial(context.Background(), "tcp", "usage.lan:8080")
	if dialAddress != "10.2.3.4:8080" {
		t.Fatalf("dial address = %q", dialAddress)
	}

	definition := validDefinition()
	normalized, err := normalizeConfig(Config{Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	client, _ := safeHTTPClient(normalized)
	original, _ := http.NewRequest(http.MethodGet, definition.Request.URL, nil)
	sameHost, _ := http.NewRequest(http.MethodGet, "https://usage.example.test/other", nil)
	differentHost, _ := http.NewRequest(http.MethodGet, "https://metadata.example/", nil)
	downgrade, _ := http.NewRequest(http.MethodGet, "http://usage.example.test/other", nil)
	if err = client.CheckRedirect(sameHost, []*http.Request{original}); err != nil {
		t.Fatalf("same-host redirect rejected: %v", err)
	}
	for _, request := range []*http.Request{differentHost, downgrade} {
		if err = client.CheckRedirect(request, []*http.Request{original}); !errors.Is(err, ErrNetworkPolicy) {
			t.Fatalf("unsafe redirect error = %v", err)
		}
	}
}

func TestCurlImportParsesWhitelistWithoutExecutingOrPersistingSecrets(t *testing.T) {
	secret := "PRIVATE_CURL_TOKEN_CANARY"
	imported, err := ImportCurl("curl --request POST --url 'https://usage.example/v1' " +
		"--header 'Authorization: Bearer " + secret + "' " +
		"--header 'Content-Type: application/json' --data-raw '{\"query\":\"quota\"}'")
	if err != nil {
		t.Fatal(err)
	}
	if imported.Request.Method != MethodPOST || imported.Request.URL != "https://usage.example/v1" ||
		string(imported.Request.Body) != `{"query":"quota"}` || len(imported.Secrets) != 1 ||
		string(imported.Secrets[0].Value) != secret ||
		imported.Request.Headers[0].Prefix != "Bearer " {
		t.Fatalf("ImportCurl() = %+v", imported)
	}
	encoded, marshalErr := json.Marshal(imported.Request)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatal("persistable request retained imported secret")
	}

	customSecret := "PRIVATE_X_AUTH_CANARY"
	custom, err := ImportCurl("curl https://usage.example/v1 -H 'X-Auth: " + customSecret + "'")
	if err != nil || len(custom.Secrets) != 1 || string(custom.Secrets[0].Value) != customSecret {
		t.Fatalf("custom header import = %+v, %v", custom, err)
	}
	encoded, marshalErr = json.Marshal(custom.Request)
	if marshalErr != nil || strings.Contains(string(encoded), customSecret) {
		t.Fatalf("persisted custom header = %s, %v", encoded, marshalErr)
	}

	unsafe := []string{
		"curl https://usage.example/; touch /tmp/pwned",
		"curl https://usage.example/ | sh",
		"curl https://usage.example/ -d @/etc/passwd",
		"curl https://usage.example/ -H 'X-Key: $TOKEN'",
		"curl https://usage.example/ -H 'X-Key: $(whoami)'",
		"curl --config ~/.curlrc https://usage.example/",
		"curl -L https://usage.example/",
		"curl https://usage.example/ > response.json",
		"curl https://usage.example/ -d '{\"api_key\":\"secret\"}'",
		"curl https://usage.example/ -d '{\"auth_token\":\"secret\"}'",
		"curl https://usage.example/ -d '{\"private_key\":\"secret\"}'",
		"curl https://usage.example/ -d '{\"user_prompt\":\"secret\"}'",
		"curl https://usage.example/ -d '{\"authentication\":{\"type\":\"bearer\",\"value\":\"secret\"}}'",
		"curl https://usage.example/ -d '{\"passwd\":\"secret\"}'",
		"curl https://usage.example/ -d '{\"passphrase\":\"secret\"}'",
		"curl 'https://usage.example/?token=PRIVATE_QUERY_CANARY'",
		"curl 'https://usage.example/?auth_token=PRIVATE_QUERY_CANARY'",
		"curl 'https://usage.example/?client_secret=PRIVATE_QUERY_CANARY'",
		"curl 'https://usage.example/?subscription_key=PRIVATE_QUERY_CANARY'",
		"curl 'https://usage.example/?x-api-key=PRIVATE_QUERY_CANARY'",
		"curl file:///etc/passwd",
		"curl https://user:pass@usage.example/",
	}
	for _, command := range unsafe {
		if _, importErr := ImportCurl(command); !errors.Is(importErr, ErrInvalidCurl) {
			t.Fatalf("ImportCurl(%q) error = %v", command, importErr)
		} else if strings.Contains(importErr.Error(), secret) {
			t.Fatal("curl error exposed secret")
		}
	}
}

func TestConfigurationRejectsPersistedSecretsAndPrivateDisplayData(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Definition)
	}{
		{name: "missing secret reference", mutate: func(definition *Definition) {
			definition.Request.Headers[0] = Header{Name: "Authorization"}
		}},
		{name: "secret URL query", mutate: func(definition *Definition) {
			definition.Request.URL = "https://usage.example.test/v1?client_secret=PRIVATE_QUERY_CANARY"
		}},
		{name: "secret JSON body", mutate: func(definition *Definition) {
			definition.Request.Method = MethodPOST
			definition.Request.Body = []byte(`{"private_key":"secret"}`)
		}},
		{name: "absolute path display", mutate: func(definition *Definition) {
			definition.DisplayName = "quota (/Users/alice/private)"
		}},
		{name: "loopback HTTPS", mutate: func(definition *Definition) {
			definition.Request.URL = "https://127.0.0.1/private"
		}},
		{name: "public cleartext", mutate: func(definition *Definition) {
			definition.Request.URL = "http://198.51.100.2/private"
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			definition := validDefinition()
			testCase.mutate(&definition)
			if _, err := New(Config{Definition: definition}); err == nil {
				t.Fatal("New() accepted unsafe configuration")
			}
		})
	}
}

func TestJSONPathSubsetRejectsExecutableOrUnboundedExpressions(t *testing.T) {
	valid := []string{"$.data.balance", "$['balance_infos'][0].total_balance", `$.a-b[12]`}
	for _, path := range valid {
		if _, err := compilePath(path); err != nil {
			t.Fatalf("compilePath(%q) error = %v", path, err)
		}
	}
	invalid := []string{"$", "$..secret", "$[*]", "$[?(@.x)]", "$.function()", "$[2048]"}
	for _, path := range invalid {
		if _, err := compilePath(path); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("compilePath(%q) error = %v", path, err)
		}
	}
}
