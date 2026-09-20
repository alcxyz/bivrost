# ADR 0005: Session-only runtime metadata

- Status: Accepted future direction
- Date: 2026-09-20
- Scope: Future work; no implementation is claimed here.

## Context

Environment metadata can change independently of a binary release. Refreshing
it after a connection is bootstrapped could reduce stale routing data, but an
unvalidated remote document must never become executable configuration.

## Decision

Future sessions may fetch runtime metadata from a private Blob source after
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

## Consequences

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
