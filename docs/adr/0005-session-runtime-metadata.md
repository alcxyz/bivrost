# ADR 0005: Heimdal session-only runtime metadata

- Status: Accepted future direction
- Date: 2026-09-20
- Scope: Future work; no implementation is claimed here.

## Context

Environment metadata can change independently of a binary release. Refreshing
it after a connection is bootstrapped could reduce stale routing data, but an
unvalidated remote document must never become executable configuration.

## Decision

Heimdal names the metadata acquisition component of Bivrost, not a separate
server. Future sessions may fetch runtime metadata from a private Blob source after
bootstrap, using the already established local identity. The response must be
bounded by a size limit and validated as data against an explicit schema and
an immutable revision before use. A refresh is parsed and validated off to the
side, then installed atomically; it cannot retarget active backend data during
a session. It must never be interpreted as code, hooks, or an instruction
stream.

The default behavior has no persistent metadata cache. Data may live only for
the session unless a later decision explicitly changes that rule. Metadata
version information is advisory for now; whether a version can enforce a
minimum client release remains deferred. If metadata is unavailable or fails
validation, the basic connection remains available when its already loaded
configuration is sufficient.

Bootstrap configuration must already contain the connection and metadata
locator, exact route and trust information needed to avoid a circular dependency.
Publication should separate reader and publisher permissions and provide
immutable revisions, a current pointer and rollback. Subprocesses may receive
restricted temporary files with session cleanup; users can still copy metadata
they are authorized to read. Client version advice is not an access-control
boundary. A server component and server-side version enforcement are deferred.

## Connection and fallback contract

Each new connection fetches metadata again, reusing a valid local Azure login.
Interactive browser authentication happens when needed, not on every fetch.
The first provider is Azure Blob Storage using Entra data-plane authorization;
login alone does not grant blob read access. Storage keys and embedded SAS
credentials are not the fallback. Amazon S3 is a separate provider and is not
part of this initial decision.

The private downstream bootstrap supplies the metadata locator and trust
configuration. The public binary contains no deployment-specific endpoints.
When Blob access requires the tunnel, establish the minimal connection first.
No remote metadata may be required to reach its own source.

On a source outage, explicitly configured local metadata may support the
requested operation. Report its source and the failed refresh. Authorization
denials and invalid or untrusted responses must remain visible; never silently
substitute stale downloaded metadata. An already bootstrapped basic connection
can remain usable, but features requiring missing valid metadata are unavailable.
Local configuration grants no resource permissions and cannot bypass provider
authorization. A refresh cannot change active targets underneath the user.

The initial implementation retains existing local configuration. Optional
Tesseract encrypted local configuration follows [ADR 0008](0008-tesseract-local-configuration.md).
It does not create an automatic persistent Heimdal cache. Session cleanup is
best-effort on abnormal termination and cannot prevent an authorized user from
copying data. Metadata expiry does not revoke provider permissions.

## Consequences

Deployment permissions separate metadata consumers, metadata publishers, and
Terraform-state maintainers. State-maintenance access must not implicitly grant
metadata publication rights. Provider scopes and conditions enforce that split;
PIM controls its activation window. A shared metadata container is appropriate
when its consumers share a read boundary. See the generic
[Azure adoption example](../heimdal-adoption.md) for a deployment illustration.

Runtime refresh can be added without turning the public catalogue into a
secret or deployment-data store. A temporary source outage must have a clear
fallback, and schema, size, immutable revision, atomic refresh, and version
behavior require review before any implementation is accepted.

## Alternatives considered

- Executing remote hooks or treating metadata as a script was rejected because
  a data refresh must not become a code-loading path.
- Retargeting an active backend from a refreshed document was rejected because
  it makes an established session change underneath its user.
- A persistent default cache was rejected because it creates stale deployment
  state and a new recovery surface.
