# ADR 0014: Heimdal bootstrap reference implementation

- Status: Accepted for the reference example; CLI generation and live QA pending
- Date: 2026-09-28
- Scope: Azure Public Cloud infrastructure example, extending ADR 0013

## Context

Heimdal bootstrap needs concrete infrastructure definitions before a CLI can
produce them. It must fit adopter review/CI workflows and avoid a dependency on
its own storage for initial state. Metadata publication and user onboarding are
separate work; today's document schema supplies routes, not environments.

## Decision

Use ordinary Terraform/OpenTofu-compatible HCL as the first reference format.
The adopter copies/version-pins it, selects an engine and manages its dependency
lock file and independent state backend. Bivrost does not execute apply or keep
a second ownership inventory. Future generation must preserve this boundary.

Start with Azure Public Cloud and an existing resource group, subnet and private
DNS zone in one explicitly selected subscription, plus existing principals. Create a dedicated metadata account, Blob container,
private endpoint/DNS association and container-scoped role assignments. Do not
create or adopt shared groups, networks, PIM policies or workload federation.
Use explicit deployment inputs and private access; disable Shared Key and
anonymous access. Use management-plane resource creation so bootstrap does not
need Blob keys or an already-working private data-plane path to create the
container. Publishing and consumption still require that path and Entra roles.

Reference supplied consumer groups, PIM-governed publisher groups and CI
principals independently. The template grants roles; it does not prove that a
group is PIM-governed or neutralize inherited grants. Infrastructure deployment
rights and routine metadata publication must use separate operating identities.

Emit only credential-free source locators and ownership references. Do not call
these a complete consumer onboarding contract: explicit bootstrap transport and
trust settings, catalogue schema and source precedence remain to be designed in
#35. No runtime command naming changes are part of this example.

See the [bootstrap guide](../heimdal-bootstrap.md) for inputs, ownership, state,
CI separation and live validation gates. Retirement stays deferred under ADR 0013.

## Consequences

The example can be reviewed and validated without deploying anything. Adopters
need existing state, network and identity foundations; the first version is not
a turnkey subscription provisioner. Versioning/soft delete aid recovery but do
not establish immutable metadata or instant privilege revocation.

This selects a reference format, not a universal cloud provisioning abstraction.
Other cloud implementations and alternative adopter tooling remain possible.
Validation against provider schemas does not prove live Azure policy compatibility,
private DNS, RBAC behavior or repeatable apply; disposable QA remains necessary.

## Alternatives considered

- Azure Bicep fits Azure deployments but does not share the Terraform/OpenTofu
  workflow used for this first reference. It remains a possible future example.
- Direct Azure SDK provisioning would require ownership/reconciliation logic
  already supplied by IaC and is outside the agreed generation-only boundary.
- An all-in-one subscription/network/group bootstrap would take ownership of
  shared foundations and identity policy that belong to the adopter.
- Hosting infrastructure state in the new metadata container creates an initial
  dependency cycle and mixes state access with consumer metadata permissions.
