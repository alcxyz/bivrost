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
  normal kubeconfig context alone. If Kubernetes tools are missing or AKS
  credentials cannot be obtained, the shell still opens with an isolated empty
  kubeconfig and reports the limitation. It never selects your normal cluster.
- Uses Podman on the local computer, either its native Linux engine or a local
  Podman Machine. Podman configuration, proxying, and the optional registry
  engine are session-scoped. Docker is not supported.

End the shell to close its tunnels, temporary files, and session-owned
processes.

**Use a trusted personal workstation.** Other local users and processes on the
same computer can reach ordinary session tunnels; binding to loopback is not
per-user isolation. Podman registry credentials may persist after disconnect.
Read the [security boundaries](docs/security-boundaries.md) before deploying
Bivrost.

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

`bivrost doctor terraform` checks an explicitly selected backend container's
properties; it does not establish state read/write or lease permissions. The
remaining Terraform workflow and generic command execution are tracked in
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
configuration directory; on macOS that is normally `~/Library/Application
Support`. `--config PATH` selects an explicit JSON profile and is mutually
exclusive with `--env`.

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

## Share a Kubernetes session with local tools

Inside a connected session:

```sh
bivrost session publish
bivrost session path
bivrost session unpublish
```

Discovery depends on where and how you launch the client:

| Client | How it finds the session |
| --- | --- |
| `k9s` inside Bivrost | Inherits the connected shell's `KUBECONFIG`; publishing is unnecessary. |
| `k9s` in another terminal | Use `k9s --kubeconfig PATH` with the published path. Plain `k9s` does not automatically discover publications. |
| Freelens/Lens | Can discover publications when configured to watch the publication directory alongside normal kubeconfig sources. |

Publishing does not change another terminal's environment. A cluster appearing
in Freelens does not mean it will also appear in plain `k9s` outside Bivrost.

`publish` prints a kubeconfig path. Pass it explicitly to another terminal's
`k9s --kubeconfig PATH` or `kubectl --kubeconfig PATH`. The file includes the
exec authentication setup and a session-owned proxy; that terminal does not
need the connected shell's proxy variables.

Desktop clients can watch `$XDG_RUNTIME_DIR/bivrost/published`, or
`${XDG_STATE_HOME:-$HOME/.local/state}/bivrost/published` when XDG_RUNTIME_DIR is
unset. Add that directory alongside your normal kubeconfig sources using the
client's supported settings. Bivrost does not configure the client for you.

Publishing is opt-in and does not change your normal kubeconfig or context.
Unpublish disconnects published clients while leaving the connected shell
usable. Exit removes the publication. Switching creates a new context and path;
select it explicitly in external clients. If the new session has no Kubernetes
access, nothing is published.

After a crash, a stale publication file may remain. Its capability cannot
authenticate to a replacement session's gateway. `bivrost session clean` removes
recognised stale publications; publish also performs this cleanup. The files
are private local capabilities: do not share them, commit them, or include
their contents in logs.

Linux and macOS desktop discovery is configured separately from Bivrost. Native
Windows and real GUI-client behavior require live QA.

## Initialise a Heimdal metadata source (development)

Heimdal uses one existing Azure container by default, named `heimdal`. Each
connection environment has its own blob prefix inside that shared container.
You must name the storage account and the publishing subscription explicitly:

```sh
bivrost heimdal init -e example --subscription SUBSCRIPTION --account ACCOUNT \
  --private-host state.example.net
```

This publishes `environments/example/revisions/<sha256>.json`, then
`environments/example/current.json`. Use `--container` and `--prefix` to
change those defaults. The revision contains schema version 1, its environment,
issue/expiry timestamps, and exact private hosts only. Default validity is 24h;
`--valid-for` accepts a positive duration up to 168h. No credentials, hooks,
Terraform state, or complete connection profiles belong in this document.

Initialization uses the local Azure CLI identity. An administrator must already
provide the container and publishing permissions. Both writes are create-only;
existing revisions and pointers are never replaced by this command. If the
second upload is not confirmed, a revision may remain without a pointer, or the
pointer write may have succeeded before a timeout. The error reports this
uncertainty without deleting data.
Local staging files are private and removed on normal completion or handled
cancellation; an abrupt crash may leave them in the OS temporary directory.

For private storage, run inside a session whose bootstrap already routes the
metadata hostname. Inside a session, the command verifies the active session
through its controller, so it needs a Bash, Zsh, or PowerShell session; in
other shells it refuses to run because the session controller is unavailable.
The command prints the source locator for the downstream bootstrap
configuration. Separate read and publish permissions; an ordinary consumer
should not need publishing rights. A content digest verifies the referenced
bytes, not publisher identity; trust also depends on the configured source and
its access controls.

Initial publication and opt-in retrieval are available on `dev`. Updating the
current pointer and rollback commands remain subsequent work. Terraform-state
PIM access is not a prerequisite.

### Fetch metadata when connecting (development)

Add a `heimdal` object to your existing connection profile or downstream
catalogue entry. For example:

```json
{
  "heimdal": {
    "subscription": "METADATA-SUBSCRIPTION-ID",
    "account": "examplemetadata",
    "environment": "example",
    "container": "heimdal",
    "prefix": "environments/example",
    "allow_local_fallback": false
  }
}
```

This is a fragment, not a complete connection profile. `container` defaults to
`heimdal`; `prefix` defaults to `environments/<environment>`. The explicit
metadata environment is validated against both documents and can differ from
your local connection alias. The metadata subscription is independent of the
Bastion subscription; Bivrost does not change your Azure CLI selection.

For a private source, include its exact Blob hostname (for example
`examplemetadata.blob.core.windows.net`) in the profile's `private_hosts` or
pass it with `--private-host`. Metadata cannot supply the route needed to fetch
itself. An unconfigured private source may be unreachable; no automatic network
discovery or privilege activation is performed.

`connect`, `connect --acr` and managed `switch` fetch a fresh pointer and revision
after the bootstrap tunnel is ready. Bivrost validates them before adding the
routes and opening the shell. Use `bivrost doctor` inside the supported session
to inspect its effective configuration, including acquired routes. No response
is cached on disk. Expiry is checked when metadata is acquired; there is no
background refresh or automatic shutdown when metadata later expires.

A failed fetch or invalid document stops connection setup by default. Set
`allow_local_fallback` to `true` only when your local profile is sufficient:
Bivrost then reports the failure and opens using local routes alone, without
any routes from an earlier download. This includes visibly reported permission
denials and validation failures. Cancellation always stops setup. Profiles
without `heimdal` retain the normal local-only behavior. Standalone `ssh` and
proxy commands do not retrieve Heimdal metadata.

## Architecture diagrams

See the [visual architecture guide](docs/architecture.md) for local command execution,
Heimdal storage, permission boundaries and session lifecycle. The Heimdal layouts
are generic reference designs for adopters, not an installed configuration.
