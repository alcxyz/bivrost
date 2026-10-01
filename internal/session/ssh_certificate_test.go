package session

import (
	"errors"
	"strings"
	"testing"

	"github.com/alcxyz/bivrost/internal/diagnostics"
)

func TestInteractionReasonNamesKnownEntraRequirements(t *testing.T) {
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
		reason := interactionReason([]byte(stderr))
		if !strings.Contains(reason, want) {
			t.Errorf("%q: reason = %q, want %q", stderr, reason, want)
		}
		if strings.Contains(reason, "AADSTS") || strings.Contains(reason, "administrator") {
			t.Errorf("%q: reason repeats Azure CLI output: %q", stderr, reason)
		}
	}
}

func TestSSHCertificateErrorSeparatesConnectivity(t *testing.T) {
	t.Parallel()
	connectivity := sshCertificateError([]byte("requests.exceptions.ConnectionError: Max retries exceeded with url: /organizations"))
	if !strings.Contains(connectivity.Error(), "could not reach Microsoft Entra ID") || strings.Contains(connectivity.Error(), "/organizations") {
		t.Fatalf("connectivity error = %v", connectivity)
	}
	other := sshCertificateError([]byte("AADSTS50076"))
	if errors.Is(other, diagnostics.ErrInteractionRequired) || !strings.Contains(other.Error(), "Entra SSH setup failed") {
		t.Fatalf("generic error = %v", other)
	}
}
