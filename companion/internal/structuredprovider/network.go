package structuredprovider

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type ipResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

type httpClient interface {
	Do(*http.Request) (*http.Response, error)
}

func safeHTTPClient(config normalizedConfig) (*http.Client, *http.Transport) {
	resolver := config.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dial := config.dialer
	if dial == nil {
		networkDialer := &net.Dialer{Timeout: config.requestTimeout}
		dial = networkDialer.DialContext
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            policyDialer(config.target, resolver, dial),
		DisableCompression:     true,
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      false,
		MaxConnsPerHost:        1,
		MaxResponseHeaderBytes: 32 << 10,
		TLSHandshakeTimeout:    config.requestTimeout,
		ResponseHeaderTimeout:  config.requestTimeout,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   config.requestTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 || request.URL.Scheme != config.target.Scheme ||
				!sameURLHost(request.URL, config.target) {
				return ErrNetworkPolicy
			}
			return nil
		},
	}
	return client, transport
}

func policyDialer(
	target *url.URL,
	resolver ipResolver,
	dial dialContextFunc,
) dialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || !strings.EqualFold(strings.TrimSuffix(host, "."), strings.TrimSuffix(target.Hostname(), ".")) {
			return nil, ErrNetworkPolicy
		}
		addresses, err := resolveTarget(ctx, resolver, host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.Join(ErrUnavailable, err)
		}
		for _, candidate := range addresses {
			if !allowedTargetIP(candidate.IP, target.Scheme) {
				return nil, ErrNetworkPolicy
			}
		}
		var lastError error
		for _, candidate := range addresses {
			connection, dialErr := dial(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			lastError = dialErr
		}
		return nil, errors.Join(ErrUnavailable, lastError)
	}
}

func resolveTarget(ctx context.Context, resolver ipResolver, host string) ([]net.IPAddr, error) {
	if literal := net.ParseIP(strings.Trim(host, "[]")); literal != nil {
		return []net.IPAddr{{IP: literal}}, nil
	}
	return resolver.LookupIPAddr(ctx, host)
}

func allowedTargetIP(ip net.IP, scheme string) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() {
		return false
	}
	if scheme == "http" {
		return ip.IsPrivate()
	}
	return scheme == "https"
}

func sameURLHost(left, right *url.URL) bool {
	return strings.EqualFold(strings.TrimSuffix(left.Hostname(), "."), strings.TrimSuffix(right.Hostname(), ".")) &&
		effectivePort(left) == effectivePort(right)
}

func effectivePort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	if value.Scheme == "https" {
		return "443"
	}
	return "80"
}
