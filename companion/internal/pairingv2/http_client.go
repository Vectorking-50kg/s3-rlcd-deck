package pairingv2

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"time"
)

const (
	security2Endpoint = "pairing/session"
	proofEndpoint     = "pairing/proof"
	proofRequest      = "pairing-v2-spike"
	proofResponse     = "proof-verified"
	maximumHTTPBody   = 4096
)

type ProofResult struct {
	Route   Route
	Elapsed time.Duration
}

type ProofClient struct {
	random io.Reader
}

func NewProofClient(random io.Reader) *ProofClient {
	return &ProofClient{random: random}
}

// Prove establishes an authenticated Security2 session over a backend-only
// discovery route and performs one encrypted application round trip. It does
// not create or mutate any Companion Profile or Trust record.
func (client *ProofClient) Prove(ctx context.Context, route Route, code []byte) (ProofResult, error) {
	if route.InterfaceIndex <= 0 || route.InterfaceName == "" ||
		!usablePairingIPv4(route.Address) || route.Port == 0 {
		return ProofResult{}, errors.New("invalid Pairing v2 proof route")
	}
	security, err := NewSecurity2(code, client.random)
	if err != nil {
		return ProofResult{}, err
	}
	defer security.Close()
	httpClient, err := newPairingHTTPClient(route)
	if err != nil {
		return ProofResult{}, err
	}
	defer httpClient.CloseIdleConnections()
	baseURL := "http://" + net.JoinHostPort(route.Address.String(), strconv.Itoa(int(route.Port)))
	started := time.Now()

	command0, err := security.Start()
	if err != nil {
		return ProofResult{}, err
	}
	response0, err := postPairingEndpoint(ctx, httpClient, baseURL, security2Endpoint, command0)
	clearBytes(command0)
	if err != nil {
		return ProofResult{}, err
	}
	command1, err := security.HandleResponse0(response0)
	clearBytes(response0)
	if err != nil {
		return ProofResult{}, err
	}
	response1, err := postPairingEndpoint(ctx, httpClient, baseURL, security2Endpoint, command1)
	clearBytes(command1)
	if err != nil {
		return ProofResult{}, err
	}
	err = security.HandleResponse1(response1)
	clearBytes(response1)
	if err != nil {
		return ProofResult{}, err
	}
	encryptedRequest, err := security.Seal([]byte(proofRequest))
	if err != nil {
		return ProofResult{}, err
	}
	encryptedResponse, err := postPairingEndpoint(ctx, httpClient, baseURL, proofEndpoint, encryptedRequest)
	clearBytes(encryptedRequest)
	if err != nil {
		return ProofResult{}, err
	}
	response, err := security.Open(encryptedResponse)
	clearBytes(encryptedResponse)
	if err != nil {
		return ProofResult{}, err
	}
	defer clearBytes(response)
	if !bytes.Equal(response, []byte(proofResponse)) {
		return ProofResult{}, errors.New("Pairing v2 proof endpoint returned an unexpected response")
	}
	return ProofResult{Route: route, Elapsed: time.Since(started)}, nil
}

func newPairingHTTPClient(route Route) (*http.Client, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("create Pairing v2 cookie jar: %w", err)
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           pairingDialContext(route),
		DisableCompression:    true,
		MaxIdleConns:          1,
		MaxIdleConnsPerHost:   1,
		MaxConnsPerHost:       1,
		IdleConnTimeout:       2 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Jar:       jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("Pairing v2 redirects are forbidden")
		},
	}, nil
}

func postPairingEndpoint(
	ctx context.Context,
	client *http.Client,
	baseURL string,
	endpoint string,
	requestBody []byte,
) ([]byte, error) {
	parsedBase, err := url.Parse(baseURL)
	if err != nil || parsedBase.Scheme != "http" || parsedBase.Host == "" || parsedBase.Path != "" {
		return nil, errors.New("invalid Pairing v2 endpoint base URL")
	}
	endpointURL := *parsedBase
	endpointURL.Path = "/" + endpoint
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		endpointURL.String(),
		bytes.NewReader(requestBody),
	)
	if err != nil {
		return nil, errors.New("create Pairing v2 HTTP request")
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("Cache-Control", "no-store")
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("Pairing v2 endpoint unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maximumHTTPBody+1))
		return nil, fmt.Errorf("Pairing v2 endpoint rejected request with HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maximumHTTPBody+1))
	if err != nil {
		return nil, errors.New("read Pairing v2 endpoint response")
	}
	if len(body) == 0 || len(body) > maximumHTTPBody {
		clearBytes(body)
		return nil, errors.New("invalid Pairing v2 endpoint response length")
	}
	return body, nil
}
