package cli

import (
	"strings"
)

func canonicalHelpTopic(topic string) string {
	switch topic {
	case "environments", "env", "envs":
		return "list"
	case "v", "-v", "--version":
		return "version"
	default:
		return topic
	}
}

func HelpText(topic string) string {
	switch topic {
	case "":
		return `Bivrost — local platform access through Azure Bastion

Usage: bivrost <command> [options]

Access
  connect       Open a local shell for platform commands
  switch        Reconnect the active shell to another environment
  session       Share the active Kubernetes session with other local tools
  ssh           Open a shell on the management VM
  acr           Access container images with Podman

Setup
  doctor        Check prerequisites for an environment
  doctor terraform
                Probe explicit Azure Blob backend container metadata
  login         Sign in locally with Azure CLI, when needed
  list          List connection targets (aliases: environments, env, envs)
  list subscriptions
                List subscriptions visible to the current Azure CLI login
  config init   Create optional user settings
  version       Show the build version (aliases: v, -v, --version)

Start here
  bivrost list
  bivrost doctor -e example
  bivrost connect -e example
  bivrost connect -e example --acr

Help: bivrost <command> --help  or  bivrost help <command>
For image access: bivrost acr --help
`
	case "session", "session publish", "session unpublish", "session path", "session clean":
		return `Share a Kubernetes session with other local tools, explicitly.

Usage: bivrost session <command>

Commands
  publish     Publish this session's temporary kubeconfig; print its path
  unpublish   Withdraw it and close published client connections
  path        Print the current published kubeconfig path
  clean       Remove stale publications (can run outside a session)

Publish, unpublish and path require an active Bash, Zsh or PowerShell session.
Publish and path print only a path, suitable for use with --kubeconfig.
Inside the connected shell, plain k9s uses the session's KUBECONFIG.
In another terminal, use k9s --kubeconfig PATH with the published path.
Plain k9s there does not automatically discover published sessions.
Freelens/Lens can discover them when configured to watch the publication
directory. Bivrost does not change client settings or your normal kubeconfig.
kubelogin uses your local
Azure CLI identity. Publishing does not grant additional permissions.

Publications live under $XDG_RUNTIME_DIR/bivrost/published, or
${XDG_STATE_HOME:-$HOME/.local/state}/bivrost/published when unset.
Switching withdraws the old publication and publishes a new target with a
new path. Existing clients are disconnected, never silently retargeted.
Exit withdraws the publication. After a crash, stale files cannot connect;
clean removes them, and publish also performs this cleanup.
`
	case "acr":
		return `Enable local Podman access to private container images.

Usage: bivrost acr <command> [options]

Commands
  enable   Enable ACR in the current Bivrost shell
  connect  Compatibility alias for bivrost connect --acr
  proxy    Run an advanced standalone HTTPS proxy
  login    Refresh registry login over an advanced connection
  doctor   Check Podman and live ACR connectivity

Start with ACR
  bivrost connect -e example --acr

Or enable it inside an existing Bivrost shell
  bivrost acr enable

Deferred activation supports Bash, Zsh, and PowerShell. Other shells
can use --acr at startup. Podman is required only when enabling ACR.
Exit closes the session connections. Images stay on your computer.

Help: bivrost acr <command> --help
`
	case "acr enable":
		return `Enable Podman registry access in the current Bivrost shell.

Usage: bivrost acr enable

Uses the current session's target, identity, and existing tunnel.
Authenticates Podman and applies temporary settings to this shell.
Calling it again safely reports that ACR is already enabled.

Supported inside Bivrost's Bash, Zsh, and PowerShell sessions.
For other shells, start with bivrost connect -e NAME --acr.
No environment, configuration, or other activation options are accepted.

Options
  -h, --help  Show this help
`

	case "login":
		return `Sign in with Azure CLI on this computer.

Usage: bivrost login [options]

Options
  -t, --tenant TENANT  Choose an Azure tenant
  -d, --debug          Record a bounded local diagnostic log
  -h, --help           Show this help

Use this initially or when Azure requires reauthentication.
An existing az login works too. Connect and ACR commands reuse
that sign-in; they do not call bivrost login automatically.
This does not sign Azure CLI into the management VM.

Example: bivrost login -t YOUR-TENANT-ID
`
	case "config", "config init":
		return `Create optional user preferences without overwriting existing settings.

Usage: bivrost config init

Settings control the shell prompt.
Defaults work without a file. The file uses XDG_CONFIG_HOME when
set, otherwise the platform's native user configuration directory.
Environment profiles can come from BIVROST_CATALOGUE_FILE or from
the environments directory beside this settings file.

Options
  -h, --help  Show this help
`
	case "list":
		return `List configured connection targets from the catalogue and local profiles.

Usage: bivrost list
Aliases: bivrost environments, bivrost env, bivrost envs

Shows configuration source, configured Kubernetes/registry capabilities and PIM
requirements. No login or tunnel is needed. Capabilities describe configuration,
not verified connectivity or permissions.

Listing an environment does not grant access. Azure and Kubernetes
enforce your permissions. Profiles marked requires_pim require activation.

To discover subscriptions visible to your Azure CLI login, run:
  bivrost list subscriptions [--refresh]

Options
  -h, --help  Show this help
`
	case "list subscriptions":
		return `List Azure subscriptions visible to the current Azure CLI login.

Usage: bivrost list subscriptions [--refresh]

Shows subscription name, ID, tenant, state, and which subscription Azure CLI
currently marks as default. Discovery does not change the selected subscription,
sign in, grant access, inspect storage, or read Terraform state.

Options
      --refresh  Retrieve an up-to-date subscription list from Azure
  -h, --help     Show this help

Without --refresh, Azure CLI may use its local subscription cache.
The list includes enabled subscriptions in the current Azure cloud.
If discovery fails, run bivrost login or az login, check your access and
connectivity, then retry.
`
	case "doctor terraform":
		return `Check explicit Azure Blob backend container metadata with your Azure CLI login.

Usage: bivrost doctor terraform --subscription NAME_OR_ID --account NAME --container NAME [options]

The command derives the Blob endpoint from the active supported Azure CLI cloud,
then requests properties for only the named container with Microsoft Entra login.
It does not select a subscription globally, set ARM variables, require Terraform,
list or read blobs, download state, run init, or acquire a state lock.

Inside an authenticated Bivrost session, the probe is forced through that
session's proxy and reports whether the endpoint has an exact private route.
Outside a session, ambient network and proxy settings apply. A failed probe does
not by itself distinguish login, authorization, target, or network failures.
If a private route is missing, supported named-environment sessions get a
bivrost switch suggestion including existing private routes and enabled ACR.
Otherwise, add the suggested --private-host option to your original connection
command and reconnect. Custom profiles and skipped ACR login use this fallback.

Required target
      --subscription NAME_OR_ID  Project-owned backend subscription
      --account NAME             Azure storage account name
      --container NAME           Azure Blob container name

Options
  -d, --debug  Record a bounded local diagnostic log
  -h, --help   Show this help
`
	case "version":
		return `Show the installed Bivrost build version.

Usage: bivrost version
Aliases: bivrost v, bivrost -v, bivrost --version

Options
  -h, --help  Show this help
`
	}
	var description, notes, example string
	switch topic {
	case "connect":
		description = "Open a local shell with platform connectivity."
		notes = "Azure sign-in is reused. Proxy variables and kubeconfig apply only\nto this shell; your personal Kubernetes context stays unchanged. Kubernetes\nsetup is automatic; missing client tools or unavailable AKS credentials leave\nother commands usable with an isolated empty kubeconfig.\nRepeat --private-host for exact DNS hosts needed only by this session;\nthese additions are not saved. Exit the shell to disconnect. Add --acr\nfor Podman registry access, or run bivrost acr enable inside a Bash, Zsh,\nor PowerShell session. Podman is required only when ACR is enabled."
		example = "bivrost connect -e example"
	case "switch":
		description = "Close the active session and connect to another environment."
		notes = "Run inside a Bivrost Bash, Zsh, or PowerShell session. Finish shell jobs\nbefore switching; in PowerShell, remove finished job records with Remove-Job.\nThe target configuration is validated before leaving. A fresh shell opens after\ncleanup; shell-local variables, directory changes, and prior --private-host\nadditions are not carried over. Repeat --private-host for exact DNS hosts needed\nby the new session. Add --acr to enable registry access in the new session.\nIf the new connection fails, you return to your original terminal; the old\nsession is not restored. Provider permissions and PIM still apply."
		example = "bivrost switch -e example --acr"
	case "ssh":
		description = "Open an interactive shell on the management VM."
		notes = "Uses local Azure sign-in for SSH authentication. Commands inside\nthe VM use the VM's own environment and Azure authentication."
		example = "bivrost ssh -e example"
	case "doctor":
		description = "Check environment prerequisites without changing your setup."
		notes = "Inside a supported Bivrost session, the target defaults to that session.\nChecks active Podman settings and registry login history, plus read-only\nKubernetes API and node-list requests in a matching session.\nWith ACR enabled, pulls the configured diagnostic image (cached locally).\nUse --no-pull to skip this check. For an explicit Azure Blob backend metadata\nprobe, use bivrost doctor terraform --help. NOT VERIFIED means a live or manual\ncheck is still needed. Does not install tools, log in, start tunnels, activate\nPIM, or restart services."
		example = "bivrost doctor -e example"
	case "acr proxy":
		description = "Run an advanced standalone HTTPS proxy for Podman."
		notes = "Advanced transport mode; persistent service proxy settings are manual.\nFor automatic setup and cleanup, use bivrost connect --acr.\nDo not run this proxy on the same port as an ACR session."
		example = "bivrost acr proxy -e example"
	case "acr connect":
		description = "Compatibility alias for bivrost connect --acr."
		notes = "Starts its own proxy and Bastion connection, then authenticates Podman.\nProxy and Podman settings apply only to this shell. Repeat --private-host for\nexact DNS hosts needed only by this session; these additions are not saved.\nExit to disconnect. Uses an existing local Podman engine or running Podman\nMachine. Stop a standalone acr proxy first if it occupies the same port."
		example = "bivrost acr connect -e example"
	case "acr login":
		description = "Refresh Podman's registry login."
		notes = "Advanced standalone-proxy login. ACR sessions log in automatically;\nexit and reconnect to refresh a session. Uses local Azure sign-in and\nPodman credential storage. This is not a new Azure login."
		example = "bivrost acr login -e example"
	case "acr doctor":
		description = "Check Podman prerequisites and live ACR connectivity."
		notes = "Requires an existing proxy and connection for network checks.\nFor an initial setup check without a tunnel, use bivrost doctor.\nA successful check does not verify image push/pull permissions."
		example = "bivrost acr doctor -e example"
	default:
		return ""
	}
	var b strings.Builder
	if topic == "doctor" {
		b.WriteString(description + "\n\nUsage: bivrost doctor [-e NAME | -c PATH] [options]\n\nTarget (optional inside an active Bivrost session)\n")
	} else {
		b.WriteString(description + "\n\nUsage: bivrost " + topic + " (-e NAME | -c PATH) [options]\n\nTarget (choose one)\n")
	}
	b.WriteString("  -e, --env NAME     Catalogue environment or local profile\n  -c, --config PATH  Explicit custom profile\n\nOptions\n")
	if topic == "doctor" {
		b.WriteString("      --no-pull      Skip the diagnostic image pull\n")
	}
	if topic == "connect" || topic == "switch" {
		b.WriteString("      --acr          Enable Podman registry access at startup\n")
	}
	if topic == "connect" || topic == "acr connect" || topic == "switch" {
		b.WriteString("      --private-host HOST\n                     Route one exact private DNS host (repeatable)\n")
	}
	if topic == "acr connect" || topic == "connect" {
		b.WriteString("  -n, --no-login     Connect without refreshing registry login\n")
	}
	b.WriteString("  -d, --debug        Record a bounded local diagnostic log\n  -h, --help         Show this help\n\n")
	b.WriteString(notes + "\n")
	b.WriteString("\nExample: " + example + "\n")
	return b.String()
}
