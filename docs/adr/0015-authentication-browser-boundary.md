# ADR 0015: Authentication browser boundary

- Status: Accepted; live Microsoft Entra and Windows WAM QA pending
- Date: 2026-10-01

## Context

Bivrost delegates Microsoft Entra authentication to Azure CLI. Azure CLI 2.89
requests the VM SSH certificate silently, but when that request fails with an
authentication error (multi-factor or Conditional Access step-up, an expired or
revoked refresh token, or missing consent) it falls back to an interactive
authorization-code flow for the SSH certificate scope only. Other token requests
report the error instead. MSAL opens that flow with Python's `webbrowser`
module, which honors `BROWSER`; without it, MSAL on Linux prefers
`/usr/bin/microsoft-edge` and otherwise uses the operating system default.

Bivrost discarded Azure CLI output during this step. A connect could therefore
open the wrong browser or profile and then stall at "Preparing a short-lived
Entra SSH certificate" until its timeout. Connectivity failures raise network
errors and do not open a browser.

Developers often keep work identities in a dedicated browser profile, sometimes
in a browser other than the system default.

## Decision

Bivrost owns the browser that Azure CLI child processes can open. Each Azure CLI
command receives `BROWSER` set to the Bivrost executable and a private mode
variable. Python runs `BROWSER` as one executable with the sign-in address as its
only argument, without a shell. When started this way, Bivrost handles the
request and always exits successfully, because a failure would let `webbrowser`
try the system default browser.

- Connection, diagnostic, discovery and Heimdal commands use refuse mode.
  Bivrost opens nothing. During SSH certificate setup it records the request in
  the session directory, stops the Azure CLI process group and reports
  that interactive sign-in is required. It also gives the Microsoft Entra reason
  when a known error code appears in the captured warnings, and directs the user
  to `bivrost login --ssh`. Azure CLI output is matched locally and never
  printed or recorded. Diagnostics record the outcome `interaction_required`.
- `bivrost login` is the only interactive path. Without configuration, Azure CLI
  keeps its normal browser selection. When the user setting
  `authentication_browser` is present, launch mode starts its `executable` with
  its `arguments`; an argument exactly `{url}` receives the sign-in address,
  which is otherwise appended. The browser is detached and started directly,
  never through a shell. Batch files are rejected because Windows would run them
  through `cmd.exe`.
- If the configured browser cannot be started, Bivrost prints the sign-in
  address on the terminal for manual use and suggests `--device-code`. It does
  not fall back to the system default browser.
- `bivrost login --device-code` uses Azure CLI device-code sign-in.
  `--ssh` requests the Azure Linux VM sign-in application scope, so a later
  silent SSH certificate request can satisfy that application's interactive
  requirements.

The setting lives in the user's Bivrost `settings.json` (ADR 0001). Bivrost does
not change operating system browser defaults, Azure CLI configuration or browser
profiles, and catalogues cannot select a browser.

The new `authbrowser` package depends only on `config`; `azure`, `session` and
the executable use it. The executable now dispatches browser requests before
Podman basename dispatch, extending ADR 0011.

## Consequences

Connect and doctor fail quickly with an actionable message instead of opening a
browser. Users satisfy new sign-in requirements explicitly, in the browser
profile they chose.

On Windows, Azure CLI uses the WAM broker by default. WAM shows its own account
dialog rather than a browser, including for SSH certificates, and `BROWSER`
cannot intercept it. Disabling the broker would also change silent token
acquisition, so Bivrost leaves it unchanged. Those prompts remain bounded by
the existing setup timeout.

Whether `az login --scope` refreshes the conditions that the silent SSH
certificate request needs depends on tenant policy. It needs live verification,
as does device-code sign-in under device-compliance policies, which device code
cannot satisfy.

The guard relies on Azure CLI and MSAL using Python's `webbrowser`. A future
Azure CLI release that changes this would bypass refuse mode. Detection would
then fall back to the setup timeout.

## Alternatives considered

- Passing a shell command template through `BROWSER` with `%s` would make the
  sign-in address part of command text and depend on `webbrowser` parsing.
- Setting `BROWSER` to a non-existent program makes `webbrowser` fall through to
  the system default browser.
- Disabling the WAM broker or editing Azure CLI configuration would change
  global client behavior that Bivrost does not own.
- Calling Microsoft Entra directly for SSH certificates would duplicate Azure
  CLI token caching and Conditional Access handling.
