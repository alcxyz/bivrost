# ADR 0009: Incremental cloud provider boundaries

- Status: Accepted
- Date: 2026-09-20
- Scope: Package boundaries now; additional cloud providers remain future work.

## Context

Bivrost currently depends on Azure identity, Bastion, AKS and ACR. Its session
ownership, shell integration and local tool configuration are useful independently
of that provider. Heimdal should not deepen the coupling between metadata
validation and Azure-specific acquisition.

## Decision

Keep Azure as the first concrete implementation while separating provider
operations from session orchestration incrementally. Start with the Azure CLI
launcher and operation argument construction in `internal/azure`. This package
must not import CLI configuration, prompt code or session orchestration.
Callers retain process ownership, cancellation policy, diagnostics and cleanup.
Do not move tokens into logs or broaden subprocess output while moving code.

As Heimdal is implemented, keep metadata schema validation and source selection
separate from Azure Blob retrieval. Deployment-specific locators remain supplied
by downstream configuration. Shared session and local tool behavior must not
require every future provider to offer equivalents of Bastion or PIM.

AWS and GCP are intended future providers, not currently supported backends.
Introduce small capability interfaces when a second concrete implementation
establishes their requirements. Do not introduce a universal provider interface,
plugin loader, provider-selection flags or a new configuration schema in this
preparatory refactor. Authentication, registry credentials, Kubernetes credentials,
transport and metadata storage may require separate capabilities rather than one
monolithic provider object.

## Dependency review and next boundaries

The current root package still owns configuration and lifecycle code. Azure
operations are spread across login parsing, Bastion session setup, kubeconfig
preparation, registry login and doctor. `platformServices` already supplies a
local seam for lifecycle tests; it is not a public cloud-provider contract.

The initial extraction removes Azure command launching from generic OS process
management and moves login/AKS argument construction behind the same package
boundary. Remaining credential acquisition, Azure-specific kubeconfig exec
validation, routing and configuration stay explicit. Extract them as cohesive
operations when needed, preserving their security and cleanup tests. This change
does not claim that the root package is cloud-neutral already.

## Consequences

The next metadata implementation has an Azure integration package to extend,
without making the current CLI or profiles incompatible. Cross-platform command
launching remains testable in isolation. Multi-cloud work still needs actual
provider implementations and capability-specific diagnostics.

## Alternatives considered

- Keeping every operation in the root package makes provider dependencies harder
  to distinguish as metadata support grows.
- Moving all lifecycle code at once would create unnecessary review and regression
  risk before another provider exists.
- Designing a universal provider framework now would encode unverified assumptions
  about authentication, transport and temporary privileges.
