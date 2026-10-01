# Authentication and browser profiles

Bivrost uses your local Azure CLI sign-in. Signing in and connecting are
separate steps:

- `bivrost login` (or `az login`) is the only Bivrost command that may open a
  browser.
- `connect`, `ssh`, `switch`, `doctor`, `acr` and discovery commands reuse the
  existing sign-in. They never open a browser.

## When connect needs a new sign-in

For VM SSH certificates, Azure CLI normally switches to browser sign-in when the
silent request fails. Bivrost refuses that request and stops with a message such
as:

```text
interactive sign-in required for the Entra SSH certificate: multi-factor
authentication or a Conditional Access step-up is required. Bivrost did not
open a browser; run bivrost login --ssh, then connect again
```

The reason comes from the Microsoft Entra error code that Azure CLI reported:
multi-factor or Conditional Access step-up, a device or app requirement,
missing consent, or an expired sign-in. Network failures are reported
separately and do not ask for sign-in.

`bivrost login --ssh` signs in for the Azure Linux VM sign-in application, so the
next connect can request the certificate silently. Some tenant policies may
still block the request; check with your Azure administrator if the message
persists.

## Choose the sign-in browser

Add `authentication_browser` to the Bivrost user settings file
(`settings.json` under `XDG_CONFIG_HOME/bivrost` when set, otherwise the
platform's user configuration directory; `bivrost config init` creates it):

```json
{
  "authentication_browser": {
    "executable": "/usr/bin/firefox",
    "arguments": ["-P", "work", "--new-window", "{url}"]
  }
}
```

`executable` is a program path or a name found on `PATH`. Each entry in
`arguments` is passed as one argument, without a shell. An argument that is
exactly `{url}` receives the sign-in address; without one, the address is
appended. Batch files (`.bat`, `.cmd`) are rejected.

Examples:

| Platform | `executable` | `arguments` |
| --- | --- | --- |
| Linux, Chromium-based | `/usr/bin/google-chrome` | `["--profile-directory=Profile 1", "{url}"]` |
| macOS, Chrome | `/usr/bin/open` | `["-na", "Google Chrome", "--args", "--profile-directory=Profile 1", "{url}"]` |
| macOS, Firefox | `/Applications/Firefox.app/Contents/MacOS/firefox` | `["-P", "work", "{url}"]` |
| Windows, Edge | `C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe` | `["--profile-directory=Profile 1", "{url}"]` |

Find a Chromium profile directory name on its `chrome://version` or
`edge://version` page.

Without this setting, `bivrost login` leaves the choice to Azure CLI: your
`BROWSER` variable if set, otherwise Azure CLI's default. Bivrost never changes
the system default browser.

If the configured browser cannot start, Bivrost does not fall back to the system
default. It prints the sign-in address on the terminal so you can open it in
the right profile, or you can press Ctrl+C and use device code:

```text
bivrost login --device-code
bivrost login --device-code --ssh
```

Device-code sign-in cannot satisfy Conditional Access policies that require a
compliant or managed device.

## Windows

Azure CLI on Windows signs in through the Web Account Manager (WAM) by default.
WAM shows an account dialog instead of a browser, so `authentication_browser`
applies only if you have disabled the broker in Azure CLI. Bivrost cannot
intercept a WAM dialog during connect; such a prompt is still bounded by the
connection setup timeout.

## Privacy

Sign-in addresses, tokens and Azure CLI output are not written to Bivrost
diagnostics. Diagnostics record only that sign-in was required. The address
appears on the terminal only in the manual fallback above.

See [ADR 0015](adr/0015-authentication-browser-boundary.md) for the design.
