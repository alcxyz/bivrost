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

func TestPodmanWrapperDeferredShellPath(t *testing.T) {
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
			wrapper := filepath.Join(home, "wrapper's bin")
			addition := filepath.Join(home, "user bin")
			if err := os.Mkdir(wrapper, 0700); err != nil {
				t.Fatal(err)
			}
			activation := filepath.Join(home, "activation")
			executable := filepath.Join(home, "bivrost")
			child := filepath.Join(home, "child")
			for path, contents := range map[string]string{
				executable:                       "#!/bin/sh\nexit 0\n",
				filepath.Join(wrapper, "podman"): "#!/bin/sh\nprintf 'wrapper:%s\\n' \"$*\"\n",
				activation:                       activationScript(name, &podmanSession{configuration: filepath.Join(home, "config"), wrapperDirectory: wrapper}),
				child:                            "#!/bin/sh\npodman build 'context with spaces'\n",
			} {
				if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
					t.Fatal(err)
				}
			}
			settings := profile.DefaultPromptSettings()
			settings.Enabled = false
			args, env, err := shellinit.PreparePrompt(t.TempDir(), shell, []string{"-i"}, []string{
				"PATH=" + os.Getenv("PATH"), "HOME=" + home, "BIVROST_EXECUTABLE=" + executable,
				"BIVROST_ACR_ENV_FILE=" + activation, "TEST_WRAPPER=" + wrapper, "TEST_ADDITION=" + addition, "TEST_CHILD=" + child,
			}, profile.Profile{Prompt: &settings})
			if err != nil {
				t.Fatal(err)
			}
			script := `
export PATH="$TEST_ADDITION:$PATH"
original_path="$PATH"
bivrost acr enable || exit 20
[[ "$PATH" == "$TEST_WRAPPER:$original_path" ]] || exit 21
[[ "$BIVROST_PODMAN_BIN" == "$TEST_WRAPPER" ]] || exit 22
[[ "$("$TEST_CHILD")" == 'wrapper:build context with spaces' ]] || exit 23
bivrost acr enable || exit 24
[[ "$PATH" == "$TEST_WRAPPER:$original_path" ]] || exit 25
if typeset -f _bivrost_prompt_indicator >/dev/null; then exit 26; fi
printf 'wrapper-path-ok\n'
`
			cmd := exec.Command(shell, append(args, "-c", script)...)
			cmd.Env = env
			output, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(string(output), "wrapper-path-ok") {
				t.Fatalf("wrapper path activation failed: %v: %s", err, output)
			}
		})
	}
}

func TestPodmanWrapperPowerShellPath(t *testing.T) {
	shell, err := exec.LookPath("pwsh")
	if err != nil {
		t.Skip("PowerShell unavailable")
	}
	home := t.TempDir()
	wrapper := filepath.Join(home, "wrapper's bin")
	if err := os.Mkdir(wrapper, 0700); err != nil {
		t.Fatal(err)
	}
	activation := filepath.Join(home, "activation.json")
	for path, contents := range map[string]string{
		activation:                           activationScript("pwsh", &podmanSession{configuration: filepath.Join(home, "config"), wrapperDirectory: wrapper}),
		filepath.Join(wrapper, "podman.ps1"): "Write-Output ('wrapper:' + ($args -join '|'))\n",
	} {
		if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
			t.Fatal(err)
		}
	}
	script := `
function global:Invoke-BivrostTest { $global:LASTEXITCODE = 0 }
` + shellinit.PowerShellPromptInit + `
$env:PATH = $env:TEST_ADDITION + [IO.Path]::PathSeparator + $env:PATH
$originalPath = $env:PATH
bivrost acr enable
$expectedPath = $env:TEST_WRAPPER + [IO.Path]::PathSeparator + $originalPath
if ($env:PATH -cne $expectedPath) { exit 20 }
if ($env:BIVROST_PODMAN_BIN -cne $env:TEST_WRAPPER) { exit 21 }
$result = & $env:TEST_PWSH -NoLogo -NoProfile -NonInteractive -Command 'podman.ps1 build "context with spaces"'
if ($result -cne 'wrapper:build|context with spaces') { exit 22 }
bivrost acr enable
if ($env:PATH -cne $expectedPath) { exit 23 }
if (Get-Variable BivrostOriginalPrompt -ErrorAction SilentlyContinue) { exit 24 }
Write-Output 'wrapper-path-ok'
`
	cmd := exec.Command(shell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "BIVROST_PROMPT_ENABLED=0", "BIVROST_EXECUTABLE=Invoke-BivrostTest", "BIVROST_ACR_ENV_FILE="+activation,
		"TEST_WRAPPER="+wrapper, "TEST_ADDITION="+filepath.Join(home, "user bin"), "TEST_PWSH="+shell)
	cmd.Env = shellinit.ReplaceEnvironment(cmd.Env, "BIVROST_PODMAN_BIN", "")
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "wrapper-path-ok") {
		t.Fatalf("wrapper path activation failed: %v: %s", err, output)
	}
}

func TestDoctorSessionEnvironmentRejectsWrapperBypass(t *testing.T) {
	c := platformTestConfig(t)
	directory := t.TempDir()
	configuration := filepath.Join(directory, "containers.conf")
	if err := os.WriteFile(configuration, []byte("[containers]\nhttp_proxy=false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	wrapper, other := filepath.Join(directory, "wrapper"), filepath.Join(directory, "other")
	binary := "podman"
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	for _, path := range []string{wrapper, other} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, binary), []byte("placeholder"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	status := &doctorSessionStatus{Enabled: true, Environment: map[string]string{"CONTAINERS_CONF_OVERRIDE": configuration, "BIVROST_PODMAN_BIN": wrapper}}
	doctorTestActiveEnvironment(t, c, status)
	t.Setenv("BIVROST_PODMAN_BIN", wrapper)
	t.Setenv("PATH", wrapper)
	if !doctorSessionEnvironment(c, status) {
		t.Fatal("matching wrapper rejected")
	}
	t.Run("earlier executable", func(t *testing.T) {
		t.Setenv("PATH", other+string(os.PathListSeparator)+wrapper)
		if doctorSessionEnvironment(c, status) {
			t.Fatal("bypassed wrapper accepted")
		}
	})
	t.Run("missing executable", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if doctorSessionEnvironment(c, status) {
			t.Fatal("missing wrapper accepted")
		}
	})
	t.Run("changed marker", func(t *testing.T) {
		t.Setenv("BIVROST_PODMAN_BIN", other)
		if doctorSessionEnvironment(c, status) {
			t.Fatal("changed wrapper marker accepted")
		}
	})
}
