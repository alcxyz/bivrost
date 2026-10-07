package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
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
	// Report invalid browser rules now rather than during sign-in.
	if _, err := profile.HasAuthenticationBrowsers(); err != nil {
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
	// Azure CLI warnings go to a private file rather than a pipe: a browser
	// started by webbrowser inherits stderr and would hold a pipe open after
	// Azure CLI exits. The file is removed before setup returns.
	stderrPath := filepath.Join(directory, "azure-cli-warnings")
	stderr, err := os.OpenFile(stderrPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("could not prepare Entra SSH setup")
	}
	defer os.Remove(stderrPath)
	defer stderr.Close()
	warnings := func() []byte { return readPrefix(stderrPath, 64*1024) }
	cmd.Stderr = stderr
	// Azure CLI launchers may run Python as a child; stop the whole group.
	prepareProcess(cmd)
	cmd.WaitDelay = 5 * time.Second
	if err := cmd.Start(); err != nil {
		return errors.New("could not start Azure CLI for Entra SSH setup")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	stop := func() {
		cancel()
		<-done
	}
	deadline := time.NewTimer(sshCertificateTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	signingIn := false
	refused := func() error {
		return fmt.Errorf("%w for the Entra SSH certificate: %s. Bivrost did not open a browser because interactive_connect is off; run bivrost login --ssh, then connect again", diagnostics.ErrInteractionRequired, interactionReason(warnings()))
	}
	for {
		select {
		case err := <-done:
			if err == nil {
				return nil
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// The request may arrive between polls.
			if signingIn || authbrowser.Requested(signal) {
				if !interactive {
					return refused()
				}
				return fmt.Errorf("%w: Entra SSH sign-in did not complete; connect again and finish sign-in in the browser, or run bivrost login --ssh first", diagnostics.ErrInteractionRequired)
			}
			return sshCertificateError(warnings())
		case <-deadline.C:
			stop()
			if signingIn {
				return fmt.Errorf("Entra SSH sign-in was not completed within %v; connect again and finish sign-in in the browser: %w", sshSignInTimeout, context.DeadlineExceeded)
			}
			return fmt.Errorf("Entra SSH setup timed out; check Azure login and connectivity, then retry: %w", context.DeadlineExceeded)
		case <-ticker.C:
			if signingIn {
				// MSAL waits for a redirect even when no browser could open.
				if bytes.Contains(warnings(), []byte(noBrowserWarning)) {
					stop()
					return fmt.Errorf("%w: no browser could be opened for Entra SSH sign-in; set authentication_browser, or run bivrost login --ssh where a browser is available, then connect again", diagnostics.ErrInteractionRequired)
				}
				continue
			}
			if !authbrowser.Requested(signal) {
				continue
			}
			if !interactive {
				stop()
				return refused()
			}
			signingIn = true
			deadline.Reset(sshSignInTimeout)
			fmt.Fprintf(os.Stderr, "Azure needs interactive sign-in for the SSH certificate: %s.\nContinue in the browser window; waiting up to %v...\n", interactionReason(warnings()), sshSignInTimeout)
		}
	}
}

// MSAL logs this when every browser failed to open the sign-in page.
const noBrowserWarning = "Found no browser in current environment"

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

// readPrefix returns at most limit bytes from the start of path.
func readPrefix(path string, limit int64) []byte {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	data, _ := io.ReadAll(io.LimitReader(file, limit))
	return data
}
