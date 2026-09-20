package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSessionPromptLabelIsLiteral(t *testing.T) {
	c := config{Environment: "prod$(touch /tmp/injected)%", AKS: &aksConfig{Name: "cluster`command`"}}
	label := sessionLabel(c)
	if strings.ContainsAny(label, "$`%\\\n") {
		t.Fatalf("unsafe label: %q", label)
	}
}

// Execute only synthetic startup files: never load the tester's real shell files.
func TestSessionPromptPreservesShellStartup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows uses native PowerShell")
	}
	for _, shell := range []string{"bash", "zsh"} {
		t.Run(shell, func(t *testing.T) {
			executable, err := exec.LookPath(shell)
			if err != nil {
				t.Skip("shell unavailable")
			}
			home := t.TempDir()
			startup := home
			env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}
			script := ""
			if shell == "bash" {
				err = os.WriteFile(filepath.Join(home, ".bashrc"), []byte("PROMPT_COMMAND='PS1=original-prompt'\n"), 0600)
				script = "eval \"$PROMPT_COMMAND\"; eval \"$PROMPT_COMMAND\"; printf '%s\\n' \"$PS1\""
			} else {
				startup = filepath.Join(home, "custom-startup")
				if err = os.Mkdir(startup, 0700); err != nil {
					t.Fatal(err)
				}
				env = append(env, "ZDOTDIR="+startup)
				err = os.WriteFile(filepath.Join(startup, ".zshenv"), []byte("export BIVROST_TEST_STARTUP=loaded\n"), 0600)
				if err != nil {
					t.Fatal(err)
				}
				err = os.WriteFile(filepath.Join(startup, ".zshrc"), []byte("precmd() { PROMPT=original-prompt; }\n"), 0600)
				script = "precmd; for hook in $precmd_functions; do $hook; done; _bivrost_prompt_indicator; print -r -- \"$PROMPT\"; print -r -- \"$BIVROST_TEST_STARTUP:$ZDOTDIR\""
			}
			if err != nil {
				t.Fatal(err)
			}
			original := append([]string(nil), env...)
			args, childEnv, err := prepareSessionPrompt(t.TempDir(), executable, []string{"-i"}, env, config{Environment: "staging", AKS: &aksConfig{Name: "test-cluster"}})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(env, "\n") != strings.Join(original, "\n") {
				t.Fatal("parent environment changed")
			}
			cmd := exec.Command(executable, append(args, "-c", script)...)
			cmd.Env = childEnv
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("shell failed: %v: %s", err, output)
			}
			want := `original-prompt \[\e[1;36m\][Bivrost: staging / test-cluster]\[\e[0m\] `
			if shell == "zsh" {
				want = "original-prompt %B%F{cyan}[Bivrost: staging / test-cluster]%f%b "
			}
			if strings.Count(string(output), want) != 1 {
				t.Fatalf("prompt missing or duplicated: %s", output)
			}
			if shell == "zsh" && !strings.Contains(string(output), "loaded:"+startup) {
				t.Fatalf("startup or ZDOTDIR not preserved: %s", output)
			}
		})
	}
}

func TestZshIndicatorFollowsDynamicallyRenderedMultilinePrompt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell test")
	}
	shell, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	home := t.TempDir()
	startup := `setopt promptsubst
_theme_prompt() { printf '\ncloud-prod\nuser@host\n> '; }
PROMPT='$(_theme_prompt)'
`
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(startup), 0600); err != nil {
		t.Fatal(err)
	}
	args, env, err := prepareSessionPrompt(t.TempDir(), shell, []string{"-i"}, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home}, config{Environment: "staging", AKS: &aksConfig{Name: "test-cluster"}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(shell, append(args, "-c", `_bivrost_prompt_indicator; _bivrost_prompt_indicator; print -P -- "$PROMPT"`)...)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("shell failed: %v: %s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	last := lines[len(lines)-1]
	if !strings.HasPrefix(last, "> ") || !strings.Contains(last, "[Bivrost: staging / test-cluster]") || strings.Count(string(output), "[Bivrost:") != 1 {
		t.Fatalf("indicator must appear once beside input cursor, got %q", output)
	}
}

func TestZshPromptFrameworks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell test")
	}
	shell, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	for _, framework := range []string{"starship", "oh-my-zsh"} {
		t.Run(framework, func(t *testing.T) {
			home := t.TempDir()
			env := []string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=xterm-256color"}
			var startup string
			if framework == "starship" {
				if _, err := exec.LookPath("starship"); err != nil {
					t.Skip("starship unavailable")
				}
				profile := os.Getenv("BIVROST_TEST_STARSHIP_CONFIG")
				if profile == "" {
					profile = filepath.Join(home, "starship.toml")
					if err := os.WriteFile(profile, []byte(`format = '$line_break$username$line_break$character'
[username]
show_always = true
`), 0600); err != nil {
						t.Fatal(err)
					}
				}
				env = append(env, "STARSHIP_CONFIG="+profile)
				startup = `eval "$(starship init zsh)"`
			} else {
				root := os.Getenv("BIVROST_TEST_OH_MY_ZSH")
				if root == "" {
					t.Skip("set BIVROST_TEST_OH_MY_ZSH to test an installed Oh My Zsh")
				}
				env = append(env, "ZSH="+root, "ZSH_CACHE_DIR="+home+"/cache")
				startup = `ZSH_THEME=robbyrussell
plugins=()
zstyle ':omz:update' mode disabled
ZSH_DISABLE_COMPFIX=true
source "$ZSH/oh-my-zsh.sh"`
			}
			if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(startup), 0600); err != nil {
				t.Fatal(err)
			}
			args, childEnv, err := prepareSessionPrompt(t.TempDir(), shell, []string{"-i"}, env, config{Environment: "staging", AKS: &aksConfig{Name: "test-cluster"}})
			if err != nil {
				t.Fatal(err)
			}
			script := `for pass in 1 2; do
     (( $+functions[precmd] )) && precmd
     for hook in $precmd_functions; do "$hook"; done
   done
   print -P -- "$PROMPT"`
			cmd := exec.Command(shell, append(args, "-c", script)...)
			cmd.Env = childEnv
			cmd.Dir = home
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("shell failed: %v: %s", err, output)
			}
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			last := lines[len(lines)-1]
			if !strings.Contains(last, "[Bivrost: staging / test-cluster]") || strings.Count(string(output), "[Bivrost:") != 1 {
				t.Fatalf("indicator missing from cursor line or duplicated: %q", output)
			}
		})
	}
}

func TestDisabledPromptLeavesShellStartupUntouched(t *testing.T) {
	directory := t.TempDir()
	settings := defaultPromptSettings()
	settings.Enabled = false
	args, env, err := prepareSessionPrompt(directory, "/bin/zsh", []string{"-i"}, []string{"ZDOTDIR=/existing"}, config{Prompt: &settings, Environment: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 1 || args[0] != "-i" || environmentValue(env, "ZDOTDIR") != "/existing" {
		t.Fatal("disabled integration changed shell startup")
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 0 {
		t.Fatalf("disabled integration wrote startup files: %v", err)
	}
}

func TestCustomPromptPlacementWithoutColour(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell test")
	}
	shell, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	for _, placement := range []string{"prefix", "suffix"} {
		t.Run(placement, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("PROMPT='> '"), 0600); err != nil {
				t.Fatal(err)
			}
			settings := defaultPromptSettings()
			settings.Placement = placement
			settings.Colour = "none"
			settings.Label = "[{environment}]"
			args, env, err := prepareSessionPrompt(t.TempDir(), shell, []string{"-i"}, []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}, config{Prompt: &settings, Environment: "staging"})
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(shell, append(args, "-c", `_bivrost_prompt_indicator; _bivrost_prompt_indicator; print -r -- "$PROMPT"`)...)
			cmd.Env = env
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("shell failed: %v: %s", err, output)
			}
			want := ">  [staging] \n"
			if placement == "prefix" {
				want = "[staging] > \n"
			}
			if string(output) != want {
				t.Fatalf("prompt = %q, want %q", output, want)
			}
		})
	}
}
