# ADR 0011: Go package boundaries and command entry point

- Status: Accepted
- Date: 2026-09-21

## Context

The initial executable kept command parsing, configuration, proxy transport,
shell integration and lifecycle coordination in the root package. That layout
made ownership hard to see and allowed unrelated implementation details to
depend on one another.

## Decision

Use a thin executable under `cmd/bivrost`. It owns process exit handling,
Podman basename dispatch and the `main.version` linker variable. Keep runtime
implementation in internal packages:

- `cli`: argument parsing and help; it does not start connections.
- `config`: profiles, catalogue loading, user settings and validation.
- `diagnostics`: bounded local logs with fixed, allowlisted events.
- `proxy`: HTTPS CONNECT routing and network target validation.
- `shell`: generated startup hooks, prompt rendering and child environment helpers.
- `podman`: executable wrapper construction, execution and cleanup.
- `azure`: Azure CLI launching and operation argument construction.
- `session`: command dispatch, connection ownership, activation, switching,
  session-aware diagnostics and coordinated cleanup.

The dependency direction is from the executable to session orchestration, and
from orchestration to the narrower packages. None of those packages imports
`session`. CLI parsing uses configuration validation; configuration reuses proxy
target validation; proxy transport emits fixed diagnostic events. These concrete
dependencies do not introduce a provider framework.

Podman Machine forwarding, kubeconfig preparation and doctor probes remain in
`session` while they share session state and teardown requirements. Further
extractions need cohesive APIs, rather than one directory for each command.
Unit tests live beside their implementation; tests covering multiple packages
remain beside the session orchestration they exercise.

## Compatibility and consequences

Command syntax, profile JSON, shell/controller protocols, authentication and
cleanup behavior remain unchanged. Build and release commands target
`./cmd/bivrost`; `-X main.version=...` remains supported. Downstream packaging
must select the new entry point when adopting this source layout.

The root contains project metadata and documentation, not Go implementation.
Package visibility makes dependencies explicit without creating a public Go API.

## Alternatives considered

- Moving everything into one internal package would tidy the root while leaving
  the original coupling intact.
- Splitting every lifecycle operation immediately would require broad exports or
  artificial interfaces around shared session state.
- A universal cloud-provider abstraction remains deferred under ADR 0009 until
  concrete additional implementations establish its requirements.
