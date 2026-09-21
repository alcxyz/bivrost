package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	profile "github.com/alcxyz/bivrost/internal/config"
	shellinit "github.com/alcxyz/bivrost/internal/shell"
)

func TestShellActivationUpdatesOnlySuccessfulEnable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell integration")
	}
	for _, name := range []string{"bash", "zsh"} {
		t.Run(name, func(t *testing.T) {
			shell, err := exec.LookPath(name)
			if err != nil {
				t.Skip("shell unavailable")
			}
			home := t.TempDir()
			executable := filepath.Join(home, "bivrost executable")
			activation := filepath.Join(home, "activation file")
			// A stale file must not be sourced following another command or a failure.
			for path, contents := range map[string]string{
				executable:                         "#!/bin/sh\nexit \"${BIVROST_TEST_STATUS:-0}\"\n",
				activation:                         "export CONTAINER_HOST='ssh://session'\n",
				filepath.Join(home, "."+name+"rc"): "alias bivrost='false'\nexport BIVROST_TEST_STARTUP=loaded\n",
			} {
				if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
					t.Fatal(err)
				}
			}
			settings := profile.DefaultPromptSettings()
			settings.Enabled = false
			args, env, err := shellinit.PreparePrompt(t.TempDir(), shell, []string{"-i"}, []string{
				"PATH=" + os.Getenv("PATH"), "HOME=" + home,
				"BIVROST_EXECUTABLE=" + executable, "BIVROST_ACR_ENV_FILE=" + activation,
			}, profile.Profile{Prompt: &settings})
			if err != nil {
				t.Fatal(err)
			}
			script := `
bivrost version || exit 20
[[ -z "${CONTAINER_HOST:-}" ]] || exit 21
bivrost acr enable extra || exit 22
[[ -z "${CONTAINER_HOST:-}" ]] || exit 23
export BIVROST_TEST_STATUS=7
bivrost acr enable
[[ $? == 7 && -z "${CONTAINER_HOST:-}" ]] || exit 24
unset BIVROST_TEST_STATUS
bivrost acr enable || exit 25
[[ "$CONTAINER_HOST" == ssh://session ]] || exit 26
[[ "$BIVROST_TEST_STARTUP" == loaded ]] || exit 27
if typeset -f _bivrost_prompt_indicator >/dev/null; then exit 28; fi
printf 'activation-ok\n'
`
			cmd := exec.Command(shell, append(args, "-c", script)...)
			cmd.Env = env
			output, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(output), "activation-ok") {
				t.Fatalf("activation failed: %v: %s", err, output)
			}
		})
	}
}

func TestPowerShellActivationUpdatesOnlySuccessfulEnable(t *testing.T) {
	shell, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell unavailable")
	}
	activation := filepath.Join(t.TempDir(), "activation file.json")
	if err := os.WriteFile(activation, []byte(`{"CONTAINER_HOST":"ssh://session","CONTAINERS_CONF_OVERRIDE":"C:/Users/O’Brian/session"}`), 0600); err != nil {
		t.Fatal(err)
	}
	settings := profile.DefaultPromptSettings()
	settings.Enabled = false
	_, env, err := shellinit.PreparePrompt(t.TempDir(), shell, nil, []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		"BIVROST_EXECUTABLE=Invoke-BivrostTest", "BIVROST_ACR_ENV_FILE=" + activation,
	}, profile.Profile{Prompt: &settings})
	if err != nil {
		t.Fatal(err)
	}
	script := `
function global:Invoke-BivrostTest { $global:LASTEXITCODE = [int]$env:BIVROST_TEST_STATUS }
Set-Alias bivrost Write-Error
` + shellinit.PowerShellPromptInit + `
bivrost version
if ($LASTEXITCODE -ne 0 -or $env:CONTAINER_HOST) { exit 20 }
bivrost acr enable extra
if ($env:CONTAINER_HOST) { exit 21 }
$env:BIVROST_TEST_STATUS = '7'
bivrost acr enable
if ($LASTEXITCODE -ne 7 -or $env:CONTAINER_HOST) { exit 22 }
$env:BIVROST_TEST_STATUS = '0'
$env:CONTAINER_CONNECTION = 'stale'
$env:CONTAINER_SSHKEY = 'stale'
bivrost acr enable
if ($LASTEXITCODE -ne 0 -or $env:CONTAINER_HOST -ne 'ssh://session') { exit 23 }
if ($env:CONTAINER_CONNECTION -or $env:CONTAINER_SSHKEY) { exit 25 }
$expectedPath = 'C:/Users/O' + [char]0x2019 + 'Brian/session'
if ($env:CONTAINERS_CONF_OVERRIDE -cne $expectedPath) { exit 26 }
if (Get-Variable BivrostOriginalPrompt -ErrorAction SilentlyContinue) { exit 24 }
foreach ($invalid in @('{"CONTAINER_HOST":"changed","UNEXPECTED":"value"}', '{"CONTAINER_HOST":7}', '[{"CONTAINER_HOST":"changed"}]', '{"CONTAINER_HOST":null}')) {
    [IO.File]::WriteAllText($env:BIVROST_ACR_ENV_FILE, $invalid, [Text.Encoding]::UTF8)
    $rejected = $false
    try { bivrost acr enable } catch { $rejected = $true }
    if (-not $rejected -or $LASTEXITCODE -ne 1) { exit 27 }
    if ($env:CONTAINER_HOST -cne 'ssh://session' -or $env:CONTAINERS_CONF_OVERRIDE -cne $expectedPath) { exit 28 }
}
Write-Output 'activation-ok'
`
	cmd := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "activation-ok") {
		t.Fatalf("activation failed: %v: %s", err, output)
	}
}
