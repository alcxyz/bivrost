# Draft issue: complete Windows native Podman Machine QA

Run live QA for the Windows path using a local Podman Machine and the same
session ownership rules as other supported platforms.

## Acceptance criteria

- A clean Windows workstation can discover and use one local running Podman
  Machine without Docker or a remote engine.
- `connect --acr`, `acr enable`, and `doctor` exercise session-scoped Podman
  configuration and proxy cleanup.
- Azure browser MFA and the user's local Azure CLI identity work without a
  shared credential flow.
- Closing the shell, interrupting setup, and handling a failed Machine start
  leave no Bivrost-owned session processes or temporary files.
- Results, supported Windows versions, and known limitations are recorded
  before Windows support is advertised.
