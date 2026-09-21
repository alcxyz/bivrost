# ADR 0010: Managed session switching

- Status: Accepted
- Date: 2026-09-21

## Context

Changing environment currently requires exiting the session and invoking connect
again. An in-place target change would risk sending existing commands to another
environment with stale proxy, Kubernetes or registry state.

## Decision

Provide `bivrost switch --env NAME` (or `--config PATH`) inside supported Bash,
Zsh and PowerShell sessions. Treat switching as ending one session and creating
another under the original connection owner. Existing `connect` nesting remains
rejected. Other shells use exit followed by connect.

The shell integration submits a request through the existing authenticated local
controller. The owner loads and validates the target before accepting the
request. A switch occurs only after the requesting shell exits with the reserved
switch status; merely submitting a request cannot retarget running commands.
The owner completes old-session cleanup before starting new tunnels, kubeconfig,
registry settings and shell. No configuration is evaluated as shell code.

Shell integration refuses switching when the shell reports active or stopped
jobs; PowerShell also requires removing retained job records. Detached processes are not migrated or promised to be tracked; users must
finish their work before switching. Shell-local state and directory changes are
not carried into the fresh shell. Ordinary exit remains a disconnect.

Registry activation is opt-in for each target using `--acr`; neither activation
nor credentials are inherited from the old session. Existing provider access and
PIM requirements still apply. This feature does not activate privileges.

Validation failure leaves the current session intact. A connection failure after
old-session cleanup returns to the original terminal and reports the failure;
there is no automatic rollback that might imply continued access to the old
target. Invalid or unauthenticated control requests cannot trigger a switch.

## Consequences

ADR 0001's one-target-per-session boundary remains intact. Switching is a
convenience for a managed reconnect, not a persistent shell whose infrastructure
changes underneath it. The reserved exit status and control request must agree,
and cancellation, cleanup and unsupported shell behavior require tests.

## Alternatives considered

- In-place environment mutation was rejected because existing subprocesses retain
  old inherited settings and may still be operating on the prior target.
- Automatically retaining registry activation was rejected in favor of explicit
  per-target opt-in.
- Nested connections complicate port ownership and cleanup and remain rejected.
- Exit followed by connect remains the portable fallback.
