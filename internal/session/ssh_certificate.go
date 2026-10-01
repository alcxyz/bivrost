package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/alcxyz/bivrost/internal/authbrowser"
	"github.com/alcxyz/bivrost/internal/azure"
	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
)

const sshCertificateTimeout = 2 * time.Minute

// prepareSSHCertificate asks Azure CLI for a short-lived Entra SSH certificate.
// Azure CLI falls back to browser sign-in for this request when silent token
// renewal fails. Bivrost refuses that browser request and stops immediately, so
// connect never starts interactive authentication on its own.
func prepareSSHCertificate(ctx context.Context, c profile.Profile, sshConfig, directory string, port int) error {
	authCtx, cancel := context.WithTimeout(ctx, sshCertificateTimeout)
	defer cancel()
	// Warnings are kept so a refused sign-in can be classified. They are matched
	// against known codes and never printed or recorded.
	cmd, err := azure.Command(authCtx, "ssh", "config", "--ip", "127.0.0.1", "--port", strconv.Itoa(port), "--file", sshConfig, "--keys-destination-folder", directory, "--subscription", c.Subscription)
	if err != nil {
		return err
	}
	signal := filepath.Join(directory, "browser-request")
	if err := authbrowser.Refuse(cmd, signal); err != nil {
		return err
	}
	stderr := &boundedOutput{limit: 64 * 1024}
	cmd.Stderr = stderr
	// Azure CLI launchers may run Python as a child; stop the whole group.
	prepareProcess(cmd)
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return errors.New("could not start Azure CLI for Entra SSH setup")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return sshCertificateError(stderr.Bytes(), authbrowser.Requested(signal), authCtx.Err())
		case <-ticker.C:
			if authbrowser.Requested(signal) {
				cancel()
				<-done
				return sshCertificateError(stderr.Bytes(), true, nil)
			}
		}
	}
}

type interactionReason struct {
	codes  []string
	reason string
}

// Microsoft Entra error codes that Azure CLI reports before it falls back to
// browser sign-in for an SSH certificate.
var interactionReasons = []interactionReason{
	{[]string{"AADSTS50076", "AADSTS50079", "AADSTS50074", "AADSTS50078", "AADSTS50158"}, "multi-factor authentication or a Conditional Access step-up is required"},
	{[]string{"AADSTS53000", "AADSTS53001", "AADSTS53002", "AADSTS53003"}, "a Conditional Access device or app requirement was not met"},
	{[]string{"AADSTS65001"}, "consent for Azure Linux VM sign-in is required"},
	{[]string{"AADSTS700082", "AADSTS70043", "AADSTS50173", "AADSTS50133", "AADSTS50132"}, "the Azure CLI sign-in has expired or must be renewed"},
}

var connectivityMarkers = []string{"Failed to establish a new connection", "Max retries exceeded", "ConnectionError", "Name or service not known", "nodename nor servname", "getaddrinfo failed"}

func sshCertificateError(stderr []byte, interactive bool, contextErr error) error {
	if interactive {
		reason := "Azure requires fresh interactive sign-in"
		for _, candidate := range interactionReasons {
			if containsAny(stderr, candidate.codes) {
				reason = candidate.reason
				break
			}
		}
		return fmt.Errorf("%w for the Entra SSH certificate: %s. Bivrost did not open a browser; run bivrost login --ssh, then connect again", diagnostics.ErrInteractionRequired, reason)
	}
	if errors.Is(contextErr, context.DeadlineExceeded) {
		return fmt.Errorf("Entra SSH setup timed out; check Azure login and connectivity, then retry: %w", contextErr)
	}
	if containsAny(stderr, connectivityMarkers) {
		return errors.New("Entra SSH setup could not reach Microsoft Entra ID or Azure; check network connectivity and proxy settings, then retry")
	}
	return errors.New("Entra SSH setup failed; run bivrost login and check the Azure CLI ssh extension and VM login access")
}

func containsAny(data []byte, markers []string) bool {
	for _, marker := range markers {
		if bytes.Contains(data, []byte(marker)) {
			return true
		}
	}
	return false
}

// boundedOutput keeps the start of subprocess output for local classification.
type boundedOutput struct {
	data  bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if remaining := b.limit - b.data.Len(); remaining > 0 {
		b.data.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}

func (b *boundedOutput) Bytes() []byte { return b.data.Bytes() }
