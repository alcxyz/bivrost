package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUnixShellSwitchWrapper(t *testing.T) {
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
			executable := filepath.Join(home, "fake bivrost")
			activation := filepath.Join(home, "activation")
			fake := `#!/bin/sh
printf 'binary:%s:%s\n' "${BIVROST_SWITCH_ALLOWED-unset}" "$*"
if [ "$1" = switch ]; then
    case "$2" in
        accepted) exit 85 ;;
        --help) exit 0 ;;
        failed) exit 7 ;;
    esac
fi
if [ "$1" = sentinel ]; then exit 85; fi
exit 0
`
			if err := os.WriteFile(executable, []byte(fake), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(activation, nil, 0600); err != nil {
				t.Fatal(err)
			}
			settings := defaultPromptSettings()
			settings.Enabled = false
			args, env, err := prepareSessionPrompt(t.TempDir(), shell, []string{"-i"}, []string{
				"PATH=" + os.Getenv("PATH"), "HOME=" + home,
				"BIVROST_EXECUTABLE=" + executable, "BIVROST_ACR_ENV_FILE=" + activation,
			}, config{Prompt: &settings})
			if err != nil {
				t.Fatal(err)
			}
			run := func(script string) (string, int) {
				t.Helper()
				cmd := exec.Command(shell, append(args, "-c", script)...)
				cmd.Env = env
				output, err := cmd.CombinedOutput()
				if err == nil {
					return string(output), 0
				}
				var exitError *exec.ExitError
				if !errors.As(err, &exitError) {
					t.Fatalf("shell failed to run: %v: %s", err, output)
				}
				return string(output), exitError.ExitCode()
			}

			t.Run("accepted exits shell", func(t *testing.T) {
				output, status := run("bivrost switch accepted; printf 'continued\\n'")
				if status != 85 || !strings.Contains(output, "binary:1:switch accepted") || strings.Contains(output, "continued") {
					t.Fatalf("accepted switch did not exit the shell with 85: status %d, output %q", status, output)
				}
			})

			t.Run("other outcomes continue", func(t *testing.T) {
				script := `
unset BIVROST_SWITCH_ALLOWED
bivrost switch --help || exit 20
[[ -z "${BIVROST_SWITCH_ALLOWED+x}" ]] || exit 21
bivrost switch failed
[[ $? == 7 ]] || exit 22
bivrost sentinel
[[ $? == 85 ]] || exit 23
printf 'continued\n'
`
				output, status := run(script)
				if status != 0 || !strings.Contains(output, "binary:1:switch --help") || !strings.Contains(output, "binary:1:switch failed") || !strings.Contains(output, "binary:unset:sentinel") || !strings.Contains(output, "continued") {
					t.Fatalf("non-accepted command changed shell lifetime or leaked permission: status %d, output %q", status, output)
				}
			})

			t.Run("subshells and pipelines rejected", func(t *testing.T) {
				pipelineStatus := "${PIPESTATUS[0]}"
				if name == "zsh" {
					pipelineStatus = "${pipestatus[1]}"
				}
				script := `
( bivrost switch accepted )
[[ $? == 1 ]] || exit 20
bivrost switch accepted | cat
[[ ` + pipelineStatus + ` == 1 ]] || exit 21
printf 'guarded\n'
`
				output, status := run(script)
				if status != 0 || !strings.Contains(output, "guarded") || strings.Contains(output, "binary:") {
					t.Fatalf("nested switch reached the binary or ended the parent shell: status %d, output %q", status, output)
				}
			})

			t.Run("background job preserved", func(t *testing.T) {
				script := `
sleep 1 &
bivrost_test_job=$!
bivrost switch accepted
[[ $? == 1 ]] || exit 20
kill -0 "$bivrost_test_job" 2>/dev/null || exit 21
wait "$bivrost_test_job" || exit 22
printf 'job-preserved\n'
`
				output, status := run(script)
				if status != 0 || !strings.Contains(output, "job-preserved") || strings.Contains(output, "binary:") {
					t.Fatalf("switch did not preserve a shell job: status %d, output %q", status, output)
				}
			})
		})
	}
}

func TestPowerShellSwitchWrapper(t *testing.T) {
	shell, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell unavailable")
	}
	script := `
function global:Invoke-BivrostSwitchTest {
    $global:BivrostCalls += ,($args -join ' ')
    $global:BivrostPermissions += ,$(if (Test-Path Env:BIVROST_SWITCH_ALLOWED) { $env:BIVROST_SWITCH_ALLOWED } else { 'unset' })
    if ($args[0] -ceq 'switch') {
        if ($args[1] -ceq 'accepted') { $global:LASTEXITCODE = 85; return }
        if ($args[1] -ceq '--help') { $global:LASTEXITCODE = 0; return }
        if ($args[1] -ceq 'failed') { $global:LASTEXITCODE = 7; return }
    }
    if ($args[0] -ceq 'sentinel') { $global:LASTEXITCODE = 85; return }
    $global:LASTEXITCODE = 0
}
` + powershellPromptInit + `
$env:BIVROST_SWITCH_ALLOWED = 'original'
bivrost switch --help
if ($LASTEXITCODE -ne 0 -or $env:BIVROST_SWITCH_ALLOWED -cne 'original' -or $global:BivrostPermissions[-1] -cne '1') { exit 20 }
Remove-Item Env:BIVROST_SWITCH_ALLOWED
bivrost switch failed
if ($LASTEXITCODE -ne 7 -or (Test-Path Env:BIVROST_SWITCH_ALLOWED) -or $global:BivrostPermissions[-1] -cne '1') { exit 21 }
bivrost sentinel
if ($LASTEXITCODE -ne 85 -or $global:BivrostPermissions[-1] -cne 'unset') { exit 22 }
$beforePipeline = $global:BivrostCalls.Count
bivrost switch accepted | Out-Null
if ($LASTEXITCODE -ne 1 -or $global:BivrostCalls.Count -ne $beforePipeline) { exit 23 }
$beforeScript = $global:BivrostCalls.Count
& $env:BIVROST_TEST_SWITCH_SCRIPT
if ($LASTEXITCODE -ne 1 -or $global:BivrostCalls.Count -ne $beforeScript) { exit 24 }
$job = Start-Job { Start-Sleep -Seconds 2 }
try {
    $beforeJob = $global:BivrostCalls.Count
    bivrost switch accepted
    if ($LASTEXITCODE -ne 1 -or $global:BivrostCalls.Count -ne $beforeJob) { exit 25 }
} finally {
    Stop-Job $job -ErrorAction SilentlyContinue
    $beforeStoppedJob = $global:BivrostCalls.Count
    bivrost switch accepted
    if ($LASTEXITCODE -ne 1 -or $global:BivrostCalls.Count -ne $beforeStoppedJob) { exit 26 }
    Remove-Job $job -Force -ErrorAction SilentlyContinue
}
Write-Output 'before-accepted'
bivrost switch accepted
Write-Output 'continued'
`
	cmd := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = replaceEnvironment(os.Environ(), "BIVROST_PROMPT_ENABLED", "0")
	cmd.Env = replaceEnvironment(cmd.Env, "BIVROST_EXECUTABLE", "Invoke-BivrostSwitchTest")
	cmd.Env = replaceEnvironment(cmd.Env, "BIVROST_ACR_ENV_FILE", filepath.Join(t.TempDir(), "unused.json"))
	switchScript := filepath.Join(t.TempDir(), "nested switch.ps1")
	if err := os.WriteFile(switchScript, []byte("bivrost switch accepted\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd.Env = replaceEnvironment(cmd.Env, "BIVROST_TEST_SWITCH_SCRIPT", switchScript)
	output, err := cmd.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 85 || !strings.Contains(string(output), "before-accepted") || strings.Contains(string(output), "continued") {
		t.Fatalf("PowerShell switch wrapper failed: %v: %s", err, output)
	}
}
