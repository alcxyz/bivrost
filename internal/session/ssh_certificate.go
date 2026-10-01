package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/alcxyz/bivrost/internal/authbrowser"
	"github.com/alcxyz/bivrost/internal/azure"
	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
)

// Setup normally completes silently. Once Azure CLI opens a sign-in page, the
// user gets a longer window to finish multi-factor or Conditional Access steps.
var (
	sshCertificateTimeout = 2 * time.Minute
	sshSignInTimeout      = 5 * time.Minute
)

// prepareSSHCertificate asks Azure CLI for a short-lived Entra SSH certificate.
// Azure CLI falls back to browser sign-in for this request when silent token
// renewal fails. Bivrost opens that sign-in in the configured authentication
// browser and explains the wait, or refuses it when interactive_connect is off.
func prepareSSHCertificate(ctx context.Context, c profile.Profile, sshConfig, directory string, port int) error {
	interactive, err := profile.LoadInteractiveConnect()
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Warnings are kept so a sign-in request can be explained. They are matched
	// against known codes and never printed or recorded.
	cmd, err := azure.Command(runCtx, "ssh", "config", "--ip", "127.0.0.1", "--port", strconv.Itoa(port), "--file", sshConfig, "--keys-destination-folder", directory, "--subscription", c.Subscription)
	if err != nil {
		return err
	}
	signal := filepath.Join(directory, "browser-request")
	if interactive {
		err = authbrowser.Interactive(cmd, signal)
	} else {
		err = authbrowser.Refuse(cmd, signal)
	}
	if err != nil {
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
	deadline := time.NewTimer(sshCertificateTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	signingIn := false
	for {
		select {
		case err := <-done:
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if signingIn {
				return fmt.Errorf("%w: Entra SSH sign-in did not complete; connect again and finish sign-in in the browser, or run bivrost login --ssh first", diagnostics.ErrInteractionRequired)
			}
			return sshCertificateError(stderr.Bytes())
		case <-deadline.C:
			cancel()
			<-done
			if signingIn {
				return fmt.Errorf("Entra SSH sign-in was not completed within %v; connect again and finish sign-in in the browser: %w", sshSignInTimeout, context.DeadlineExceeded)
			}
			return fmt.Errorf("Entra SSH setup timed out; check Azure login and connectivity, then retry: %w", context.DeadlineExceeded)
		case <-ticker.C:
			if signingIn || !authbrowser.Requested(signal) {
				continue
			}
			reason := interactionReason(stderr.Bytes())
			if !interactive {
				cancel()
				<-done
				return fmt.Errorf("%w for the Entra SSH certificate: %s. Bivrost did not open a browser because interactive_connect is off; run bivrost login --ssh, then connect again", diagnostics.ErrInteractionRequired, reason)
			}
			signingIn = true
			deadline.Reset(sshSignInTimeout)
			fmt.Printf("Azure needs interactive sign-in for the SSH certificate: %s.\nContinue in the browser window; waiting up to %v...\n", reason, sshSignInTimeout)
		}
	}
}

type knownInteraction struct {
	codes  []string
	reason string
}

// Microsoft Entra error codes that Azure CLI reports before it falls back to
// browser sign-in for an SSH certificate.
var interactionReasons = []knownInteraction{
	{[]string{"AADSTS50076", "AADSTS50079", "AADSTS50074", "AADSTS50078", "AADSTS50158"}, "multi-factor authentication or a Conditional Access step-up is required"},
	{[]string{"AADSTS53000", "AADSTS53001", "AADSTS53002", "AADSTS53003"}, "a Conditional Access device or app requirement was not met"},
	{[]string{"AADSTS65001"}, "consent for Azure Linux VM sign-in is required"},
	{[]string{"AADSTS700082", "AADSTS70043", "AADSTS50173", "AADSTS50133", "AADSTS50132"}, "the Azure CLI sign-in has expired or must be renewed"},
}

var connectivityMarkers = []string{"Failed to establish a new connection", "Max retries exceeded", "ConnectionError", "Name or service not known", "nodename nor servname", "getaddrinfo failed"}

func interactionReason(stderr []byte) string {
	for _, candidate := range interactionReasons {
		if containsAny(stderr, candidate.codes) {
			return candidate.reason
		}
	}
	return "Azure requires fresh interactive sign-in"
}

func sshCertificateError(stderr []byte) error {
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
// It is read while the command still writes to it.
type boundedOutput struct {
	mu    sync.Mutex
	data  bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if remaining := b.limit - b.data.Len(); remaining > 0 {
		b.data.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}

func (b *boundedOutput) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Clone(b.data.Bytes())
}
