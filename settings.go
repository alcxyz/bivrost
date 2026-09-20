package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxSettingsBytes = 64 * 1024

type promptSettings struct {
	Enabled   bool   `json:"enabled"`
	Label     string `json:"label"`
	Colour    string `json:"colour"`
	Placement string `json:"placement"`
}

func defaultPromptSettings() promptSettings {
	return promptSettings{
		Enabled:   true,
		Label:     "[Bivrost: {environment} / {cluster}]",
		Colour:    "cyan",
		Placement: "suffix",
	}
}

func settingsPath() (string, error) {
	root, err := userConfigRoot()
	if err != nil {
		return "", errors.New("could not locate the Bivrost settings directory")
	}
	return filepath.Join(root, "bivrost", "settings.json"), nil
}

func initSettings() (string, error) {
	path, err := settingsPath()
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(struct {
		Prompt promptSettings `json:"prompt"`
	}{defaultPromptSettings()}, "", "  ")
	if err != nil {
		return "", errors.New("could not serialize default settings")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", errors.New("could not create the Bivrost settings directory")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return "", errors.New("Bivrost settings.json already exists; leaving it unchanged")
	}
	if err != nil {
		return "", errors.New("could not create Bivrost settings.json")
	}
	_, writeErr := file.Write(append(data, '\n'))
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return "", errors.New("could not finish writing Bivrost settings.json")
	}
	return path, nil
}

// Prompt preferences are user-local and independent of connection profiles.
type userSettings struct {
	Prompt promptSettings
}

func loadPromptSettings() (promptSettings, error) {
	settings, err := loadUserSettings()
	return settings.Prompt, err
}

func loadUserSettings() (userSettings, error) {
	defaults := userSettings{Prompt: defaultPromptSettings()}
	path, err := settingsPath()
	if err != nil {
		return defaults, err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return defaults, nil
	}
	if err != nil {
		return defaults, errors.New("could not open Bivrost settings.json")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSettingsBytes+1))
	if err != nil {
		return defaults, errors.New("could not read Bivrost settings.json")
	}
	if len(data) > maxSettingsBytes {
		return defaults, errors.New("Bivrost settings.json exceeds 64 KiB")
	}
	var settings struct {
		Prompt json.RawMessage `json:"prompt"`
	}
	if err := decodeSettingsObject(data, &settings); err != nil {
		return defaults, err
	}
	result := defaults
	if len(settings.Prompt) != 0 {
		if err := decodeSettingsObject(settings.Prompt, &result.Prompt); err != nil {
			return defaults, err
		}
	}
	if err := validatePromptSettings(result.Prompt); err != nil {
		return defaults, err
	}

	return result, nil
}

func decodeSettingsObject(data []byte, target any) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return errors.New("Bivrost settings.json must contain settings objects")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		if err.Error() == `json: unknown field "container_engine"` {
			return errors.New("container_engine is no longer supported; Bivrost uses Podman only; remove container_engine from Bivrost settings.json")
		}
		return errors.New("Bivrost settings.json contains invalid or unsupported settings")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("Bivrost settings.json must contain exactly one JSON object")
	}
	return nil
}

func validatePromptSettings(settings promptSettings) error {
	switch settings.Colour {
	case "cyan", "yellow", "red", "green", "none":
	default:
		return errors.New("prompt colour must be cyan, yellow, red, green, or none")
	}
	switch settings.Placement {
	case "suffix", "prefix":
	default:
		return errors.New("prompt placement must be suffix or prefix")
	}
	if strings.TrimSpace(settings.Label) == "" {
		return errors.New("prompt label must not be empty")
	}
	for _, r := range settings.Label {
		if r < 32 || r > 126 || strings.ContainsRune("$`\\%!", r) {
			return errors.New("prompt label must use printable ASCII without $, backticks, backslashes, %, or !")
		}
	}
	literal := strings.NewReplacer("{environment}", "", "{cluster}", "").Replace(settings.Label)
	if strings.ContainsAny(literal, "{}") {
		return errors.New("prompt label supports only {environment} and {cluster} placeholders")
	}
	return nil
}
