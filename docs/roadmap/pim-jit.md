# Draft issue: add short-lived PIM JIT lifecycle

Design an optional, bounded PIM flow with browser MFA and provider approval,
ownership-aware cleanup, and recoverable expiry behavior.

## Acceptance criteria

- Activation requests use an exact short scope, reason, and duration shown to
  the user, follow browser MFA and provider approval policy, and require
  deliberate renewal.
- The client records activation IDs created by the current session and never
  deactivates preexisting activations.
- Cleanup acts only on activations whose ownership is proven by those records.
- Cleanup is best-effort and does not promise immediate revocation.
- Concurrent local references and cross-device ambiguity never trigger guessed
  cleanup.
- Expiry is a supported fallback; recovery reconciles expired and still-present
  records with the provider before retrying cleanup.
- Recovery state stores IDs and non-secret metadata only; it never stores
  tokens or credential material.
- The only persistent recovery location is non-secret XDG state, with private
  file permissions and an explicit cleanup policy.
