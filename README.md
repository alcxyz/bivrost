# Bivrost

Bivrost is a local session CLI for reaching Azure Bastion and SSH based
development environments from a shell on your own computer.

The public repository is `alcxyz/bivrost` on GitHub. GitHub is canonical;
Forgejo is a mirror. Bivrost is released under the MIT License.

## What a session does

- Uses your local Azure CLI identity. `bivrost login` opens the normal Azure
  browser and MFA flow; Bivrost does not create a shared identity or grant
  access.
- Opens a local shell whose private network traffic uses Azure Bastion and SSH.
- Creates a temporary kubeconfig for `kubectl` when the selected environment
  has Kubernetes details. It is scoped to the child shell and leaves your
  normal kubeconfig context alone. Missing Kubernetes tools or unavailable AKS
  credentials leave other platform commands usable with an isolated empty
  kubeconfig; the session reports the limitation instead of selecting your
  normal cluster.
- Uses Podman on the local computer, either its native Linux engine or a local
  Podman Machine. Podman configuration, proxying, and the optional registry
  engine are session-scoped. Docker is not supported.

End the shell to close its tunnels, temporary files, and session-owned
processes.

## Install a release

Download the archive for your operating system and CPU from
[GitHub Releases](https://github.com/alcxyz/bivrost/releases), together with its
`bivrost_<version>_checksums.txt`. Linux/macOS archives are `.tar.gz`; Windows
archives are `.zip`. Extract the archive and put `bivrost` (or `bivrost.exe`)
on your PATH, then run `bivrost version` and `bivrost --help`.

Verify the archive's SHA-256 against its entry in the checksum file using
`sha256sum <archive>` on Linux, `shasum -a 256 <archive>` on macOS, or
`Get-FileHash <archive> -Algorithm SHA256` in PowerShell. Checksums detect changed
contents; the first release does not provide artifact signatures.

The archive contains neutral example configuration, not deployment endpoints or
credentials. Your deployment supplies configuration separately. Azure CLI,
OpenSSH, tools for Kubernetes access and optional Podman remain prerequisites; the binary
does not install them. Windows builds are CI-tested; live Windows Podman Machine
QA remains tracked in [issue #6](https://github.com/alcxyz/bivrost/issues/6).

### Release maintenance

Update `VERSION` on `dev`, then open a promotion PR from `dev` to `main`.
CI tests all three operating systems and
checks GoReleaser snapshot archives. Main publishes a new version only after
those checks pass. Existing tags and published assets are never replaced.
Retry an interrupted release by rerunning its original workflow; a newer commit
needs a new version. Local archive validation uses
`goreleaser release --snapshot --clean` followed by
`python3 scripts/check-release-archives.py`; use the toolchain in `.go-version`
and GoReleaser version pinned in the workflow.

## Quick start

Install Azure CLI and OpenSSH. Kubernetes access also needs `kubectl` and
`kubelogin`; install Podman when using registry access.

```text
bivrost list
bivrost login
bivrost doctor -e <environment>
bivrost connect -e <environment>
```

`bivrost list` shows configured targets, their source, capabilities and PIM
requirements without signing in or opening a connection. `environments`, `env`
and `envs` remain aliases. Listing a target does not verify access.

Inside a Bash, Zsh or PowerShell session, use `bivrost switch -e <environment>`
to close the current session and open a fresh one. Add `--acr` for registry
access in the new session. Finish shell jobs first; shell-local variables and
directory changes are not carried over. If the new connection fails after
cleanup, you return to your original terminal. Other shells can use `exit`
followed by `bivrost connect`.

Repeat `--private-host HOST` on `connect`, `acr connect`, or `switch` when the
selected target needs additional exact private DNS hosts. These additions apply
only to the new session, are combined with the selected profile's
`private_hosts`, and are not written to the profile or catalogue. A switch does
not carry additions from the old session unless they are supplied again.
Inside that session, `bivrost doctor` without a target uses the effective
session configuration, including these additions.

This routing option does not configure Terraform authentication, backend,
workspace, variables, or provider subscriptions, and Bivrost does not inspect
or operate on Terraform state. Terraform retains the project's configuration
and authentication selection; backends and providers configured for Azure CLI
authentication can use the user's existing local Azure CLI login.

### Terraform baseline (development branch)

Inspect the subscriptions visible through your existing local Azure login:

```text
bivrost list subscriptions
bivrost list subscriptions --refresh
```

The first command uses Azure CLI's local subscription list; `--refresh` asks
Azure CLI to refresh it from Azure. The list includes enabled subscriptions in
the current Azure cloud. Neither command signs in, selects a
subscription, or proves permission to read a storage container. The default
marker shows Azure CLI's current selection, not an override for your project.

Then connect with the exact private backend hostname, if it is not already in
the profile's `private_hosts`:

```text
bivrost connect -e example --private-host examplebackend.blob.core.windows.net
```

Run your usual Terraform commands inside the shell. Configure backend and
provider subscriptions in your project as usual; the Bastion subscription can
be different. Bivrost does not change the Azure CLI selection or set Terraform
authentication variables. Subscription discovery and private routes work without
Heimdal.

Kubernetes setup is attempted automatically. If its client tools are missing
or AKS credentials cannot be acquired, the shell still opens for Terraform and
other platform commands, and `bivrost doctor` explains the Kubernetes limitation.
Restore the missing tools or access and reconnect to retry Kubernetes setup.
Cancellation, unsafe generated configuration, and failures in shared transport
or local file protection still stop connection setup.

Backend-specific diagnostics and generic command execution remain planned in
[issue #5](https://github.com/alcxyz/bivrost/issues/5). Bivrost does not run
`terraform init`, read state, or acquire a backend lock during connection or
subscription discovery. Terraform commands you run yourself retain their normal
state and locking behavior.

### Profiles and registry access

For a standalone profile, copy the shipped `config.example.json`, fill in
your deployment's values, and select it explicitly:

```text
bivrost connect --config ./config.example.json
```

Keep a filled profile private. Do not commit deployment values or credentials
to the public repository.

Use `bivrost ssh -e <environment>` for a shell on the management VM. Add
`--acr` to `connect` when the local shell needs private container images:

```text
bivrost connect -e <environment> --acr
```

Inside a supported Bivrost shell, `bivrost acr enable` enables registry access
for that session. `doctor` performs real prerequisite and live probes when a
session is available. It can pull the configured diagnostic image; use
`--no-pull` to skip that step. Podman may reuse already cached layers.

Azure and Kubernetes still enforce the permissions of your local identity.
Listing an environment or reading its catalogue entry is not authorization.

## Environment configuration

Public Bivrost code consumes a generic JSON map of environment names to
connection metadata. A downstream deployment can point Bivrost at that map
with `BIVROST_CATALOGUE_FILE`:

```json
{
  "environment-name": {
    "subscription": "<subscription-id>",
    "registry_subscription": "<registry-subscription-id>",
    "registry": "<registry-name>",
    "bastion_name": "<bastion-name>",
    "bastion_resource_group": "<resource-group>",
    "vm_resource_id": "<vm-resource-id>",
    "proxy_port": 18080,
    "socks_port": 18081,
    "aks": {
      "name": "<cluster-name>",
      "resource_group": "<cluster-resource-group>",
      "subscription": "<cluster-subscription-id>"
    },
    "private_hosts": [],
    "requires_pim": false
  }
}
```

The catalogue contains metadata only. It must not contain passwords, access
tokens, private keys, or other credentials. A user may override an environment
with `XDG_CONFIG_HOME/bivrost/environments/<name>.json`. When
`XDG_CONFIG_HOME` is unset, Bivrost uses the platform's native user
configuration directory; on macOS this is the directory returned by the
operating system's user-configuration API (normally `Library/Application
Support`) unless `XDG_CONFIG_HOME` is set. `--config PATH` selects an explicit
JSON profile and is mutually exclusive with `--env`.

## Platform status

Windows native Podman Machine live QA is pending. Linux and macOS behavior is
informed by testing in predecessor work; that history is not a release claim
for this extraction. See [the roadmap](docs/roadmap/README.md) for the public
validation and distribution work.

## Design records

The [ADR index](docs/adr/README.md) records the current session and packaging
boundaries and separates accepted future directions from proposed work.

## Development

From the repository root, build and run the local command with:

```text
go build ./cmd/bivrost
go run ./cmd/bivrost --help
```

### Source layout

The executable lives in `cmd/bivrost`. Internal packages separate command parsing,
configuration, diagnostics, proxy transport, shell hooks and Podman wrapping from
session lifecycle coordination. See [ADR 0011](docs/adr/0011-go-package-layout.md)
for package ownership and dependency rules.

### Branches and releases

`dev` is the default development branch. Open feature PRs against `dev`;
its CI tests and builds snapshots without publishing releases. Promote `dev`
to protected `main` with a new `VERSION` when preparing a release. The
promotion check rejects feature branches, unchanged versions and existing tags.
Squash feature PRs and release promotions, then merge `main` back into `dev`
after each release promotion.

Consumers tracking `main` receive the release line; consumers tracking `dev`
explicitly opt into unreleased work. Release tags identify immutable published
builds. See [ADR 0007](docs/adr/0007-release-distribution.md).
