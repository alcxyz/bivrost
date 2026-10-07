# ADR 0016: Single command execution in a temporary session

- Status: Accepted
- Date: 2026-10-07

## Context

Scripts, CI jobs and coding agents need to run one command against an
environment without driving an interactive shell. Before this decision they
had to keep `bivrost connect` open in a terminal they own, publish the
session and pass the published kubeconfig to each client. Issue #1 asked for
the lifecycle contract to be settled before any syntax.

## Decision

Provide `bivrost run (-e NAME | -c PATH) [--acr [-n]] [--private-host HOST]
[--] COMMAND [ARGS...]`.

- **Session ownership.** `run` creates and owns one session exactly as
  `connect` does: same validation, proxy, Bastion tunnel, SSH forwards,
  Heimdal refresh, optional registry activation and isolated kubeconfig. The
  session ends when the command ends. Nesting inside an active session is
  rejected, as for `connect`.
- **Execution.** COMMAND is executed directly, never through a shell, so its
  arguments reach it verbatim. Shell syntax requires an explicit `sh -c` or
  `pwsh -Command`. The command name is resolved before any tunnel is opened.
- **Inheritance.** The command receives the session environment used by the
  interactive shell (`HTTPS_PROXY`, `NO_PROXY`, `KUBECONFIG`,
  `BIVROST_SESSION` and, with `--acr`, the Podman settings). It does not
  receive the session controller, so `switch`, `session publish` and
  `acr enable` are unavailable inside it. Without usable Kubernetes it still
  runs with an isolated empty kubeconfig; Bivrost never falls back to an
  ambient context.
- **Streams.** Stdin, stdout and stderr are passed through. Bivrost's own
  setup progress goes to stderr so stdout belongs to the command.
- **Exit status.** The command's status passes through. Following `env` and
  `timeout`, Bivrost uses 125 for its own failures (usage, configuration,
  setup, lost session, termination before the command finished), 126 when the
  command cannot be executed and 127 when it is not found. A command ended by
  a signal yields 128 plus the signal number.
- **Interrupts and cleanup.** Ctrl+C cancels setup. Once the command runs it
  shares Bivrost's process group and receives terminal interrupts directly;
  Bivrost does not forward a second copy, which could escalate tools such as
  Terraform. SIGTERM or SIGHUP sent to Bivrost is passed to the command as
  SIGTERM (terminated on Windows), which has 10 seconds to exit. If a tunnel,
  proxy or registry session fails while the command runs, the command is
  stopped the same way. All session resources are then cleaned up as for
  `connect`.

## Consequences

Each `run` pays the full session setup, which suits occasional commands and
CI. Frequent callers can still keep a `connect` session and use its
environment. Exit status 125 to 127 are ambiguous only for commands that use
those values themselves. Progress messages from shared setup steps now go to
stderr for every session command.

## Alternatives considered

- `connect -- COMMAND` was rejected because `connect` names an interactive
  session, and its help and banner would need two modes.
- Running COMMAND through the user's shell was rejected: quoting would vary
  across Bash, Zsh and PowerShell, and the shell's startup files would run.
- Reusing an existing session from another process was deferred. It needs a
  controller authorisation model for non-shell callers, which ADR 0012 limits
  to explicit kubeconfig publication.
- An MCP server for agents was deferred. A generic execution tool would bypass
  per-command approval in agent clients and keep cloud access open in a
  long-running process; agents already invoke CLIs with exit codes.
