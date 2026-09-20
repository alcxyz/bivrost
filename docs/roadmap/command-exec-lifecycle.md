# Draft issue: define the plain command-execution lifecycle

Describe how a local command runs inside a Bivrost session, including startup,
inheritance of session variables, exit status, interruption, and cleanup.
The command syntax is intentionally unsettled until the lifecycle contract is
reviewed.

## Acceptance criteria

- The proposal defines session ownership, child-process inheritance, exit
  status, interrupt handling, and cleanup without committing to a CLI syntax.
- A command can use the temporary kubeconfig and session-scoped Podman setup
  when those capabilities are available.
- A missing Kubernetes target does not force a failure for a command that only
  needs the shell or Terraform baseline.
- The proposal never falls back to an ambient Kubernetes context.
- Candidate syntax and compatibility costs are recorded before implementation.
