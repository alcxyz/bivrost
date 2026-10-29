# ADR 0001: Session ownership and user configuration isolation

- Status: Accepted
- Date: 2026-09-20
- Updated: 2026-10-12
- History: [replaced text](archive/0001-session-ownership-and-user-configuration-history.md)

## Context

A local connection needs temporary tunnels, shell variables, credentials
configuration, and sometimes a kubeconfig. Sharing those objects with a
user's normal shell or mutating global client configuration makes cleanup and
recovery ambiguous.

## Decision

Each Bivrost invocation owns a session directory, its child processes, local
forwarding, and temporary kubeconfig. Session configuration is passed to the
child shell and is removed when the session exits. The user's normal
Kubernetes context and unrelated shell configuration remain unchanged.

Sessions are independent of each other, so several may run side by side, each
with its own ports, forwards and publication. A private registry of active
sessions, under `XDG_RUNTIME_DIR` (falling back to the XDG state area when that
is unset), records their non-credential metadata, and each owner holds an
operating-system lock on its record for the session's lifetime
([ADR 0018](0018-concurrent-sessions.md)).

User preferences and environment overrides live in the user's configuration
area under `bivrost`; `XDG_CONFIG_HOME` is honored when set. User-local files
are created with private permissions. A session may use the user's existing
Azure CLI login, but Bivrost does not turn that login into a shared store.

## Consequences

Sessions are easy to identify and clean up, and a failed session cannot
silently rewrite a user's normal kubeconfig. Users must start a new session
when they need a different target. Recovery metadata, when needed, belongs in
the user's XDG state area and is separate from credentials.

Global kubeconfig merging, permanent proxy variables, and shared engine
configuration are outside this boundary.

Session ownership describes lifecycle and configuration isolation, not isolation
from other local users. Ordinary loopback forwarding is unauthenticated and
requires a trusted host. The authenticated controller and publication gateway
do not secure alternate backing forwards. See [security boundaries](../security-boundaries.md).
