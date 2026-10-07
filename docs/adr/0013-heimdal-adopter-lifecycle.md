# ADR 0013: Heimdal administrator, user and CI lifecycle

- Status: Accepted future direction; not implemented
- Date: 2026-09-28
- Scope: Infrastructure bootstrap, publication, user onboarding and ownership; retirement deferred.

## Context

The current development interface `heimdal init` publishes a route-only document
into existing storage. Consumers already need local connection profiles. This
is not the complete adoption lifecycle: administrators need reproducible cloud
infrastructure, CI needs ongoing publication, and users need to initialize
Bivrost from a trusted source and select environments supplied by that source.

Using init for both publication and consumer onboarding would obscure which
identity, permissions and side effects a command requires.

## Decision

Separate these responsibilities. The names below are planned interfaces, not
commands users can rely on in the current build.

| Actor | Planned interface | Responsibility |
| --- | --- | --- |
| Administrator | `bivrost heimdal bootstrap` | Generate versioned infrastructure definitions for review and deployment through the adopter's IaC workflow |
| Administrator or CI | `bivrost heimdal publish` | First publication and repeatable reconciliation of metadata |
| Consumer | `bivrost heimdal init --source <bootstrap-reference>` | Register a trusted source and verify access using local Azure identity |
| Consumer | `bivrost list`, `bivrost connect -e <environment>` | Discover and consume fresh environment definitions |
| Administrator | Retirement workflow, deferred | Remove explicitly owned resources using the existing IaC ownership model |

### Infrastructure bootstrap

The adopter's IaC state owns the cloud resource lifecycle. Bivrost must not
maintain a competing inventory or implicitly deploy changes merely by generating
definitions. The first reference format is selected in
[ADR 0014](0014-heimdal-bootstrap-reference.md); it does not add automatic deployment.

Inputs explicitly identify tenant, subscription, region, network choices and
identity groups. Do not silently use the Azure CLI's selected subscription.
Provide configurable storage naming with a `heimdal` container default and a
reference design separating metadata storage from Terraform state.

Separate consumer read grants, PIM-controlled human publishing and scoped CI
publishing. PIM policy remains administrator-controlled; existing groups and
state-maintenance restrictions must not be silently changed. Evaluate inherited
grants when validating the design. Reading metadata never grants target access.

Output a credential-free minimal onboarding reference: source locator, trust
settings and enough connection information to reach a private source without
first downloading its contents. Distinguish created resources from references
to shared resources. Publication is a later, separate operation.

### Publication and CI

`heimdal publish` will replace the administrative meaning of today's init.
It must support first publication and noninteractive, idempotent reconciliation:
unchanged sufficiently valid data is a no-op; changed data or required validity
renewal produces a validated revision and a conditional pointer update.
Define the renewal threshold explicitly. Protect concurrent writers and uncertain
request outcomes; never overwrite a newer pointer blindly. Rollback must respect
validity rules rather than make expired data acceptable.

CI authenticates through a scoped workload identity. Human publishers use their
own identity and provider-managed PIM where configured. No embedded credentials,
storage keys or SAS fallback are introduced. A renewal schedule is necessary;
one initial publication cannot keep expiring metadata current indefinitely.

### User initialization and environment discovery

Azure login identifies the user but does not identify a trusted organization
source. The user must supply an administrator-provided bootstrap reference.
Initialization verifies access and persists only minimal bootstrap/trust settings,
not the downloaded environment catalogue. Preserve existing configuration on
failure and keep unrelated local profiles intact.

Extend the metadata schema beyond route-only v1 before promising environment
onboarding. Define bounded, versioned data and explicit source precedence, alias
collision handling and schema compatibility. Metadata cannot contain executable
hooks, credentials or instructions to change the source trust anchor.

`list` and initialization may need temporary discovery transport for a private
source even outside a connected shell. Define its lifecycle and cleanup explicitly.
Each connection fetches and validates fresh metadata; discovery must not introduce
a persistent downloaded cache or reuse stale routes. Existing explicit local
fallback remains subject to ADR 0005 and independent provider authorization.

### Compatibility and deferred retirement

Do not silently reinterpret a publishing invocation of `heimdal init` as
consumer onboarding. The implementation must provide an explicit migration
path, updated help and compatibility tests before repurposing the name. Current
examples remain valid until that migration actually ships.

Retirement is deferred. Preserve ownership information now so a future workflow
can stop publication, assess consumers and retention, and produce a reviewed
IaC teardown plan. It must not delete an existing shared storage account or group
merely because Heimdal referenced it. Source removal does not revoke independent
permissions or terminate established sessions. No automatic teardown is added.

## Consequences

Bootstrap, publication and consumer access have separate identities, permission
requirements and side effects. Adopters can maintain infrastructure and metadata
through CI without requiring consumers to maintain a local environment catalogue.
This expands the roadmap; current route retrieval alone does not fulfill it.

ADR 0005 retains its validation, session lifetime and explicit fallback contract.
ADR 0003's local configuration sources remain supported; remote catalogue source
precedence needs a design before implementation. PIM automation remains future
work under ADR 0006, independent of provider-managed PIM in a deployment.

## Alternatives considered

- Reusing init for both uploading and onboarding was rejected because the same
  name would obscure write operations and consumer read operations.
- Direct provisioning with a separate Bivrost resource inventory was rejected
  in favor of the adopter's IaC ownership and review workflow.
- Downloading a durable local catalogue during onboarding was rejected because
  it reintroduces stale configuration and conflicts with session-only metadata.
- Implementing retirement in the first milestone was deferred; ownership and
  shared-resource boundaries are still required from the beginning.
