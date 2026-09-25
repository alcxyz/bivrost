# ADR 0012: Opt-in Kubernetes session publication

- Status: Accepted
- Date: 2026-09-25

## Context

Desktop clients and independent terminals do not inherit a connected shell's
KUBECONFIG. Users need to retain normal local clusters while explicitly making
a connected session available to additional tools.

## Decision

Provide `bivrost session publish`, `unpublish`, and `path` inside an authenticated
session. Publication is disabled by default. `publish` and `path` print only the
published kubeconfig path. The original shell remains isolated.

Publication files live in the private `bivrost/published` directory under
XDG_RUNTIME_DIR, falling back to XDG_STATE_HOME (or ~/.local/state). Each file
has a unique path and context name. It contains the cluster CA and exec-based
local identity configuration, never a copied Azure token or private key.
The kubelogin executable and its CLI search path are captured so desktop clients
can authenticate without inheriting the session shell's PATH.

A publication owns a loopback HTTP CONNECT gateway with a random capability.
It accepts only the publication's synthetic `.invalid` Kubernetes hostname and
forwards it to the current session's local API tunnel. TLS remains end-to-end,
with the original cluster TLS identity. No general proxy or new cloud
permissions are exposed. Unpublish closes existing gateway connections and
removes the file; it does not disconnect the original shell.

The controller owns publishing and cleanup. Disconnect withdraws publication.
A managed switch withdraws the old target before connecting the new one and
carries the opt-in to a new publication when Kubernetes is available. Existing
clients are never silently retargeted; they must select the new context/path.
A failed reconnect leaves no active publication.

After abnormal process death a descriptor may remain, but its gateway is gone
and its unique capability cannot authenticate to a replacement gateway. Publish
and `bivrost session clean` remove recognised stale descriptors when the owner
is conclusively unavailable. Ambiguous failures leave files untouched.

GUI discovery configuration and normal kubeconfig sources belong to the user's
configuration tooling. Bivrost does not edit GUI state, the global kubeconfig,
or another terminal's environment. Normal contexts and published contexts can
be presented together by external clients.

## Alternatives

- Permanently merging sessions into ~/.kube/config makes ownership and cleanup
  ambiguous and risks changing normal context selection.
- Only sharing a symlink does not revoke connections held by clients that
  already loaded it, and leaves stale loopback port reuse ambiguous.
- A desktop-specific command would exclude terminal clients such as k9s.
- Reusing one path across switches could silently retarget background clients.

## Limits

This is a same-user convenience boundary, not isolation from a malicious local
user. Unpublishing does not revoke cloud-issued credentials. Applications may
cache displayed contexts and need a refresh. Windows filesystem permissions,
GUI discovery, and live client behavior still require platform QA.
