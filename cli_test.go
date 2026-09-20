package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alcxyz/bivrost/internal/azure"
)

func TestParseCommandHelp(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}, {"ssh", "--help"}, {"acr", "--help"}, {"login", "--help"}} {
		command, err := parseCommand(args)
		if err != nil {
			t.Errorf("parseCommand(%q) error = %v", args, err)
			continue
		}
		if command.kind != commandHelp {
			t.Errorf("parseCommand(%q) kind = %v, want help", args, command.kind)
		}
	}
}

func TestParseCommandProfiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args        []string
		wantKind    commandKind
		wantEnv     string
		wantConfig  string
		wantNoLogin bool
	}{
		{args: []string{"connect", "--env", "staging"}, wantKind: commandConnect, wantEnv: "staging"},
		{args: []string{"ssh", "--env", "staging"}, wantKind: commandSSH, wantEnv: "staging"},
		{args: []string{"ssh", "--config", "/tmp/custom.json"}, wantKind: commandSSH, wantConfig: "/tmp/custom.json"},
		{args: []string{"acr", "proxy", "--env", "prod"}, wantKind: commandACRProxy, wantEnv: "prod"},
		{args: []string{"acr", "connect", "--env", "review-42", "--no-login"}, wantKind: commandACRConnect, wantEnv: "review-42", wantNoLogin: true},
		{args: []string{"acr", "login", "--config", "relative.json"}, wantKind: commandACRLogin, wantConfig: "relative.json"},
		{args: []string{"acr", "doctor", "--env=dev1"}, wantKind: commandACRDoctor, wantEnv: "dev1"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()
			got, err := parseCommand(tt.args)
			if err != nil {
				t.Fatalf("parseCommand() error = %v", err)
			}
			if got.kind != tt.wantKind || got.environment != tt.wantEnv || got.configPath != tt.wantConfig || got.noLogin != tt.wantNoLogin {
				t.Fatalf("parseCommand() = %+v, want kind %v, env %q, config %q, no-login %v", got, tt.wantKind, tt.wantEnv, tt.wantConfig, tt.wantNoLogin)
			}
		})
	}
}

func TestParseCommandRejectsMissingOrConflictingProfile(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"ssh"},
		{"connect"},
		{"acr", "proxy"},
		{"acr", "connect", "--env", "staging", "--config", "custom.json"},
		{"ssh", "--config="},
	} {
		if _, err := parseCommand(args); err == nil {
			t.Errorf("parseCommand(%q) error = nil", args)
		}
	}
}

func TestParseCommandRejectsUnsafeEnvironmentNames(t *testing.T) {
	t.Parallel()
	invalid := []string{
		"", ".", "..", "../prod", "prod/test", "Prod", "-prod", "prod_1", "prod.json",
		"a" + strings.Repeat("b", 63),
	}
	for _, environment := range invalid {
		if _, err := parseCommand([]string{"ssh", "--env", environment}); err == nil {
			t.Errorf("parseCommand() accepted unsafe environment %q", environment)
		}
	}
}

func TestResolveConfigPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	got, err := resolveConfigPath("review-42", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "bivrost", "environments", "review-42.json"); got != want {
		t.Fatalf("resolveConfigPath() = %q, want %q", got, want)
	}
	if got, err := resolveConfigPath("", "configs/custom.json"); err != nil || got != "configs/custom.json" {
		t.Fatalf("resolveConfigPath() explicit = %q, %v", got, err)
	}
	if _, err := resolveConfigPath("../prod", ""); err == nil {
		t.Fatal("resolveConfigPath() accepted path traversal")
	}
	if _, err := resolveConfigPath("prod", "custom.json"); err == nil {
		t.Fatal("resolveConfigPath() accepted both environment and explicit path")
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if _, err := resolveConfigPath("review-42", ""); err == nil {
		t.Fatal("resolveConfigPath() accepted a relative XDG_CONFIG_HOME")
	}
}

func TestRunValidatesConfigurationBeforeDispatch(t *testing.T) {
	t.Parallel()
	invalidACR := filepath.Join(t.TempDir(), "invalid-acr.json")
	if err := os.WriteFile(invalidACR, []byte(`{"registry":"not.valid"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, subcommand := range []string{"proxy", "connect", "login", "doctor"} {
		err := run([]string{"acr", subcommand, "--config", invalidACR})
		if err == nil || !strings.Contains(err.Error(), "registry must be") {
			t.Errorf("run(acr %s) error = %v, want ACR validation error", subcommand, err)
		}
	}

	invalidConnection := filepath.Join(t.TempDir(), "invalid-connection.json")
	if err := os.WriteFile(invalidConnection, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"ssh", "--config", invalidConnection}); err == nil || !strings.Contains(err.Error(), "fill in subscription") {
		t.Fatalf("run(ssh) error = %v, want connection validation error", err)
	}
}

func TestNoLoginOnlyAcceptedByACRConnect(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"ssh", "--env", "dev", "--no-login"},
		{"acr", "proxy", "--env", "dev", "--no-login"},
		{"acr", "login", "--env", "dev", "--no-login"},
		{"acr", "doctor", "--env", "dev", "--no-login"},
		{"login", "--no-login"},
	} {
		if _, err := parseCommand(args); err == nil {
			t.Errorf("parseCommand(%q) accepted --no-login", args)
		}
	}
}

func TestParseLoginAndAzureArguments(t *testing.T) {
	t.Parallel()
	command, err := parseCommand([]string{"login", "--tenant", "tenant.example"})
	if err != nil {
		t.Fatal(err)
	}
	if command.kind != commandLogin || command.tenant != "tenant.example" {
		t.Fatalf("parseCommand(login) = %+v", command)
	}
	if got, want := strings.Join(azure.LoginArguments(command.tenant), " "), "login --output none --tenant tenant.example"; got != want {
		t.Fatalf("azure.LoginArguments() = %q, want %q", got, want)
	}
	if got := strings.Join(azure.LoginArguments(""), " "); got != "login --output none" {
		t.Fatalf("azure.LoginArguments(empty) = %q", got)
	}
	for _, disallowed := range []string{"--use-device-code", "--env", "--config", "--no-login"} {
		if strings.Contains(strings.Join(azure.LoginArguments(command.tenant), " "), disallowed) {
			t.Errorf("Azure login arguments contain %s", disallowed)
		}
	}
}

func TestParseCommandRejectsUnknownInputBeforeDispatch(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"unknown"},
		{"acr", "unknown", "--env", "dev"},
		{"ssh", "--env", "dev", "extra"},
		{"login", "unexpected"},
		{"login", "--use-device-code"},
		{"login", "--tenant="},
	} {
		if _, err := parseCommand(args); err == nil {
			t.Errorf("parseCommand(%q) error = nil", args)
		}
	}
}

func TestCommandAndFlagShortcuts(t *testing.T) {
	pairs := [][2][]string{
		{{"v"}, {"version"}}, {{"-v"}, {"version"}}, {{"--version"}, {"version"}},
		{{"env"}, {"environments"}}, {{"envs"}, {"environments"}},
		{{"connect", "-e", "staging"}, {"connect", "--env", "staging"}},
		{{"ssh", "-e=development"}, {"ssh", "--env=development"}},
		{{"connect", "-c", "custom.json"}, {"connect", "--config", "custom.json"}},
		{{"login", "-t", "tenant"}, {"login", "--tenant", "tenant"}},
		{{"acr", "connect", "-e", "staging", "-n"}, {"acr", "connect", "--env", "staging", "--no-login"}},
	}
	for _, pair := range pairs {
		short, err := parseCommand(pair[0])
		if err != nil {
			t.Fatalf("%q: %v", pair[0], err)
		}
		long, err := parseCommand(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if short != long {
			t.Fatalf("%q parsed differently from %q", pair[0], pair[1])
		}
	}
	for _, args := range [][]string{
		{"connect", "-e", "staging", "--config", "file.json"},
		{"connect", "--env", "staging", "-c", "file.json"},
		{"connect", "-e", ""}, {"connect", "-c", ""}, {"login", "-t", ""},
		{"connect", "-e", "staging", "-n"}, {"-v", "extra"}, {"env", "extra"},
	} {
		if _, err := parseCommand(args); err == nil {
			t.Fatalf("accepted invalid shortcuts %q", args)
		}
	}
}

func TestDebugFlagIsOptInAcrossOperationalCommands(t *testing.T) {
	for _, args := range [][]string{
		{"connect", "-e", "staging"}, {"ssh", "-e", "development"}, {"login"},
		{"acr", "proxy", "-e", "staging"}, {"acr", "connect", "-e", "staging"},
		{"acr", "login", "-e", "staging"}, {"acr", "doctor", "-e", "staging"},
	} {
		plain, err := parseCommand(args)
		if err != nil || plain.debug {
			t.Fatalf("default debug for %q: %v", args, err)
		}
		for _, flag := range []string{"-d", "--debug"} {
			withFlag := append(append([]string(nil), args...), flag)
			parsed, err := parseCommand(withFlag)
			if err != nil || !parsed.debug {
				t.Fatalf("%q did not enable logging: %v", withFlag, err)
			}
		}
	}
}

func TestParseCommandRejectsRemovedEngineFlag(t *testing.T) {
	t.Parallel()
	for _, command := range [][]string{
		{"doctor"}, {"connect"}, {"ssh"}, {"acr", "proxy"},
		{"acr", "connect"}, {"acr", "login"}, {"acr", "doctor"},
	} {
		for _, engine := range []string{"docker", "podman", "containerd", ""} {
			args := append(append([]string(nil), command...), "--env", "staging", "--engine="+engine)
			if _, err := parseCommand(args); err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -engine") {
				t.Errorf("parseCommand(%q) error = %v; want unsupported flag", args, err)
			}
		}
	}
}
