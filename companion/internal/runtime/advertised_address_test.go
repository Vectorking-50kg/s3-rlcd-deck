package runtime

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestResolveDeviceHubAdvertisedAddressFailsClosed(t *testing.T) {
	tests := []struct {
		name       string
		bound      string
		configured string
		routeIP    net.IP
		want       string
	}{
		{name: "explicit", bound: "0.0.0.0:7780", configured: "192.168.1.20:7780", want: "192.168.1.20:7780"},
		{name: "concrete listener", bound: "10.0.0.8:7780", want: "10.0.0.8:7780"},
		{name: "wildcard uses route", bound: "0.0.0.0:7780", routeIP: net.ParseIP("192.168.50.4"), want: "192.168.50.4:7780"},
		{name: "wildcard without route", bound: "0.0.0.0:7780", want: ""},
		{name: "loopback is not advertised", bound: "127.0.0.1:7780", want: ""},
		{name: "invalid explicit fails closed", bound: "10.0.0.8:7780", configured: "0.0.0.0:7780", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveDeviceHubAdvertisedAddress(test.bound, test.configured, test.routeIP); got != test.want {
				t.Fatalf("resolveDeviceHubAdvertisedAddress() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeConfigRejectsUnsafeAdvertisedDeviceHubAddress(t *testing.T) {
	config := Config{
		Version: "test",
		Management: ManagementConfig{
			AdminToken: "management-token-with-enough-bytes",
		},
		DeviceHub: DeviceHubConfig{
			AdvertisedAddress: "0.0.0.0:7780",
		},
		Pairing: testPairingService(t),
	}
	if _, err := normalizeConfig(config); err == nil {
		t.Fatal("normalizeConfig accepted an unspecified advertised address")
	}
}

func TestAdvertisedIPv4RejectsReservedAndDocumentationRanges(t *testing.T) {
	tests := map[string]bool{
		"10.0.0.8": true, "100.64.1.8": true, "192.168.1.8": true, "8.8.8.8": true,
		"0.1.2.3": false, "127.0.0.1": false, "169.254.1.1": false, "192.0.2.1": false,
		"198.18.0.1": false, "198.51.100.1": false, "203.0.113.1": false,
		"224.0.0.1": false, "240.0.0.1": false, "255.255.255.255": false,
	}
	for value, want := range tests {
		if got := usableAdvertisedIPv4(net.ParseIP(value)); got != want {
			t.Errorf("usableAdvertisedIPv4(%s) = %v, want %v", value, got, want)
		}
	}
}

func TestParseDefaultRouteInterfaceFailsClosedOnAmbiguity(t *testing.T) {
	if got, ok := parseDefaultRouteInterface("gateway: 192.168.1.1\ninterface: en0\n"); !ok || got != "en0" {
		t.Fatalf("parseDefaultRouteInterface() = %q, %v", got, ok)
	}
	if got, ok := parseDefaultRouteInterface("interface: en0\ninterface: en7\n"); ok || got != "" {
		t.Fatalf("ambiguous parseDefaultRouteInterface() = %q, %v", got, ok)
	}
}

func TestPairingCodeRecomputesAdvertisedAddressBeforeIssuing(t *testing.T) {
	application, err := New(Config{
		Version: "test",
		Management: ManagementConfig{
			Address: "127.0.0.1:0", AdminToken: "management-token-with-enough-bytes",
		},
		DeviceHub: DeviceHubConfig{Address: "127.0.0.1:0"},
		Pairing:   testPairingService(t),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	current := "192.168.1.20:7780"
	application.advertisedAddress = func(context.Context, string, string) string { return current }

	issue := func() (int, string) {
		recorder := httptest.NewRecorder()
		application.handleIssuePairingCode(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/pairing/codes", nil))
		if recorder.Code != http.StatusOK {
			return recorder.Code, ""
		}
		var document struct {
			DeviceHubAddress string `json:"device_hub_address"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
			t.Fatalf("decode pairing response: %v", err)
		}
		return recorder.Code, document.DeviceHubAddress
	}

	if status, address := issue(); status != http.StatusOK || address != current {
		t.Fatalf("first issue = %d %q", status, address)
	}
	current = "192.168.2.30:7780"
	if status, address := issue(); status != http.StatusOK || address != current {
		t.Fatalf("route-changed issue = %d %q", status, address)
	}
	current = ""
	if status, _ := issue(); status != http.StatusServiceUnavailable {
		t.Fatalf("unavailable route issue = %d, want 503", status)
	}
}
