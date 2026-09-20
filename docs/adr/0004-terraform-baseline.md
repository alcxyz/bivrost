# ADR 0004: Terraform baseline and local Azure discovery

- Status: Accepted future direction
- Date: 2026-09-20
- Scope: Future work; no implementation is claimed here.

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

## Alternatives considered

- A Bivrost wrapper that silently migrates or reads a backend was rejected
  because it hides state ownership and can change a deployment unexpectedly.
- Blocking all user-invoked Terraform state behavior was rejected because it
  would make ordinary `plan` and locking semantics unusable.
