package config

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

type PromptSettings struct {
	Enabled   bool   `json:"enabled"`
	Label     string `json:"label"`
	Colour    string `json:"colour"`
	Placement string `json:"placement"`
}

func DefaultPromptSettings() PromptSettings {
	return PromptSettings{
		Enabled:   true,
		Label:     "[Bivrost: {environment} / {cluster}]",
		Colour:    "cyan",
		Placement: "suffix",
	}
}

func SettingsPath() (string, error) {
	root, err := UserRoot()
	if err != nil {
		return "", errors.New("could not locate the Bivrost settings directory")
	}
	return filepath.Join(root, "bivrost", "settings.json"), nil
}

func InitSettings() (string, error) {
	path, err := SettingsPath()
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(struct {
		Prompt PromptSettings `json:"prompt"`
	}{DefaultPromptSettings()}, "", "  ")
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

// AuthenticationBrowser starts explicit interactive sign-in in a chosen browser
// or profile. Arguments are passed directly, never through a shell; an argument
// equal to AuthenticationURLPlaceholder receives the sign-in address, which is
// otherwise appended.
type AuthenticationBrowser struct {
	Executable string   `json:"executable"`
	Arguments  []string `json:"arguments"`
}

const AuthenticationURLPlaceholder = "{url}"

// Command returns the browser executable and arguments for one sign-in address.
func (b AuthenticationBrowser) Command(address string) (string, []string) {
	args := make([]string, 0, len(b.Arguments)+1)
	placed := false
	for _, argument := range b.Arguments {
		if argument == AuthenticationURLPlaceholder {
			argument = address
			placed = true
		}
		args = append(args, argument)
	}
	if !placed {
		args = append(args, address)
	}
	return b.Executable, args
}

// Prompt and authentication browser preferences are user-local and independent
// of connection profiles.
type userSettings struct {
	Prompt                PromptSettings
	AuthenticationBrowser *AuthenticationBrowser
}

func LoadPromptSettings() (PromptSettings, error) {
	settings, err := LoadUserSettings()
	return settings.Prompt, err
}

func LoadUserSettings() (userSettings, error) {
	defaults := userSettings{Prompt: DefaultPromptSettings()}
	path, err := SettingsPath()
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
		Prompt                json.RawMessage `json:"prompt"`
		AuthenticationBrowser json.RawMessage `json:"authentication_browser"`
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
	if err := ValidatePromptSettings(result.Prompt); err != nil {
		return defaults, err
	}
	if len(settings.AuthenticationBrowser) != 0 {
		var browser AuthenticationBrowser
		if err := decodeSettingsObject(settings.AuthenticationBrowser, &browser); err != nil {
			return defaults, err
		}
		if err := ValidateAuthenticationBrowser(browser); err != nil {
			return defaults, err
		}
		result.AuthenticationBrowser = &browser
	}

	return result, nil
}

// LoadAuthenticationBrowser returns the configured sign-in browser, or nil when
// Azure CLI should use its normal browser selection.
func LoadAuthenticationBrowser() (*AuthenticationBrowser, error) {
	settings, err := LoadUserSettings()
	return settings.AuthenticationBrowser, err
}

func ValidateAuthenticationBrowser(browser AuthenticationBrowser) error {
	if strings.TrimSpace(browser.Executable) == "" {
		return errors.New("authentication_browser executable must not be empty")
	}
	if !plainSetting(browser.Executable, 4096) {
		return errors.New("authentication_browser executable must be at most 4096 characters without control characters")
	}
	switch strings.ToLower(filepath.Ext(browser.Executable)) {
	case ".bat", ".cmd":
		// Windows runs batch files through cmd.exe, which would reinterpret the
		// sign-in address as command text.
		return errors.New("authentication_browser executable must not be a batch file")
	}
	if len(browser.Arguments) > 32 {
		return errors.New("authentication_browser supports at most 32 arguments")
	}
	placeholders := 0
	for _, argument := range browser.Arguments {
		if !plainSetting(argument, 1024) {
			return errors.New("authentication_browser arguments must be at most 1024 characters without control characters")
		}
		if argument == AuthenticationURLPlaceholder {
			placeholders++
		} else if strings.Contains(argument, AuthenticationURLPlaceholder) {
			return errors.New("authentication_browser {url} must be a whole argument")
		}
	}
	if placeholders > 1 {
		return errors.New("authentication_browser {url} may appear only once")
	}
	return nil
}

func plainSetting(value string, limit int) bool {
	if len(value) > limit {
		return false
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
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

func ValidatePromptSettings(settings PromptSettings) error {
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
