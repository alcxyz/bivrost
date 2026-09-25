# ADR 0004: Terraform baseline and local Azure discovery

- Status: Accepted; baseline implementation on `dev`
- Date: 2026-09-20
- Scope: Incremental implementation on `dev`; explicit backend metadata diagnostics implemented.

## Context

Infrastructure workflows need a predictable local baseline without turning a
connection helper into a hidden provisioning system. Subscription discovery
is useful for choosing a target, but discovery must not be confused with
permission grants.

## Decision

Future Terraform support will use normal local commands and the user's visible
Azure CLI identity. Subscription discovery will be user-visible and read-only
with respect to access: it may enumerate subscriptions the identity can see,
but it will not grant roles, activate access, or hide a change in scope.

The Bivrost discovery and diagnostic paths will not implicitly download or
inspect Terraform state, migrate a backend, or acquire a state lock. A user
who explicitly runs ordinary Terraform retains Terraform's normal behavior,
including `plan` state reads and locks. The baseline includes ordinary `kubectl` and Terraform commands without an
extra kube enable ceremony. If the Azure Kubernetes target is unavailable, a
session that can otherwise run Terraform remains usable and reports the
Kubernetes limitation clearly. It never falls back to the ambient Kubernetes
context.

Project configuration owns the backend, workspace, variables and provider
subscriptions; a connection environment must not silently replace them.
Discovery can enumerate visible subscriptions and storage using the personal
identity, but visibility proves neither data-plane access nor network reachability.
Use non-secret project metadata or configured tags for discovery, with an
explicit target fallback for resources that are usable but not enumerable.
Do not infer the cause of an Azure 403 without supporting evidence, retrieve
storage keys as a fallback, or implicitly run init migration, apply or unlock.

## Consequences

Users can inspect every meaningful command and retain ownership of Terraform
state and backends. A future implementation cannot assume that Azure
Kubernetes is available and cannot let that limitation abort an otherwise
Terraform-capable session.

## Implementation progress

The development branch supports exact session-only private host routes and
read-only `bivrost list subscriptions [--refresh]` using the local Azure CLI
identity. Listing does not select a subscription or change project settings.
Backend and provider subscription selection remains owned by the project;
there is no implicit `ARM_SUBSCRIPTION_ID` or global `az account set` override.

Kubernetes preparation remains automatic. Missing Kubernetes tools and AKS
credential acquisition failures permit a session with an isolated empty
kubeconfig. Cancellation and generated-config integrity or local-file safety
failures remain fatal. The configured Kubernetes target is retained for a
fresh attempt on reconnect; `doctor` reports the unavailable capability without
probing an ambient context. Shared transport failures still end the session.

`bivrost doctor terraform` accepts an explicit subscription, storage account
and container. It derives the Blob endpoint from the active supported Azure CLI
cloud and uses `az storage container show --auth-mode login` with suppressed
output. Microsoft documents this command as returning the named container's
system properties and user-defined metadata without its blob list, and documents
`login` mode as Microsoft Entra authorization rather than storage-key fallback:
[Get Container Properties](https://learn.microsoft.com/rest/api/storageservices/get-container-properties),
[Azure CLI data authorization](https://learn.microsoft.com/azure/storage/blobs/authorize-data-operations-cli).
The diagnostic strips storage key, SAS, connection-string and endpoint
environment variables before invoking Azure CLI. It does not require Terraform,
select a global subscription, set provider variables, enumerate or read blobs,
download state, run init, or acquire a state lock.

Inside a Bivrost session, the diagnostic authenticates the active controller
status and forces the Azure command through that session's proxy. It reports
whether the cloud-derived endpoint has an exact private route. Outside a session,
ambient network and proxy settings remain visible as an unverified route. A
failed container-properties request reports possible login, data-plane access,
target, cloud and network causes without guessing which caused an Azure 403.
Missing private routes produce an exact `--private-host` option to add to the
original connection command; the diagnostic does not modify routes or assume
that every backend requires private routing. Runtime route distribution remains
part of Heimdal, not a new local catalogue requirement.

Probe failures may receive fixed guidance for locally detected timeouts or
missing tools and narrowly recognised CLI login/network errors. A bounded
in-memory stderr sample is used only for classification, never displayed or
logged. Unknown or truncated output retains generic guidance. A network error
does not establish whether the failed request was to storage, identity or a proxy.

Generic command-execution lifecycle remains a future slice. Backend discovery
beyond the explicit diagnostic is also deferred. Neither requires Heimdal;
later runtime metadata can supply the same connection profile inputs.

## Alternatives considered

- A Bivrost wrapper that silently migrates or reads a backend was rejected
  because it hides state ownership and can change a deployment unexpectedly.
- Blocking all user-invoked Terraform state behavior was rejected because it
  would make ordinary `plan` and locking semantics unusable.
