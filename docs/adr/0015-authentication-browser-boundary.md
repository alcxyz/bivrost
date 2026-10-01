# ADR 0015: Authentication browser selection and prompts

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

This in-line browser sign-in is how users satisfy multi-factor authentication
during connect, and organizations are retiring device-code sign-in. The problems
were that the page could open in the wrong browser or profile, and Bivrost
discarded Azure CLI output, so connect appeared to stall at "Preparing a
short-lived Entra SSH certificate" until its two-minute timeout. Connectivity
failures raise network errors and do not open a browser.

## Decision

Bivrost decides which browser Azure CLI child processes can open, without
removing interactive sign-in. Each Azure CLI command receives `BROWSER` set to
the Bivrost executable and a private mode variable. Python runs `BROWSER` as one
executable with the sign-in address as its only argument, without a shell.
Exit status 0 tells `webbrowser` that a browser opened; any other status makes
it try the next `BROWSER` entry and then the system default.

- **SSH certificate setup** (connect, switch, ssh) uses interactive mode by
  default. Bivrost records the request in the session directory and prints that
  Azure needs interactive sign-in, naming the Microsoft Entra reason when a known
  error code appears in the captured warnings. Bivrost opens the configured
  authentication browser, or declines so `webbrowser` continues with the user's
  previous `BROWSER` value or the system default. The setup deadline then
  extends to five minutes so the user can finish multi-factor or Conditional
  Access steps. Azure CLI output is matched locally and never printed or
  recorded.
- **`interactive_connect: false`** in user settings switches SSH certificate
  setup to refuse mode. Bivrost opens nothing, stops the Azure CLI process group
  and directs the user to `bivrost login --ssh`. Diagnostics record the outcome
  `interaction_required`.
- **Other Azure CLI commands** (doctor, discovery, Heimdal, tunnels, ACR) always
  use refuse mode. Azure CLI does not fall back to a browser for their token
  requests, so this only enforces that diagnostics stay non-interactive.
- **`bivrost login`** uses interactive mode when `authentication_browser` is
  configured; otherwise Azure CLI keeps its own browser selection unchanged.
  `--ssh` requests the Azure Linux VM sign-in application scope in advance, and
  `--device-code` remains available where tenants allow it.
- **`authentication_browser`** names an `executable` and `arguments`; an argument
  exactly `{url}` receives the sign-in address, which is otherwise appended. The
  browser is detached and started directly, never through a shell. Batch files
  are rejected because Windows would run them through `cmd.exe`. If it cannot be
  started, Bivrost writes the sign-in address to the user's terminal for manual
  use and does not fall back to the system default.

Settings live in the user's Bivrost `settings.json` (ADR 0001). Bivrost does not
change operating system browser defaults, Azure CLI configuration or browser
profiles, and catalogues cannot select a browser.

The `authbrowser` package depends only on `config`; `azure`, `session` and the
executable use it. The executable dispatches browser requests before Podman
basename dispatch, extending ADR 0011.

## Consequences

Connect keeps working through multi-factor prompts, now in the chosen profile
and with an explanation instead of a silent stall. Users who prefer never to
see a browser during connect can opt out.

When Bivrost declines to choose a browser, MSAL's Linux preference for Edge no
longer applies, because `BROWSER` is set; the user's `BROWSER` value or the
system default is used instead.

On Windows, Azure CLI uses the WAM broker by default. WAM shows its own account
dialog rather than a browser, including for SSH certificates, and `BROWSER`
cannot intercept it or signal Bivrost. Such prompts keep the two-minute setup
deadline. Disabling the broker would also change silent token acquisition, so
Bivrost leaves it unchanged.

If no browser can open, MSAL waits for a redirect that never arrives; the
five-minute deadline then ends setup. Whether `az login --scope` satisfies the
silent SSH certificate request in advance depends on tenant policy and needs
live verification.

The design relies on Azure CLI and MSAL using Python's `webbrowser`. A future
Azure CLI release that changes this would bypass Bivrost's browser selection,
and detection would fall back to the setup deadline.

## Alternatives considered

- Refusing all browser sign-in during connect and requiring a separate login
  would remove the in-line multi-factor flow users rely on, and device-code
  sign-in is being disabled by some organizations.
- Passing a shell command template through `BROWSER` with `%s` would make the
  sign-in address part of command text and depend on `webbrowser` parsing.
- Disabling the WAM broker or editing Azure CLI configuration would change
  global client behavior that Bivrost does not own.
- Calling Microsoft Entra directly for SSH certificates would duplicate Azure
  CLI token caching and Conditional Access handling.
