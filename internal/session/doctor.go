package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/alcxyz/bivrost/internal/azure"
	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
)

// Doctor only inspects existing setup. It does not log in, install extensions,
// start tunnels, change container settings, or serialize subprocess output.
func platformDoctor(ctx context.Context, c profile.Profile, out io.Writer) error {
	return platformDoctorWithSession(ctx, c, out, nil)
}

func platformDoctorWithSession(ctx context.Context, c profile.Profile, out io.Writer, session *doctorSessionStatus) (resultErr error) {
	finish := diagnostics.Step(ctx, diagnostics.EventDoctor)
	defer func() { finish(resultErr) }()
	if err := c.ValidatePlatform(); err != nil {
		return err
	}
	failed := false
	report := func(status, label, detail string) {
		fmt.Fprintf(out, "[%s] %s: %s\n", status, label, detail)
		if status == "MISSING" || status == "ACTION NEEDED" {
			failed = true
		}
	}
	tools := []string{"az", "ssh"}
	if c.AKS != nil {
		tools = append(tools, "kubectl", "kubelogin")
	}
	if c.Registry != "" {
		tools = append(tools, "podman")
	}
	available := map[string]bool{}
	for _, name := range tools {
		_, err := exec.LookPath(name)
		available[name] = err == nil
		if err != nil {
			if name == "kubectl" || name == "kubelogin" {
				report("NOT VERIFIED", name, "missing; install this tool for Kubernetes access. Other platform commands can still use the session")
			} else {
				report("MISSING", name, "install this tool and make it available on PATH")
			}
		} else {
			report("OK", name, "available on PATH")
		}
	}
	if available["az"] {
		for _, extension := range []string{"bastion", "ssh"} {
			if doctorAzureCheck(ctx, "extension", "show", "--name", extension, "--output", "none") != nil {
				report("ACTION NEEDED", "Azure extension "+extension, "cannot verify installation; run az extension add --name "+extension)
			} else {
				report("OK", "Azure extension "+extension, "installed")
			}
		}
		if doctorAzureCheck(ctx, "account", "get-access-token", "--subscription", c.Subscription, "--output", "none") != nil {
			report("ACTION NEEDED", "Azure authentication", "cannot obtain a token for the configured Bastion subscription; run bivrost login and verify subscription access and network connectivity")
		} else {
			report("OK", "Azure authentication", "local login can obtain a token; this does not verify resource permissions")
		}
	} else {
		report("NOT VERIFIED", "Azure authentication and extensions", "Azure CLI is missing")
	}
	podmanReady := false
	if session != nil && session.Enabled {
		if !doctorSessionEnvironment(c, session) {
			report("ACTION NEEDED", "Podman command environment", "shell settings do not match the active ACR session; reconnect with --acr to restore them")
		} else if !available["podman"] || !doctorSessionEngine(ctx, session) {
			report("ACTION NEEDED", "Podman session", "the configured session engine is unavailable; reconnect with --acr")
		} else {
			podmanReady = true
			report("OK", "Podman command environment", "active ACR session settings match this shell and its engine is reachable")
		}
		if session.LoginRefreshed {
			report("OK", "ACR authentication", "registry login was refreshed by this session; current pull access is checked separately")
		} else {
			report("NOT VERIFIED", "ACR authentication", "registry login was skipped for this session")
		}
	} else if available["podman"] {
		machine, err := inspectPodman(ctx)
		if err != nil {
			report("ACTION NEEDED", "Podman setup", err.Error())
		} else if machine != nil {
			report("OK", "Podman connection", "selected connection matches a running local Podman Machine")
			if session == nil {
				report("NOT VERIFIED", "Podman Machine proxy", "bivrost connect --acr starts a session API and proxy forward automatically; normal Machine service settings are preserved")
			}
		} else {
			report("OK", "Podman", "native local engine available")
			if session == nil {
				report("NOT VERIFIED", "Podman command environment", "bivrost connect --acr opens a local shell with temporary Podman proxy settings; no manual environment setup is needed")
			}
		}
	}
	if session != nil && !session.Enabled {
		report("NOT VERIFIED", "ACR activation", "not enabled in this session; run bivrost acr enable when image access is needed")
	}
	if c.RequiresPIM {
		report("NOT VERIFIED", "PIM", "activate your eligible access before connecting; doctor does not activate or verify PIM")
	}
	if available["az"] && available["ssh"] && c.Registry != "" {
		proxyErr := error(nil)
		if session == nil {
			proxyErr = requireProxy(ctx, c)
		}
		if proxyErr != nil {
			report("NOT VERIFIED", "ACR connectivity", "run bivrost connect --acr with the same --env or --config; it starts the proxy and checks ACR connectivity automatically")
		} else if err := registryCheck(ctx, c); err != nil {
			report("NOT VERIFIED", "ACR connectivity", "proxy is running, but ACR is not reachable; verify the matching Bastion connection and private network access")
		} else {
			report("OK", "ACR connectivity", "TLS and registry endpoint reachable through the proxy")
		}
	} else {
		report("NOT VERIFIED", "ACR connectivity", "requires ACR configuration, tools, and an existing connection")
	}
	doctorResourceChecks(ctx, c, session, available["kubectl"], report)
	if c.Registry != "" {
		reportDoctorPull(ctx, c, podmanReady, report)
		report("NOT VERIFIED", "ACR image push", "requires a push to a specific repository; doctor does not upload images")
	}
	if failed {
		return errors.New("prerequisite checks need attention; follow the guidance above")
	}
	fmt.Fprintln(out, "Prerequisite checks completed. NOT VERIFIED checks still need live or manual validation.")
	return nil
}

func doctorAzureCheck(ctx context.Context, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd, err := azure.Command(ctx, args...)
	if err != nil {
		return err
	}
	// nil stdout/stderr discard both streams, including unexpected token output.
	return cmd.Run()
}
