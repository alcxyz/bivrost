package azure

import (
	"strings"
	"testing"
)

func TestTunnelErrorsClassifiesKnownFailures(t *testing.T) {
	cases := []struct {
		stderr, want string
	}{
		{"ERROR: (AuthorizationFailed) The client 'SECRET_MARKER@example.com' does not have authorization to perform action 'Microsoft.Network/bastionHosts/read'", "AuthorizationFailed"},
		{"ERROR: (SubscriptionNotFound) The subscription 'SECRET_MARKER' could not be found.", "subscription is not available"},
		{"ERROR: (ResourceNotFound) The Resource 'SECRET_MARKER' was not found.", "could not find"},
		{"ERROR: Bastion Host SKU must be Standard or Premium and Native Client must be enabled.", "native client"},
		{"ERROR: AADSTS700082: The refresh token has expired. SECRET_MARKER", "sign-in has expired"},
		{"ERROR: Please run 'az login' to setup account.", "sign-in has expired"},
		{"ConnectionError: Max retries exceeded with url: SECRET_MARKER", "could not reach Azure"},
		{"ERROR: Defined port is currently unavailable", "local port"},
		{"Traceback: something unexpected SECRET_MARKER", "exited before it was ready"},
		{"", "exited before it was ready"},
	}
	for _, c := range cases {
		errors := NewTunnelErrors()
		if _, err := errors.Write([]byte(c.stderr)); err != nil {
			t.Fatal(err)
		}
		err := errors.Err()
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Err() for %q = %v; want it to mention %q", c.stderr, err, c.want)
		}
		if err != nil && strings.Contains(err.Error(), "SECRET_MARKER") {
			t.Errorf("Err() exposed Azure CLI output: %v", err)
		}
	}
}

func TestTunnelErrorsKeepsBoundedSample(t *testing.T) {
	errors := NewTunnelErrors()
	if n, err := errors.Write([]byte(strings.Repeat("x", tunnelErrorSampleBytes+10))); err != nil || n != tunnelErrorSampleBytes+10 {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	// Output past the bound is discarded rather than failing the tunnel.
	if _, err := errors.Write([]byte("AuthorizationFailed")); err != nil {
		t.Fatal(err)
	}
	if got := errors.sample.data.Len(); got != tunnelErrorSampleBytes {
		t.Fatalf("sample length = %d; want %d", got, tunnelErrorSampleBytes)
	}
	if strings.Contains(errors.Err().Error(), "AuthorizationFailed") {
		t.Fatal("Err() classified output beyond the sample bound")
	}
}
