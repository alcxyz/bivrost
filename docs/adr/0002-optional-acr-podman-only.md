# ADR 0002: Optional ACR access through Podman only

- Status: Accepted
- Date: 2026-09-20

## Context

Some sessions need private container images while others only need shell and
Kubernetes access. Container engine state is especially easy to alter for
other tools or users on the same computer.

## Decision

Registry access is opt-in with `connect --acr` or `acr enable`. Bivrost uses
Podman only: native local Podman on Linux or a local Podman Machine on other
platforms. It creates a session-owned Podman service configuration and a
loopback proxy for the session, then removes them on exit. The selected
Podman engine and Machine must be local to the user's computer.

Docker, arbitrary remote Podman endpoints, and permanent engine or proxy
reconfiguration are outside the supported interface. ACR access is optional;
Podman is not required for a shell-only connection.

## Consequences

The default connection stays small and does not depend on a container engine.
Registry access has explicit setup and cleanup, and cached image layers remain
owned by the user's Podman installation. Users must provide and maintain a
working local Podman engine or Machine when they opt in.

Loopback proxying assumes a trusted local host; it does not isolate network
access between OS users. Podman's normal registry auth storage may retain login
credentials after disconnect. Cleanup must not log out a shared login; a future
session-only auth store needs separate lifecycle and platform validation.
See [security boundaries](../security-boundaries.md).
