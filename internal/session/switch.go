package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/alcxyz/bivrost/internal/cli"
	profile "github.com/alcxyz/bivrost/internal/config"
)

const (
	SwitchShellExitCode   = 85
	maxSwitchRequestBytes = 4096
)

var ErrSwitchAccepted = errors.New("session switch accepted")

type switchRequest struct {
	Environment  string   `json:"Environment"`
	ConfigPath   string   `json:"ConfigPath"`
	PrivateHosts []string `json:"PrivateHosts,omitempty"`
	ACR          bool     `json:"ACR"`
}

type switchReconnectError struct {
	config    profile.Profile
	published bool
}

func (e *switchReconnectError) Error() string {
	return "reconnect the platform session to the accepted target"
}

func runSwitch(ctx context.Context, command cli.Command) error {
	if os.Getenv("BIVROST_SWITCH_ALLOWED") != "1" || os.Getenv("BIVROST_SESSION") == "" {
		return errors.New("run bivrost switch inside an active Bivrost Bash, Zsh, or PowerShell session")
	}

	request := switchRequest{Environment: command.Environment, PrivateHosts: append([]string(nil), command.PrivateHosts...), ACR: command.ACR}
	if command.ConfigPath != "" {
		path, err := filepath.Abs(command.ConfigPath)
		if err != nil {
			return errors.New("cannot resolve the switch configuration path")
		}
		request.ConfigPath = path
	}
	body, err := json.Marshal(request)
	if err != nil {
		return errors.New("cannot prepare the session switch")
	}
	response, err := sessionRequestBody(ctx, "/switch", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer response.Body.Close()
	responseBody, readErr := io.ReadAll(io.LimitReader(response.Body, 4097))
	if readErr != nil {
		return errors.New("session switch response was interrupted; the current shell remains active")
	}
	if response.StatusCode != http.StatusAccepted {
		message := string(bytes.TrimSpace(responseBody))
		if message == "" {
			message = "session switch was rejected"
		}
		return errors.New(message)
	}

	fmt.Println("Switch accepted. Closing this shell and reconnecting...")
	return ErrSwitchAccepted
}

func loadSwitchTarget(request switchRequest) (profile.Profile, error) {
	if (request.Environment == "") == (request.ConfigPath == "") {
		return profile.Profile{}, errors.New("switch requires exactly one environment or configuration path")
	}
	if request.Environment != "" && !profile.ValidEnvironmentName(request.Environment) {
		return profile.Profile{}, errors.New("invalid switch environment")
	}
	if request.ConfigPath != "" && !filepath.IsAbs(request.ConfigPath) {
		return profile.Profile{}, errors.New("switch configuration path must be absolute")
	}

	c, err := profile.LoadEnvironment(request.Environment, request.ConfigPath)
	if err != nil {
		return profile.Profile{}, err
	}
	c, err = withPrivateHosts(c, request.PrivateHosts)
	if err != nil {
		return profile.Profile{}, err
	}
	if c.Environment == "" {
		c.Environment = "custom-profile"
	}
	c.ACRSession = request.ACR
	c.SkipRegistryLogin = false
	if err := c.ValidatePlatform(); err != nil {
		return profile.Profile{}, err
	}
	if request.ACR {
		if err := c.ValidateACR(); err != nil {
			return profile.Profile{}, err
		}
		if !profile.ValidResourceName(c.RegistrySubscription) {
			return profile.Profile{}, errors.New("set registry_subscription to the subscription containing ACR")
		}
	}
	return c, nil
}

func decodeSwitchRequest(r io.Reader) (switchRequest, error) {
	var request switchRequest
	data, err := io.ReadAll(io.LimitReader(r, maxSwitchRequestBytes+1))
	if err != nil {
		return switchRequest{}, errors.New("could not read switch request")
	}
	if len(data) > maxSwitchRequestBytes {
		return switchRequest{}, errors.New("switch request is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil {
		return switchRequest{}, errors.New("switch request must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return switchRequest{}, errors.New("switch request must contain exactly one JSON object")
	}
	return request, nil
}

func isSwitchShellExit(err error) bool {
	var exitError interface{ ExitCode() int }
	return errors.As(err, &exitError) && exitError.ExitCode() == SwitchShellExitCode
}
