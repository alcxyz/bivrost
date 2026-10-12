# ADR 0018: Concurrent sessions

- Status: Accepted
- Date: 2026-10-12

## Context

A second `bivrost connect` failed while another session was running, even to
a different environment, because every session reserved the profile's fixed
`socks_port` and `proxy_port`. Catalogues usually share those ports across
environments. Cross-environment work therefore had to open and close sessions
one after another, and tools holding a published kubeconfig lost it at each
switch.

Everything else was already owned by one session (ADR 0001): the session
directory, temporary kubeconfig, Kubernetes API forward, Bastion tunnel port,
controller and each publication's path and gateway (ADR 0012). The
`BIVROST_SESSION` check only prevents nesting inside one shell.

## Decision

**Ports per session by default.** A profile that omits `socks_port` or
`proxy_port`, or sets it to 0, gets a free loopback port for each session,
chosen at startup. The shell already receives the proxy through
`HTTPS_PROXY`, the session status reports the chosen ports, and publications
use their own gateways, so nothing needs a fixed number. A configured port
stays supported for clients that need a stable proxy address; two sessions
configured with the same port still conflict, and the error says to omit it.
The standalone `acr proxy`, `acr login` and `acr doctor` are separate
processes that must agree on an address, so they use the configured ports or
18080 and 18081.

**A registry of active sessions.** Each `connect` and `run` session writes a
record in the private `bivrost/sessions` directory beside the publication
directory (under `XDG_RUNTIME_DIR`, or the same fallback). A record holds the
environment, command, process ID, start time, limit, ports and whether the
session has registry access; never credentials. The owner holds an exclusive
operating-system lock on its record (`flock` on Unix, `LockFileEx` on
Windows) for the session's lifetime. The system releases the lock when the
process ends, however it ends, so a record whose lock can be taken is stale
and the next scan removes it. A separate `registry.lock` serialises scans and
changes, so a record is never seen before its owner locks it and two sessions
cannot both take the last place. `bivrost session list` shows the records,
and `bivrost session clean` removes stale ones as well as stale publications.

**An optional session limit.** A profile may set `max_sessions` (1 to 64). A
new session starts only while the number of active sessions is below the
lowest limit among its own profile and the active sessions, so a profile with
`max_sessions: 1` only runs alone and nothing starts beside it. When a profile
does not set it, its limit is the number of environments `bivrost list`
shows (catalogue environments and local profiles), and at least 1: a
catalogue with sandbox, staging, production and infra allows four sessions.
Sessions to the same environment are not refused; each is independent, with
its own ports and publication.

**Registry access stays with one session.** Only one session at a time may
have Podman registry access, whether from `--acr` or `acr enable`; the
registry record carries the claim, and a second session is refused with a
message naming the holder. The Podman Machine is shared: the bridge
reverse-forwards the session's proxy port into the machine on the same
number, and `acr proxy` identifies its proxy by port. The restriction can be
lifted once the in-machine port is chosen per session.

Unchanged: nesting inside a session is still rejected. `session publish`,
`path` and `unpublish` act on the shell's own session through its controller,
so publications of different sessions coexist. A managed switch (ADR 0010)
ends the old session, releasing its record, before the new one registers; if
another terminal took the last place meanwhile, the new connection fails and
the user returns to the original terminal, as for any failed reconnect.

## Consequences

Users can keep sessions to several environments open in separate terminals
and publish each of them. Each session pays its own Bastion tunnel and SSH
forwards. Downstream catalogues that set the same fixed ports for every
environment must drop them, or give each environment its own, before their
users can run sessions side by side. Profiles given with `--config` are not
counted, so users without a catalogue get one session unless their profiles
set `max_sessions`. The standalone `acr proxy`, `acr login` and `acr doctor`
no longer meet a session on the old default ports; a profile that needs that
combination sets fixed ports.

Same-user processes can read the registry and see which environments are
connected and on which loopback ports; ADR 0001's trusted-host boundary
already covers them. Parallel sessions share the local Azure CLI login and its
token cache, which Azure CLI guards with its own file lock, and the SSH
`known_hosts` file, where each VM has its own host key alias. Live use of
parallel token refreshes has not yet been verified.

## Alternatives considered

- A configurable port range instead of free ports: needs a new setting and
  still conflicts with other programs, while free ports need neither.
- Process IDs or loopback probes to detect stale records: process IDs are
  reused, and `run` sessions have no controller to probe. Operating-system
  locks are released exactly when the owner ends.
- A fixed default of one session per environment: simpler, but refuses useful
  parallel work in one environment and ignores a profile's need to run alone.
- Several Podman registry sessions: the in-machine proxy port and the
  standalone proxy's identity would have to become per session first.
