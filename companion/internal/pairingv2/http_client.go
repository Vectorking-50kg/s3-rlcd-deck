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
	"sync"
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

// SecureSession owns one interface-pinned HTTP connection, cookie jar, and
// ordered Security2 nonce stream. Exchanges are serialized because nonce and
// cookie order are part of the authenticated transcript.
type SecureSession struct {
	mutex    sync.Mutex
	security *Security2
	client   *http.Client
	baseURL  string
	closed   bool
}

func NewProofClient(random io.Reader) *ProofClient {
	return &ProofClient{random: random}
}

// Prove establishes an authenticated Security2 session over a backend-only
// discovery route and performs one encrypted application round trip. It does
// not create or mutate any Companion Profile or Trust record.
func (client *ProofClient) Prove(ctx context.Context, route Route, code []byte) (ProofResult, error) {
	started := time.Now()
	session, err := client.Connect(ctx, route, code)
	if err != nil {
		return ProofResult{}, err
	}
	defer session.Close()
	response, err := session.Exchange(ctx, proofEndpoint, []byte(proofRequest))
	if err != nil {
		return ProofResult{}, err
	}
	defer clearBytes(response)
	if !bytes.Equal(response, []byte(proofResponse)) {
		return ProofResult{}, errors.New("Pairing v2 proof endpoint returned an unexpected response")
	}
	return ProofResult{Route: route, Elapsed: time.Since(started)}, nil
}

func (client *ProofClient) Connect(
	ctx context.Context,
	route Route,
	code []byte,
) (*SecureSession, error) {
	if route.InterfaceIndex <= 0 || route.InterfaceName == "" ||
		!usablePairingIPv4(route.Address) || route.Port == 0 {
		return nil, errors.New("invalid Pairing v2 proof route")
	}
	security, err := NewSecurity2(code, client.random)
	if err != nil {
		return nil, err
	}
	httpClient, err := newPairingHTTPClient(route)
	if err != nil {
		security.Close()
		return nil, err
	}
	baseURL := "http://" + net.JoinHostPort(route.Address.String(), strconv.Itoa(int(route.Port)))

	command0, err := security.Start()
	if err != nil {
		security.Close()
		httpClient.CloseIdleConnections()
		return nil, err
	}
	response0, err := postPairingEndpoint(ctx, httpClient, baseURL, security2Endpoint, command0)
	clearBytes(command0)
	if err != nil {
		security.Close()
		httpClient.CloseIdleConnections()
		return nil, err
	}
	command1, err := security.HandleResponse0(response0)
	clearBytes(response0)
	if err != nil {
		security.Close()
		httpClient.CloseIdleConnections()
		return nil, err
	}
	response1, err := postPairingEndpoint(ctx, httpClient, baseURL, security2Endpoint, command1)
	clearBytes(command1)
	if err != nil {
		security.Close()
		httpClient.CloseIdleConnections()
		return nil, err
	}
	err = security.HandleResponse1(response1)
	clearBytes(response1)
	if err != nil {
		security.Close()
		httpClient.CloseIdleConnections()
		return nil, err
	}
	return &SecureSession{security: security, client: httpClient, baseURL: baseURL}, nil
}

func (session *SecureSession) Exchange(
	ctx context.Context,
	endpoint string,
	plaintext []byte,
) ([]byte, error) {
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if session.closed || session.security == nil || session.client == nil {
		return nil, errors.New("Pairing v2 secure session is closed")
	}
	encryptedRequest, err := session.security.Seal(plaintext)
	if err != nil {
		return nil, err
	}
	encryptedResponse, err := postPairingEndpoint(
		ctx,
		session.client,
		session.baseURL,
		endpoint,
		encryptedRequest,
	)
	clearBytes(encryptedRequest)
	if err != nil {
		return nil, err
	}
	response, err := session.security.Open(encryptedResponse)
	clearBytes(encryptedResponse)
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (session *SecureSession) Close() {
	if session == nil {
		return
	}
	session.mutex.Lock()
	defer session.mutex.Unlock()
	if session.closed {
		return
	}
	session.closed = true
	if session.security != nil {
		session.security.Close()
	}
	if session.client != nil {
		session.client.CloseIdleConnections()
	}
	session.security = nil
	session.client = nil
	session.baseURL = ""
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
