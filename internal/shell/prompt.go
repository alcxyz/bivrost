// Package shell prepares temporary startup hooks and child shell environments.
package shell

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	profile "github.com/alcxyz/bivrost/internal/config"
)

// The indicator describes the connection selected at startup, not a live context.
func Label(c profile.Profile) string {
	environment := c.Environment
	if environment == "" {
		environment = "custom-profile"
	}
	cluster := "no AKS"
	if c.AKS != nil {
		cluster = c.AKS.Name
	}
	settings := effectivePromptSettings(c)
	return strings.NewReplacer("{environment}", promptLabelPart(environment), "{cluster}", promptLabelPart(cluster)).Replace(settings.Label)
}

func effectivePromptSettings(c profile.Profile) profile.PromptSettings {
	if c.Prompt != nil {
		return *c.Prompt
	}
	return profile.DefaultPromptSettings()
}

// Prompt expansion must never interpret profile metadata as shell code.
func promptLabelPart(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(" ._-", r) {
			return r
		}
		return '_'
	}, s)
}

func PreparePrompt(directory, shell string, args, env []string, c profile.Profile) ([]string, []string, error) {
	environment := c.Environment
	if environment == "" {
		environment = "custom-profile"
	}
	env = ReplaceEnvironment(env, "BIVROST_ENVIRONMENT", promptLabelPart(environment))
	cluster := ""
	if c.AKS != nil {
		cluster = c.AKS.Name
	}
	env = ReplaceEnvironment(env, "BIVROST_CLUSTER", promptLabelPart(cluster))
	env = ReplaceEnvironment(env, "BIVROST_PROMPT_LABEL", Label(c))
	settings := effectivePromptSettings(c)
	env = ReplaceEnvironment(env, "BIVROST_PROMPT_PLACEMENT", settings.Placement)
	activation := EnvironmentValue(env, "BIVROST_EXECUTABLE") != "" && EnvironmentValue(env, "BIVROST_ACR_ENV_FILE") != ""
	if !settings.Enabled && !activation {
		return args, env, nil
	}
	enabled := "0"
	if settings.Enabled {
		enabled = "1"
	}
	env = ReplaceEnvironment(env, "BIVROST_PROMPT_ENABLED", enabled)
	shellKind := strings.ToLower(filepath.Base(shell))
	start, end := promptColour(shellKind, settings.Colour)
	env = ReplaceEnvironment(env, "BIVROST_PROMPT_START", start)
	env = ReplaceEnvironment(env, "BIVROST_PROMPT_END", end)
	write := func(name, content string) (string, error) {
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			return "", errors.New("could not prepare the session shell indicator")
		}
		return path, nil
	}
	switch shellKind {
	case "bash":
		path, err := write("bashrc", bashPromptInit)
		if err != nil {
			return nil, nil, err
		}
		return []string{"--rcfile", path, "-i"}, env, nil
	case "zsh":
		env = ReplaceEnvironment(env, "BIVROST_ORIGINAL_ZDOTDIR", EnvironmentValue(env, "ZDOTDIR"))
		env = ReplaceEnvironment(env, "BIVROST_STARTUP_DIR", directory)
		env = ReplaceEnvironment(env, "ZDOTDIR", directory)
		if _, err := write(".zshenv", zshEnvironmentInit); err != nil {
			return nil, nil, err
		}
		if _, err := write(".zshrc", zshPromptInit); err != nil {
			return nil, nil, err
		}
	case "pwsh", "pwsh.exe", "powershell", "powershell.exe":
		return []string{"-NoLogo", "-NoExit", "-Command", PowerShellPromptInit}, env, nil
	}
	return args, env, nil
}

func ReplaceEnvironment(env []string, key, value string) []string {
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(name, key) {
			result = append(result, entry)
		}
	}
	return append(result, key+"="+value)
}

// The controller owns the activation file and writes only quoted, allowlisted
// environment assignments. Source it only after the explicit activation command.
const UnixPodmanPathInit = `if [[ -n "$BIVROST_PODMAN_BIN" ]]; then
    case "$PATH" in
        "$BIVROST_PODMAN_BIN"|"$BIVROST_PODMAN_BIN":*) ;;
        *) export PATH="$BIVROST_PODMAN_BIN:$PATH" ;;
    esac
    hash -r 2>/dev/null || true
fi
`

const unixActivationInit = UnixPodmanPathInit + `if [[ -n "$BIVROST_EXECUTABLE" && -n "$BIVROST_ACR_ENV_FILE" ]]; then
    unalias bivrost 2>/dev/null || true
    function bivrost {
        local bivrost_status=0
        local bivrost_switch=0
        if [[ "$#" -gt 0 && "$1" == switch ]]; then
            bivrost_switch=1
            if [[ -n "${BASH_VERSION-}" && ( -n "${BASHPID-}" && "$BASHPID" != "$$" || -z "${BASHPID-}" && "${BASH_SUBSHELL:-0}" != 0 ) ]]; then
                printf '%s\n' 'bivrost: switch must run directly in the Bivrost shell' >&2
                return 1
            fi
            if [[ -n "${ZSH_VERSION-}" && "${ZSH_SUBSHELL:-0}" != 0 ]]; then
                printf '%s\n' 'bivrost: switch must run directly in the Bivrost shell' >&2
                return 1
            fi
            if [[ -n "${BASH_VERSION-}" && -n "$(jobs -p)" ]]; then
                printf '%s\n' 'bivrost: finish background or stopped shell jobs before switching' >&2
                return 1
            fi
            if [[ -n "${ZSH_VERSION-}" && "${#jobstates}" -gt 0 ]]; then
                printf '%s\n' 'bivrost: finish background or stopped shell jobs before switching' >&2
                return 1
            fi
            BIVROST_SWITCH_ALLOWED=1 "$BIVROST_EXECUTABLE" "$@" || bivrost_status=$?
        else
            "$BIVROST_EXECUTABLE" "$@" || bivrost_status=$?
        fi
        if [[ "$bivrost_status" == 0 && "$#" == 2 && "$1" == acr && "$2" == enable ]]; then
            source "$BIVROST_ACR_ENV_FILE" || return $?
        fi
        if [[ "$bivrost_switch" == 1 && "$bivrost_status" == 85 ]]; then
            exit 85
        fi
        return "$bivrost_status"
    }
fi
`

const bashPromptInit = `if [[ -f "$HOME/.bashrc" ]]; then source "$HOME/.bashrc"; fi
` + unixActivationInit + `if [[ "$BIVROST_PROMPT_ENABLED" != 0 ]]; then
_bivrost_prompt_indicator() {
    local previous_status=$?
    local indicator="$BIVROST_PROMPT_START$BIVROST_PROMPT_LABEL$BIVROST_PROMPT_END "
    if [[ "$BIVROST_PROMPT_PLACEMENT" == prefix ]]; then
        case "$PS1" in
            "$indicator"*) ;;
            *) PS1="$indicator$PS1" ;;
        esac
    else
        case "$PS1" in
            *" $indicator") ;;
            *) PS1="$PS1 $indicator" ;;
        esac
    fi
    return "$previous_status"
}
if [[ $(declare -p PROMPT_COMMAND 2>/dev/null) == 'declare -a '* ]]; then
    PROMPT_COMMAND+=(_bivrost_prompt_indicator)
else
    PROMPT_COMMAND="${PROMPT_COMMAND-}"$'\n'_bivrost_prompt_indicator
fi
fi
`

const zshEnvironmentInit = `if [[ -n "$BIVROST_ORIGINAL_ZDOTDIR" ]]; then
    export ZDOTDIR="$BIVROST_ORIGINAL_ZDOTDIR"
else
    unset ZDOTDIR
fi
if [[ -f "${ZDOTDIR:-$HOME}/.zshenv" ]]; then source "${ZDOTDIR:-$HOME}/.zshenv"; fi
export BIVROST_ORIGINAL_ZDOTDIR="${ZDOTDIR-}"
export ZDOTDIR="$BIVROST_STARTUP_DIR"
`

const zshPromptInit = `if [[ -n "$BIVROST_ORIGINAL_ZDOTDIR" ]]; then
    export ZDOTDIR="$BIVROST_ORIGINAL_ZDOTDIR"
else
    unset ZDOTDIR
fi
unset BIVROST_ORIGINAL_ZDOTDIR BIVROST_STARTUP_DIR
if [[ -f "${ZDOTDIR:-$HOME}/.zshrc" ]]; then source "${ZDOTDIR:-$HOME}/.zshrc"; fi
` + unixActivationInit + `if [[ "$BIVROST_PROMPT_ENABLED" != 0 ]]; then
_bivrost_prompt_indicator() {
    local previous_status=$?
    local indicator="$BIVROST_PROMPT_START$BIVROST_PROMPT_LABEL$BIVROST_PROMPT_END "
    if [[ "$BIVROST_PROMPT_PLACEMENT" == prefix ]]; then
        case "$PROMPT" in
            "$indicator"*) ;;
            *) PROMPT="$indicator$PROMPT" ;;
        esac
    else
        case "$PROMPT" in
            *" $indicator") ;;
            *) PROMPT="$PROMPT $indicator" ;;
        esac
    fi
    return "$previous_status"
}
autoload -Uz add-zsh-hook
add-zsh-hook precmd _bivrost_prompt_indicator
fi
`

const PowerShellPromptInit = `function global:Set-BivrostPodmanPath {
    if ($env:BIVROST_PODMAN_BIN) {
        $separator = [IO.Path]::PathSeparator
        if (($env:PATH -split [regex]::Escape([string]$separator), 2)[0] -cne $env:BIVROST_PODMAN_BIN) {
            $env:PATH = $env:BIVROST_PODMAN_BIN + $separator + $env:PATH
        }
    }
}
Set-BivrostPodmanPath
if ($env:BIVROST_EXECUTABLE -and $env:BIVROST_ACR_ENV_FILE) {
    Remove-Item Alias:bivrost -Force -ErrorAction SilentlyContinue
    function global:bivrost {
        $bivrostSwitch = $args.Count -gt 0 -and $args[0] -ceq 'switch'
        if ($bivrostSwitch -and ($MyInvocation.PipelineLength -gt 1 -or $MyInvocation.ScriptName)) {
            [Console]::Error.WriteLine('bivrost: switch must run directly in the Bivrost shell')
            $global:LASTEXITCODE = 1
            return
        }
        if ($bivrostSwitch) {
            $activeJobs = @(Get-Job -ErrorAction SilentlyContinue)
            if ($activeJobs.Count -gt 0) {
                [Console]::Error.WriteLine('bivrost: finish shell jobs and remove their records with Remove-Job before switching')
                $global:LASTEXITCODE = 1
                return
            }
            $hadSwitchAllowed = Test-Path -LiteralPath Env:BIVROST_SWITCH_ALLOWED
            $previousSwitchAllowed = $env:BIVROST_SWITCH_ALLOWED
            try {
                $env:BIVROST_SWITCH_ALLOWED = '1'
                & $env:BIVROST_EXECUTABLE @args
                $bivrostStatus = $LASTEXITCODE
            } finally {
                if ($hadSwitchAllowed) {
                    $env:BIVROST_SWITCH_ALLOWED = $previousSwitchAllowed
                } else {
                    Remove-Item -LiteralPath Env:BIVROST_SWITCH_ALLOWED -ErrorAction SilentlyContinue
                }
            }
        } else {
            & $env:BIVROST_EXECUTABLE @args
            $bivrostStatus = $LASTEXITCODE
        }
        if ($bivrostStatus -eq 0 -and $args.Count -eq 2 -and $args[0] -ceq 'acr' -and $args[1] -ceq 'enable') {
            try {
                if (-not (Test-Path -LiteralPath $env:BIVROST_ACR_ENV_FILE -PathType Leaf)) {
                    throw 'Bivrost activation settings are unavailable.'
                }
                $settingsText = (Get-Content -LiteralPath $env:BIVROST_ACR_ENV_FILE -Raw -Encoding UTF8 -ErrorAction Stop).Trim()
                if (-not $settingsText.StartsWith('{') -or -not $settingsText.EndsWith('}')) {
                    throw 'Bivrost activation settings must be a JSON object.'
                }
                $settings = $settingsText | ConvertFrom-Json -ErrorAction Stop
                $allowedKeys = @('CONTAINER_HOST', 'CONTAINER_CONNECTION', 'CONTAINER_SSHKEY', 'CONTAINERS_CONF_OVERRIDE', 'BIVROST_PODMAN_BIN')
                if ($null -eq $settings -or $settings -isnot [System.Management.Automation.PSCustomObject]) {
                    throw 'Bivrost activation settings must be a JSON object.'
                }
                foreach ($property in $settings.PSObject.Properties) {
                    if ($allowedKeys -cnotcontains $property.Name -or $property.Value -isnot [string]) {
                        throw 'Bivrost activation settings contain an invalid environment assignment.'
                    }
                }
                foreach ($key in $allowedKeys) {
                    $property = $settings.PSObject.Properties[$key]
                    if ($null -eq $property) {
                        Remove-Item -LiteralPath ('Env:' + $key) -ErrorAction SilentlyContinue
                    } else {
                        Set-Item -LiteralPath ('Env:' + $key) -Value $property.Value -ErrorAction Stop
                    }
                }
                Set-BivrostPodmanPath
            } catch {
                $global:LASTEXITCODE = 1
                throw
            }
        }
        if ($bivrostSwitch -and $bivrostStatus -eq 85) {
            exit 85
        }
        $global:LASTEXITCODE = $bivrostStatus
    }
}
if ($env:BIVROST_PROMPT_ENABLED -ne '0') {
$global:BivrostOriginalPrompt = $function:prompt
function global:prompt {
    $original = & $global:BivrostOriginalPrompt
    $indicator = $env:BIVROST_PROMPT_LABEL + ' '
    if ($Host.UI.SupportsVirtualTerminal) {
        $indicator = $env:BIVROST_PROMPT_START + $env:BIVROST_PROMPT_LABEL + $env:BIVROST_PROMPT_END + ' '
    }
    if ($env:BIVROST_PROMPT_PLACEMENT -eq 'prefix') { $indicator + ($original -join '') }
    else { ($original -join '') + ' ' + $indicator }
}
}
`

func promptColour(shell, colour string) (string, string) {
	if colour == "none" {
		return "", ""
	}
	code := map[string]string{"cyan": "36", "yellow": "33", "red": "31", "green": "32"}[colour]
	switch shell {
	case "bash":
		return `\[\e[1;` + code + `m\]`, `\[\e[0m\]`
	case "zsh":
		return "%B%F{" + colour + "}", "%f%b"
	default:
		return "\x1b[1;" + code + "m", "\x1b[0m"
	}
}

func EnvironmentValue(env []string, name string) string {
	for i := len(env) - 1; i >= 0; i-- {
		key, value, found := strings.Cut(env[i], "=")
		if found && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}
