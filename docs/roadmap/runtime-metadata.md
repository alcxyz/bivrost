# Draft issue: add session runtime metadata

Allow a bootstrapped session to refresh deployment metadata from a private
Blob source without turning remote content into executable configuration.

## Acceptance criteria

- Fetching occurs only after bootstrap and uses the established local identity.
- The response has explicit size, schema, and immutable-revision validation
  before any field is used.
- A validated refresh is installed atomically and cannot retarget active
  backend data during a session.
- Remote data is never executed, evaluated, or treated as hooks or
  instructions.
- The default path keeps metadata session-only and has no persistent cache.
- Version information remains advisory until a separate enforcement decision is
  accepted.
- Source outage and invalid data have a documented safe fallback that keeps a
  basic connection available when its loaded configuration is sufficient.
