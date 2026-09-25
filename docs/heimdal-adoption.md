# Example: adopting Heimdal with Azure and PIM

This is a generic deployment example, not an installed configuration. Adopters
own their storage, identity groups, role assignments, network paths, and CI.
Bivrost does not create those resources or grant access.

Use a trusted workstation or isolated CI execution environment. Ordinary
loopback tunnels do not isolate other local users or workloads; see
[security boundaries](security-boundaries.md).

**Implementation status:** the development branch supports `heimdal init` for
first-time publication and opt-in retrieval during connect/reconnect. Ongoing
publication and rollback, and Bivrost-managed PIM activation are planned features. Activate
PIM through the provider's own interface today; Bivrost has no `pim` command.

## Storage and ownership

An example organization has a platform team and application teams Alpha and
Beta. It keeps metadata separate from Terraform state:

```text
metadata storage account
  heimdal/
    environments/development/current.json
    environments/development/revisions/<digest>.json
    environments/production/current.json
    environments/production/revisions/<digest>.json

state storage account
  tfstate-alpha/...
  tfstate-beta/...
```

One shared Heimdal container is enough when its readers may see all of its
metadata. Environment prefixes organize documents; they are not authorization
boundaries. If production metadata itself needs different read permissions,
use separate containers or deliberately designed Azure ABAC conditions.

Separate accounts make ownership clearer but are not mandatory. A single
account with separate containers also works with container-scoped assignments.
Neither arrangement protects against a broader inherited data-access grant
that covers both metadata and state.

## Permission example

All roles below are Azure **Storage Blob Data** roles, scoped to the named
container. The table describes independent grants, not a hierarchy of groups.

| Principal | Role | Scope | Activation |
| --- | --- | --- | --- |
| Metadata consumers | Reader | `heimdal` | Standing membership, or PIM gating the entire container |
| Metadata publishing CI identity | Contributor | `heimdal` | Workload identity; no interactive PIM |
| Platform metadata maintainers | Contributor | `heimdal` | Eligible PIM group membership |
| Alpha state maintainers | Contributor | `tfstate-alpha` | Eligible PIM group membership |
| Beta state maintainers | Contributor | `tfstate-beta` | Eligible PIM group membership |

Platform engineers who need state maintenance receive separate state grants.
App team membership in a state-maintenance group never implies metadata-write
access. Contributor also permits deletion: publisher access is a trusted role,
not merely permission to run one Bivrost command.

PIM limits the time during which membership is active. Scope and conditions
limit the resources it authorizes. Azure grants are additive: Reader on
Heimdal does not cancel Contributor inherited from an account, resource group,
or subscription. Review other group memberships and inherited grants before
relying on this table. See [Azure RBAC](https://learn.microsoft.com/en-us/azure/role-based-access-control/overview)
and [PIM for Groups](https://learn.microsoft.com/en-us/entra/id-governance/privileged-identity-management/concept-pim-for-groups).

If non-production readers must not read production metadata before activation,
do not give them standing Reader on this shared container. Use the separate
read boundary described above. PIM governs eligible membership, not every route
into a group: review active assignments, nested membership, and who can manage
membership or ownership. Group administration is itself privileged. Token and
authorization caching mean deactivation is not an immediate revocation promise.

### Existing shared Terraform-state containers

Do not replace existing team restrictions with an unconditional Contributor
grant on a shared state container. If teams use blob prefixes in one container,
preserve and validate their Azure ABAC conditions, including the relevant read,
write, delete and listing operations. A different unconditional grant can
bypass the intended restriction. Container-per-team is the simpler example
here, not a requirement to migrate an existing deployment.

Consult [Blob role conditions](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-auth-abac)
and their [security considerations](https://learn.microsoft.com/en-us/azure/storage/blobs/storage-auth-abac-security)
before designing a shared-container policy. This guide deliberately does not
provide a partial path condition that could be mistaken for complete isolation.

## Adoption sequence

1. **Provision through your infrastructure workflow.** Create private storage
   containers, groups and narrowly scoped role assignments. Configure the
   required network routes and PIM eligibility, MFA, approval, justification,
   and duration policies. Keep role administration separate from metadata
   publication. Use Entra data-plane authorization rather than distributing
   account keys or SAS credentials.
   For this Entra-only design, disable Shared Key authorization with
   `allowSharedKeyAccess=false`, disable anonymous blob access, and audit broader
   permissions that could re-enable either or change role assignments. Assess
   existing account-key consumers before changing an existing account. Merely
   choosing login authentication in Bivrost does not disable other access paths.
   See [prevent Shared Key authorization](https://learn.microsoft.com/en-us/azure/storage/common/shared-key-authorization-prevent).
2. **Prepare minimal bootstrap settings.** Supply the connection information,
   metadata locator and route needed to reach the source. If reading metadata
   requires production PIM, users must be able to discover and activate that
   eligibility before fetching it; do not hide the activation prerequisites
   exclusively inside the gated document.
3. **Initialize the source.** The `heimdal init` command, still under
   development, uses the signed-in Azure CLI identity, which can be a human or
   a CI workload identity. The container must already exist, and the identity
   must already hold write permission on it.
   Authenticate CI through the organization's federated workload identity flow;
   never store publishing credentials in metadata.
   Bind federation to the intended issuer, audience and protected publishing
   branch or deployment environment. Untrusted pull requests must not receive
   publishing credentials. Anyone who can change or execute the privileged
   publishing workflow is effectively a publisher; protect that workflow and
   its dependencies accordingly.
4. **Consume metadata per session (development).** Connect using bootstrap settings,
   retrieve and validate the metadata, and keep it only for the session.
   Reading metadata does not grant access to the resources it describes.
   Explicit local fallback remains subject to those resources' authorization.
   A configured source failure stops setup unless `allow_local_fallback` is
   explicitly enabled. Even then, permission and validation failures remain
   visible, and only existing local routes are used. See the
   [connection profile example](../README.md#fetch-metadata-when-connecting-development).
5. **Maintain through reviewed CI (planned publication lifecycle).** Keep the
   authoritative deployment metadata in the adopter's own repository. Validate
   changes before publishing an immutable revision and updating its pointer.
   Current initialization is create-only and is not a repeatable update command.
   Revision names are content digests, not Azure immutability policies. The
   startup metadata reader verifies the digest before applying routes.
   A publisher can delete revisions or select different content through a new
   pointer. Choose retention/versioning and audit logging appropriate to your
   recovery needs; neither a digest nor versioning prevents a malicious publisher
   from publishing a new valid document.
6. **Allow deliberate emergency maintenance.** Platform maintainers activate
   their metadata-write PIM group for a short period. Record the revision and
   reason, reconcile the change back into the source repository before the next
   CI publication, then deactivate. State maintenance uses its separate group.

For example, the initialization interface under development is:

```sh
bivrost heimdal init -e development \
  --subscription SUBSCRIPTION --account ACCOUNT \
  --container heimdal --private-host state.example.net
```

The private host here is metadata being published. It does not configure the
network route used to upload that metadata. Private-source connectivity must
already be provided by the current session or network.

The initial metadata format has a validity window. A real rollout needs a
refresh/publication schedule before it expires; initialization alone is not
an ongoing delivery service. See the [Heimdal ADR](adr/0005-session-runtime-metadata.md).

## Acceptance checks for the adopter

Use disposable fixtures, never production state, for write/delete tests:

- A normal consumer can read permitted metadata but cannot modify it.
- Shared Key and anonymous access are disabled; broader administrators and
  privileged publishing workflows are included in the trust review.
- An activated Alpha maintainer can operate on Alpha's state fixtures but
  cannot read or modify Beta's fixtures or publish Heimdal metadata.
- CI can publish metadata but has no state permission through this assignment.
- A platform maintainer can publish only after the relevant activation, unless
  another deliberately granted role already permits it.
- After deactivation/expiry, recheck access while accounting for Azure
  propagation and other grants. Do not promise instant revocation.
- An unavailable metadata source produces a clear failure/fallback indication;
  local settings do not bypass Azure authorization.

Future session-owned PIM cleanup is best-effort, touches only activations the
session created, and must not deactivate pre-existing or ambiguously shared
activations. Provider expiry remains the fallback. See [ADR 0006](adr/0006-pim-jit-lifecycle.md).
