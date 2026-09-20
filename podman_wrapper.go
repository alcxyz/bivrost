package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const podmanWrapperMetadata = "bivrost-podman.json"

type podmanWrapperConfig struct {
	Executable string `json:"executable"`
}

// preparePodmanWrapper must run before the session prepends its bin directory to
// PATH, so the saved executable is the user's actual Podman installation.
func preparePodmanWrapper(directory string) (string, error) {
	real, err := exec.LookPath("podman")
	if err != nil {
		return "", fmt.Errorf("find Podman for session wrapper: %w", err)
	}
	real, err = filepath.Abs(real)
	if err != nil {
		return "", err
	}
	source, err := os.Executable()
	if err != nil {
		return "", err
	}
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	realInfo, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if os.SameFile(sourceInfo, realInfo) {
		return "", fmt.Errorf("Podman resolves to Bivrost itself")
	}
	bin := filepath.Join(directory, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		return "", err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(bin)
		}
	}()
	name := "podman"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	in, err := os.Open(source)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(filepath.Join(bin, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0700)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	data, err := json.Marshal(podmanWrapperConfig{Executable: real})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(bin, podmanWrapperMetadata), data, 0600); err != nil {
		return "", err
	}
	complete = true
	return bin, nil
}

func runPodmanWrapper() int {
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "bivrost: cannot locate Podman session wrapper")
		return 125
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(self), podmanWrapperMetadata))
	var config podmanWrapperConfig
	if err != nil || json.Unmarshal(data, &config) != nil || !filepath.IsAbs(config.Executable) || config.Executable == self {
		fmt.Fprintln(os.Stderr, "bivrost: invalid Podman session wrapper configuration")
		return 125
	}
	selfInfo, selfErr := os.Stat(self)
	targetInfo, targetErr := os.Stat(config.Executable)
	if selfErr != nil || targetErr != nil || os.SameFile(selfInfo, targetInfo) {
		fmt.Fprintln(os.Stderr, "bivrost: invalid Podman session wrapper executable")
		return 125
	}
	return executeWrappedPodman(config.Executable, podmanWrapperArgs(os.Args[1:]))
}

// Podman accepts repeated boolean options, with the last value taking effect.
// Adding our default before user arguments therefore preserves explicit choices
// without guessing whether a flag-looking argument is another option's value.
func podmanWrapperArgs(args []string) []string {
	command := podmanBuildCommand(args)
	if command < 0 {
		return args
	}
	result := make([]string, 0, len(args)+1)
	result = append(result, args[:command+1]...)
	result = append(result, "--http-proxy=false")
	return append(result, args[command+1:]...)
}

func podmanBuildCommand(args []string) int {
	group := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "build" {
			return i
		}
		if (arg == "image" || arg == "buildx") && !group {
			group = true
			continue
		}
		if !strings.HasPrefix(arg, "-") || arg == "--" {
			return -1
		}
		name, _, hasValue := strings.Cut(arg, "=")
		switch name {
		case "--remote", "-r", "--syslog", "--transient-store":
			continue
		case "--cdi-spec-dir", "--cgroup-manager", "--config", "--conmon", "--connection", "-c", "--events-backend", "--hooks-dir", "--identity", "--imagestore", "--log-level", "--module", "--network-cmd-path", "--network-config-dir", "--out", "--root", "--runroot", "--runtime", "--runtime-flag", "--ssh", "--storage-driver", "--storage-opt", "--tls-ca", "--tls-cert", "--tls-key", "--tmpdir", "--url", "--volumepath":
			if !hasValue {
				i++
				if i >= len(args) {
					return -1
				}
			}
		default:
			// -cNAME is the attached form of the global --connection option.
			if strings.HasPrefix(arg, "-c") && !strings.HasPrefix(arg, "--") && len(arg) > 2 {
				continue
			}
			// Unknown flags may consume a following command-looking value. Let Podman
			// interpret them unchanged instead of accidentally rewriting another command.
			return -1
		}
	}
	return -1
}
