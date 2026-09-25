# Security boundaries

## Trusted local host

Run Bivrost on a trusted personal workstation or an equivalently isolated
environment. It does **not** currently isolate session network access from
other local OS users or untrusted processes on the same host. Do not use it as
a shared jump-host service or on a shared CI runner with untrusted workloads.

The ordinary HTTP proxy and its backing OpenSSH SOCKS listener bind to loopback,
but do not authenticate callers. Loopback prevents remote network connections;
it is not a per-user access control. A local caller can reach the SOCKS listener
directly, bypassing the HTTP router's host selection and port restrictions,
subject to the management server's SSH forwarding policy. This provides network
reachability, not the owner's Azure or Kubernetes credentials. Services that
trust network location alone remain exposed to that reachability.

The session controller and the published Kubernetes gateway each require a
random capability token. Those tokens protect only those two interfaces. They
do not protect the backing SOCKS listener, the ordinary HTTP proxy, or the
local Kubernetes TCP forward. Adding HTTP authentication alone would not
establish cross-user isolation. Server-side forwarding limits and resource
authorization remain necessary deployment controls.

Supporting untrusted co-resident users requires a transport redesign protecting
all backing forwards as well as client-facing proxies, with cross-user tests
on each supported OS. Windows ACL guarantees also require live validation;
Unix permission bits are not evidence of Windows access restrictions.
Transport isolation is tracked in [issue #28](https://github.com/alcxyz/bivrost/issues/28).

## Credentials and cleanup

Normal session exit closes owned tunnels and removes owned configuration.
Abrupt process or host termination can leave files behind. Bivrost does not
revoke the user's Azure CLI login, erase image layers, or log out Podman's
registry credentials. Registry login uses Podman's normal credential storage
and may outlive the session. Concurrent sessions and ordinary Podman commands
may share that login; blindly logging out on exit would disrupt them.

If session-only registry credentials are required, they need a separate owned
auth store and verified native/Podman Machine support before cleanup can make
that guarantee. Processes running as the same OS user are inside that user's
credential trust boundary; Bivrost does not protect against them.

## Metadata and authorization

Heimdal publication is trusted configuration administration. Content-addressed
revisions and digest validation detect mismatched bytes, but do not prevent an
authorized publisher from choosing a different valid revision or deleting one.
They do not authenticate the publisher independently of the configured storage
source. Storage retention/versioning is a separate deployment choice.

Private-host metadata selects a network route, not a replacement TLS identity
or token audience. Clients must preserve TLS verification. Opt-in startup
retrieval validates source, schema, digest and expiry before installing routes;
it does not refresh an active shell. Metadata must never become executable hooks
or grant provider permissions. See the [adoption example](heimdal-adoption.md)
for deployment permission boundaries.
