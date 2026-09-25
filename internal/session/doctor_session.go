package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/alcxyz/bivrost/internal/cli"
	profile "github.com/alcxyz/bivrost/internal/config"
)

// Status comes from the authenticated connection owner, not a mutable catalogue
// or a shell badge. It contains connection metadata and no authentication tokens.
type doctorSessionStatus struct {
	KubernetesUnavailable bool
	Kubeconfig            string
	Config                profile.Profile
	ProfileEnvironment    string
	Enabled               bool
	LoginRefreshed        bool
	Machine               bool
	Environment           map[string]string
}

func currentDoctorSession(ctx context.Context) (*doctorSessionStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	response, err := sessionRequest(ctx, "/status")
	if err != nil {
		return nil, errors.New("active session status is unavailable; reconnect or supply --env or --config")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("active session status is unavailable; reconnect or supply --env or --config")
	}
	var status doctorSessionStatus
	if err = json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(&status); err != nil {
		return nil, errors.New("invalid session status; reconnect")
	}
	if err = status.Config.ValidatePlatform(); err != nil {
		return nil, errors.New("invalid session configuration; reconnect")
	}
	return &status, nil
}

func runDoctor(ctx context.Context, command cli.Command, out io.Writer) error {
	if _, err := profile.LoadUserSettings(); err != nil {
		return err
	}
	implicit := command.Environment == "" && command.ConfigPath == ""
	var status *doctorSessionStatus
	if os.Getenv("BIVROST_SESSION") != "" && os.Getenv("BIVROST_CONTROL_FILE") != "" {
		var err error
		status, err = currentDoctorSession(ctx)
		if err != nil && implicit {
			return err
		}
	}
	var c profile.Profile
	if implicit {
		if status == nil {
			return errors.New("doctor requires --env NAME or --config PATH outside a supported active Bivrost session")
		}
		c = status.Config
	} else {
		var err error
		c, err = profile.LoadEnvironment(command.Environment, command.ConfigPath)
		if err != nil {
			return err
		}
		if status != nil {
			selected, _ := json.Marshal(c)
			active, _ := json.Marshal(status.Config)
			if !bytes.Equal(selected, active) {
				status = nil
			}
		}
	}
	c.SkipPullProbe = command.NoPull
	return platformDoctorWithSession(ctx, c, out, status)
}

func doctorSessionEnvironment(c profile.Profile, status *doctorSessionStatus) bool {
	for _, key := range []string{"CONTAINER_HOST", "CONTAINER_CONNECTION", "CONTAINER_SSHKEY", "CONTAINERS_CONF_OVERRIDE"} {
		if os.Getenv(key) != status.Environment[key] {
			return false
		}
	}
	if wrapper := status.Environment["BIVROST_PODMAN_BIN"]; wrapper != "" {
		executable, err := exec.LookPath("podman")
		if err != nil || filepath.Clean(filepath.Dir(executable)) != filepath.Clean(wrapper) || os.Getenv("BIVROST_PODMAN_BIN") != wrapper {
			return false
		}
	}
	if status.Environment["CONTAINERS_CONF_OVERRIDE"] == "" {
		return false
	}
	if _, err := os.Stat(status.Environment["CONTAINERS_CONF_OVERRIDE"]); err != nil {
		return false
	}
	// Uppercase settings are emitted by Bivrost; a lowercase override must agree.
	for key, want := range map[string]string{"HTTPS_PROXY": c.ProxyURL(), "NO_PROXY": "127.0.0.1,localhost", "ALL_PROXY": ""} {
		if os.Getenv(key) != want {
			return false
		}
		if value := os.Getenv(strings.ToLower(key)); value != "" && value != want {
			return false
		}
	}
	return true
}

func doctorSessionEngine(ctx context.Context, status *doctorSessionStatus) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "podman", "info", "--format", "{{.Host.ServiceIsRemote}}")
	output, err := cmd.Output()
	expected := "false"
	if status.Machine {
		expected = "true"
	}
	return err == nil && strings.TrimSpace(string(output)) == expected
}
