package pairingv2

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostPairingEndpointRetainsCookieAndRejectsRedirects(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			http.SetCookie(response, &http.Cookie{Name: "session", Value: "123"})
			_, _ = response.Write([]byte("first"))
			return
		}
		if cookie, err := request.Cookie("session"); err != nil || cookie.Value != "123" {
			t.Error("session cookie was not retained")
		}
		http.Redirect(response, request, "/forbidden", http.StatusFound)
	}))
	defer server.Close()
	client, err := newPairingHTTPClient(Route{InterfaceIndex: 1, InterfaceName: "test"})
	if err != nil {
		t.Fatal(err)
	}
	client.Transport = server.Client().Transport
	if _, err := postPairingEndpoint(context.Background(), client, server.URL, "one", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if _, err := postPairingEndpoint(context.Background(), client, server.URL, "two", []byte("x")); err == nil {
		t.Fatal("redirect was accepted")
	}
}

func TestPostPairingEndpointBoundsAndRedactsFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
		_, _ = response.Write([]byte(strings.Repeat("secret", maximumHTTPBody)))
	}))
	defer server.Close()
	client := server.Client()
	_, err := postPairingEndpoint(context.Background(), client, server.URL, "failure", []byte("request-secret"))
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Fatalf("unsafe endpoint error: %v", err)
	}
}
