# Authentication and browser profiles

Bivrost uses your local Azure CLI sign-in. Run `bivrost login` (or `az login`)
first. Later commands reuse that sign-in:

- `connect`, `switch` and `ssh` may open a browser when Azure requires new
  sign-in for the VM SSH certificate, for example multi-factor authentication.
- `doctor`, `acr` and discovery commands never open a browser.

## Sign-in during connect

Azure CLI requests the SSH certificate silently. When Azure requires more, such
as multi-factor authentication, a Conditional Access step-up, consent or a
renewed session, Azure CLI opens a sign-in page. Bivrost opens it in your
configured authentication browser and explains the wait:

```text
Preparing a short-lived Entra SSH certificate using your local Azure login...
Azure needs interactive sign-in for the SSH certificate: multi-factor
authentication or a Conditional Access step-up is required.
Continue in the browser window; waiting up to 5m0s...
```

Finish sign-in in the browser; connect then continues. Network failures are
reported separately and never open a browser.

To prevent browser sign-in during connect, add this to your settings:

```json
{ "interactive_connect": false }
```

Connect then stops with the reason instead. Run `bivrost login --ssh`, which
signs in for the Azure Linux VM sign-in application, and connect again. Some
tenant policies still require the in-connect sign-in.

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

The setting applies to `bivrost login` and to sign-in during connect.
`executable` is a program path or a name found on `PATH`. Each entry in
`arguments` is passed as one argument, without a shell. An argument that is
exactly `{url}` receives the sign-in address; without one, the address is
appended. Batch files (`.bat`, `.cmd`) are rejected.

Examples, as JSON values:

| Platform | `executable` | `arguments` |
| --- | --- | --- |
| Linux, Chromium-based | `"/usr/bin/google-chrome"` | `["--profile-directory=Profile 1", "{url}"]` |
| macOS, Chrome | `"/usr/bin/open"` | `["-na", "Google Chrome", "--args", "--profile-directory=Profile 1", "{url}"]` |
| macOS, Firefox | `"/Applications/Firefox.app/Contents/MacOS/firefox"` | `["-P", "work", "{url}"]` |
| Windows, Edge | `"C:\\Program Files (x86)\\Microsoft\\Edge\\Application\\msedge.exe"` | `["--profile-directory=Profile 1", "{url}"]` |

Find a Chromium profile directory name on its `chrome://version` or
`edge://version` page.

Without this setting, sign-in during connect uses your `BROWSER` environment
variable if set, otherwise the system default browser, and `bivrost login`
leaves the choice to Azure CLI. Bivrost never changes the system default
browser. On Linux, `BROWSER` entries naming `microsoft-edge` are skipped during
connect, because Azure CLI would open Edge directly without telling Bivrost; use
`authentication_browser` to choose Edge.

If no browser can open, for example over SSH without a display, connect stops
and suggests `authentication_browser` or `bivrost login --ssh` on a machine with
a browser.

If the configured browser cannot start, Bivrost does not fall back to the system
default. It prints the sign-in address on your terminal so you can open it in
the right profile; sign-in then completes normally. Where your organization
still allows it, `bivrost login --device-code` avoids the local browser.

## Windows

Azure CLI on Windows signs in through the Web Account Manager (WAM) by default.
WAM shows an account dialog instead of a browser, so `authentication_browser`
applies only if you have disabled the broker in Azure CLI. Bivrost cannot detect
a WAM dialog during connect, so it keeps the normal two-minute setup limit.

## Privacy

Sign-in addresses, tokens and Azure CLI output are not written to Bivrost
diagnostics. Diagnostics record only whether sign-in was required. The address
appears on the terminal only in the manual fallback above.

See [ADR 0015](adr/0015-authentication-browser-boundary.md) for the design.
