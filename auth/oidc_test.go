package auth

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type oidcRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn oidcRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func oidcResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}
}

func TestRefreshOIDCTokenDoesNotReplayGrantAfterAmbiguousServerError(t *testing.T) {
	attempts := 0
	grantConsumed := false
	client := &http.Client{Transport: oidcRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		if req.URL.String() != "https://oidc.us-east-1.amazonaws.com/token" {
			t.Fatalf("unexpected token endpoint %q", req.URL.String())
		}
		// Model a server that consumes the grant but loses/fails its response.
		grantConsumed = true
		return oidcResponse(req, http.StatusInternalServerError, `{"error":"server_error"}`), nil
	})}

	_, _, _, _, err := refreshOIDCToken("refresh-old", "client-id", "client-secret", "us-east-1", client)
	if err == nil || !strings.Contains(err.Error(), "refresh failed: HTTP 500") {
		t.Fatalf("expected server error, got %v", err)
	}
	if !grantConsumed {
		t.Fatal("test server did not consume the grant")
	}
	if attempts != 1 {
		t.Fatalf("got %d attempts for an ambiguous grant, want 1", attempts)
	}
}

func TestRefreshOIDCTokenDoesNotRetryInvalidGrant(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: oidcRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		return oidcResponse(req, http.StatusBadRequest, `{"error":"invalid_grant","credential":"do-not-leak"}`), nil
	})}

	_, _, _, _, err := refreshOIDCToken("refresh", "client-id", "client-secret", "us-east-1", client)
	if err == nil || !strings.Contains(err.Error(), "refresh failed: HTTP 400 invalid_grant") {
		t.Fatalf("expected allowlisted invalid-grant error, got %v", err)
	}
	if strings.Contains(err.Error(), "do-not-leak") {
		t.Fatal("OIDC error leaked an untrusted response body field")
	}
	if attempts != 1 {
		t.Fatalf("got %d attempts for non-retryable response, want 1", attempts)
	}
}
