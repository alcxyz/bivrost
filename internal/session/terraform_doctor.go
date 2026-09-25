package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/alcxyz/bivrost/internal/azure"
	"github.com/alcxyz/bivrost/internal/cli"
	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
)

func runTerraformDoctor(ctx context.Context, command cli.Command, out io.Writer) (resultErr error) {
	finish := diagnostics.Step(ctx, diagnostics.EventDoctor)
	defer func() { finish(resultErr) }()

	target := azure.TerraformBackend{
		Subscription: command.Subscription,
		Account:      command.Account,
		Container:    command.Container,
	}
	if err := azure.ValidateTerraformBackend(target); err != nil {
		return err
	}

	environment := os.Environ()
	var session *doctorSessionStatus
	if os.Getenv("BIVROST_SESSION") != "" || os.Getenv("BIVROST_CONTROL_FILE") != "" {
		if os.Getenv("BIVROST_SESSION") == "" || os.Getenv("BIVROST_CONTROL_FILE") == "" {
			return errors.New("active Bivrost session markers are incomplete; reconnect before probing a private Terraform backend")
		}
		var err error
		session, err = currentDoctorSession(ctx)
		if err != nil {
			return errors.New("active Bivrost session status is unavailable; reconnect before probing a private Terraform backend")
		}
		if err := checkProxy(ctx, session.Config, ""); err != nil {
			return errors.New("active Bivrost session proxy is unavailable or does not match its authenticated configuration; reconnect before probing a private Terraform backend")
		}
		// Use the proxy authenticated by the session controller. Shell proxy
		// drift cannot silently make this probe bypass the active session.
		environment = proxyEnvironment(environment, session.Config.ProxyURL())
	}

	endpoint, err := azure.TerraformBlobEndpoint(ctx, target.Account, environment)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "[OK] Terraform backend target: subscription %s, account %s, container %s (explicit; no Azure CLI or provider selection changed)\n",
		strconv.QuoteToASCII(target.Subscription), strconv.QuoteToASCII(target.Account), strconv.QuoteToASCII(target.Container))
	if session != nil {
		fmt.Fprintf(out, "[OK] Active Bivrost connection: environment %s, Bastion subscription %s (separate from the explicit backend target)\n",
			strconv.QuoteToASCII(session.ProfileEnvironment), strconv.QuoteToASCII(session.Config.Subscription))
	}
	fmt.Fprintf(out, "[OK] Azure Blob endpoint: %s (derived from the active supported Azure CLI cloud)\n", endpoint)

	host := strings.TrimPrefix(endpoint, "https://")
	if session == nil {
		fmt.Fprintln(out, "[NOT VERIFIED] Terraform backend routing configuration: using ambient network and proxy settings; run inside a matching Bivrost session when the endpoint requires a private route")
	} else if containsExactHost(session.Config.PrivateHosts, host) {
		fmt.Fprintln(out, "[OK] Terraform backend routing configuration: the authenticated active Bivrost session has an exact private route for this endpoint; reachability is verified only by the metadata probe")
	} else {
		fmt.Fprintln(out, "[NOT VERIFIED] Terraform backend routing configuration: the authenticated active Bivrost session proxy will be used, but this endpoint is not an exact private route and follows the session's ambient route")
	}
	if session == nil || !containsExactHost(session.Config.PrivateHosts, host) {
		writeTerraformRouteHint(out, host, session, os.Getenv("BIVROST_SWITCH_ALLOWED") == "1")
	}

	if err := azure.ProbeTerraformBackend(ctx, target, endpoint, environment); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		fmt.Fprintln(out, "[ACTION NEEDED] Terraform backend metadata: "+azure.TerraformProbeGuidance(err))
		return errors.New("Terraform backend diagnostic needs attention; follow the guidance above")
	}
	fmt.Fprintln(out, "[OK] Terraform backend metadata: the signed-in Azure CLI identity can read properties for the named container")
	fmt.Fprintln(out, "This verifies only container-properties access. It does not prove blob read, write, lease, lock, init, or plan permissions, and it did not list or read blobs or download state.")
	return nil
}

// Custom profile paths and deliberately skipped registry login cannot be
// reconstructed as a named-environment switch. Keep their original options.
func writeTerraformRouteHint(out io.Writer, host string, session *doctorSessionStatus, switchAllowed bool) {
	if session != nil && switchAllowed && profile.ValidEnvironmentName(session.ProfileEnvironment) &&
		session.ProfileEnvironment != "custom-profile" && !(session.Enabled && !session.LoginRefreshed) {
		args := []string{"bivrost", "switch", "-e", session.ProfileEnvironment}
		for _, existing := range session.Config.PrivateHosts {
			args = append(args, "--private-host", existing)
		}
		if !containsExactHost(session.Config.PrivateHosts, host) {
			args = append(args, "--private-host", host)
		}
		if session.Enabled {
			args = append(args, "--acr")
		}
		fmt.Fprintln(out, "If this backend requires private access, reconnect this session with:")
		fmt.Fprintln(out, "  "+strings.Join(args, " "))
		fmt.Fprintln(out, "This closes the current shell and reconnects; existing private routes and enabled ACR access are included. Then repeat this diagnostic.")
		return
	}
	fmt.Fprintln(out, "If this backend requires private access, add this option to your original bivrost connect command:")
	fmt.Fprintf(out, "  --private-host %s\n", host)
	if session != nil {
		fmt.Fprintln(out, "Exit this shell first, then reconnect with the added option and repeat this diagnostic.")
	} else {
		fmt.Fprintln(out, "Connect with that option, then repeat this diagnostic inside the new shell.")
	}
}

func containsExactHost(hosts []string, target string) bool {
	for _, host := range hosts {
		if host == target {
			return true
		}
	}
	return false
}
