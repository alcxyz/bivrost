# ADR 0006: Short-lived PIM activation lifecycle

- Status: Accepted future direction
- Date: 2026-09-20
- Scope: Future work; no implementation is claimed here.

## Context

Some environments may require just-in-time privileged access. Automation must
not clean up another person's activation or treat an expiring authorization
as a permanent local fact.

## Decision

Future PIM support will use the provider's normal browser MFA and approval
policies. It will request an exact short scope, reason, and duration, show
those choices to the user, and require deliberate renewal rather than silently
extending access. It will record only activations created by the current
Bivrost session. Cleanup is best-effort and limited to those own-created
records; it is not an immediate-revocation guarantee.

The client will never deactivate a preexisting activation. Concurrent local
references and cross-device ownership are ambiguous; when ownership cannot be
proven, the client will not guess or clean up. Expiry is the fallback
lifecycle and must be handled as a normal, recoverable outcome. Recovery must
reconcile expired and still-present records with the provider before deciding
whether another cleanup attempt is appropriate.

Recovery records may retain activation IDs and other non-secret references,
but never access tokens or credential material. The metadata-cache exception is a restricted recovery record under the user's
XDG state directory, containing ownership, identity, scope and expiry references.
Delete completed or expired records after reconciliation.

## Consequences

Users receive a bounded privilege window and predictable cleanup ownership.
Some ambiguous or interrupted cases require the user to recover them through
the provider's normal interface. A future implementation needs explicit
record identity, provider-policy handling, expiry reconciliation, and
deliberate renewal before it can be enabled.

Before implementation, choose the applicable role or group PIM APIs and
permissions, resolve pre-connection activation ordering, and define ownership
across concurrent sessions and devices.

## Alternatives considered

- Silent long-duration activation or automatic renewal was rejected because it
  exceeds the user's deliberate scope and duration.
- Deactivating every matching activation was rejected because it could revoke
  access created before the current session or on another device.
- Treating cleanup as immediate revocation was rejected because provider
  policy, approval, and expiry can make cleanup asynchronous or unavailable.
