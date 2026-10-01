package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alcxyz/bivrost/internal/diagnostics"
)

func TestSSHCertificateErrorClassifiesInteractiveSignIn(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"AADSTS50076: Due to a configuration change made by your administrator": "multi-factor authentication",
		"AADSTS53003: Access has been blocked by Conditional Access policies":   "Conditional Access device or app requirement",
		"AADSTS65001: The user or administrator has not consented":              "consent for Azure Linux VM sign-in",
		"AADSTS700082: The refresh token has expired due to inactivity":         "sign-in has expired",
		"AADSTS70043: The refresh token has expired or is invalid":              "sign-in has expired",
		"unrecognised warning": "fresh interactive sign-in",
	}
	for stderr, want := range cases {
		err := sshCertificateError([]byte(stderr), true, nil)
		if !errors.Is(err, diagnostics.ErrInteractionRequired) {
			t.Errorf("%q: error %v is not ErrInteractionRequired", stderr, err)
		}
		message := err.Error()
		if !strings.Contains(message, want) || !strings.Contains(message, "did not open a browser") || !strings.Contains(message, "bivrost login --ssh") {
			t.Errorf("%q: message = %q, want reason %q and login guidance", stderr, message, want)
		}
		if strings.Contains(message, "AADSTS") || strings.Contains(message, "administrator") {
			t.Errorf("%q: message repeats Azure CLI output: %q", stderr, message)
		}
	}
}

func TestSSHCertificateErrorSeparatesNonInteractiveFailures(t *testing.T) {
	t.Parallel()
	timeout := sshCertificateError(nil, false, context.DeadlineExceeded)
	if !errors.Is(timeout, context.DeadlineExceeded) || errors.Is(timeout, diagnostics.ErrInteractionRequired) {
		t.Fatalf("timeout error = %v", timeout)
	}
	connectivity := sshCertificateError([]byte("requests.exceptions.ConnectionError: Max retries exceeded with url: /organizations"), false, nil)
	if !strings.Contains(connectivity.Error(), "could not reach Microsoft Entra ID") || strings.Contains(connectivity.Error(), "/organizations") {
		t.Fatalf("connectivity error = %v", connectivity)
	}
	// A code alone does not mean Azure CLI asked for a browser.
	other := sshCertificateError([]byte("AADSTS50076"), false, nil)
	if errors.Is(other, diagnostics.ErrInteractionRequired) || !strings.Contains(other.Error(), "Entra SSH setup failed") {
		t.Fatalf("generic error = %v", other)
	}
}

func TestBoundedOutputKeepsPrefix(t *testing.T) {
	t.Parallel()
	output := boundedOutput{limit: 4}
	for _, part := range []string{"ab", "cdef", "gh"} {
		if n, err := fmt.Fprint(&output, part); err != nil || n != len(part) {
			t.Fatalf("Write(%q) = %d, %v", part, n, err)
		}
	}
	if got := string(output.Bytes()); got != "abcd" {
		t.Fatalf("Bytes() = %q", got)
	}
}
