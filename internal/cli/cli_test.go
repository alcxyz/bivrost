package cli

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/alcxyz/bivrost/internal/azure"
	profile "github.com/alcxyz/bivrost/internal/config"
)

func TestParseCommandHelp(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{nil, {"help"}, {"--help"}, {"-h"}, {"ssh", "--help"}, {"acr", "--help"}, {"login", "--help"}} {
		command, err := Parse(args)
		if err != nil {
			t.Errorf("parseCommand(%q) error = %v", args, err)
			continue
		}
		if command.Kind != Help {
			t.Errorf("parseCommand(%q) kind = %v, want help", args, command.Kind)
		}
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
		if _, err := Parse(args); err == nil {
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
		if _, err := Parse([]string{"ssh", "--env", environment}); err == nil {
			t.Errorf("parseCommand() accepted unsafe environment %q", environment)
		}
	}
}

func TestResolveConfigPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	got, err := profile.ResolvePath("review-42", "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "bivrost", "environments", "review-42.json"); got != want {
		t.Fatalf("resolveConfigPath() = %q, want %q", got, want)
	}
	if got, err := profile.ResolvePath("", "configs/custom.json"); err != nil || got != "configs/custom.json" {
		t.Fatalf("resolveConfigPath() explicit = %q, %v", got, err)
	}
	if _, err := profile.ResolvePath("../prod", ""); err == nil {
		t.Fatal("resolveConfigPath() accepted path traversal")
	}
	if _, err := profile.ResolvePath("prod", "custom.json"); err == nil {
		t.Fatal("resolveConfigPath() accepted both environment and explicit path")
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	if _, err := profile.ResolvePath("review-42", ""); err == nil {
		t.Fatal("resolveConfigPath() accepted a relative XDG_CONFIG_HOME")
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
		if _, err := Parse(args); err == nil {
			t.Errorf("parseCommand(%q) accepted --no-login", args)
		}
	}
}

func TestPrivateHostOptionsAreRepeatableAndSessionScoped(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"connect", "--env", "dev", "--private-host", "one.example", "--private-host=two.example"},
		{"acr", "connect", "--env", "dev", "--private-host", "one.example", "--private-host=two.example"},
		{"switch", "--env", "dev", "--private-host", "one.example", "--private-host=two.example"},
	} {
		command, err := Parse(args)
		if err != nil {
			t.Fatalf("parseCommand(%q): %v", args, err)
		}
		if got := strings.Join(command.PrivateHosts, ","); got != "one.example,two.example" {
			t.Fatalf("parseCommand(%q) private hosts = %q", args, got)
		}
	}

	for _, args := range [][]string{
		{"ssh", "--env", "dev", "--private-host", "one.example"},
		{"doctor", "--env", "dev", "--private-host", "one.example"},
		{"acr", "proxy", "--env", "dev", "--private-host", "one.example"},
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("parseCommand(%q) accepted --private-host", args)
		}
	}
}

func TestPrivateHostOptionsRejectUnsafeTargets(t *testing.T) {
	t.Parallel()
	for _, host := range []string{
		"*.example", "https://one.example", "127.0.0.1", "one.example:443",
		"management.azure.com", "login.microsoftonline.com", "UPPER.example",
	} {
		if _, err := Parse([]string{"connect", "--env", "dev", "--private-host", host}); err == nil {
			t.Errorf("parseCommand() accepted invalid private host %q", host)
		}
	}
}

func TestParseLoginAndAzureArguments(t *testing.T) {
	t.Parallel()
	command, err := Parse([]string{"login", "--tenant", "tenant.example"})
	if err != nil {
		t.Fatal(err)
	}
	if command.Kind != Login || command.Tenant != "tenant.example" {
		t.Fatalf("parseCommand(login) = %+v", command)
	}
	if got, want := strings.Join(azure.LoginArguments(command.Tenant), " "), "login --output none --tenant tenant.example"; got != want {
		t.Fatalf("azure.LoginArguments() = %q, want %q", got, want)
	}
	if got := strings.Join(azure.LoginArguments(""), " "); got != "login --output none" {
		t.Fatalf("azure.LoginArguments(empty) = %q", got)
	}
	for _, disallowed := range []string{"--use-device-code", "--env", "--config", "--no-login"} {
		if strings.Contains(strings.Join(azure.LoginArguments(command.Tenant), " "), disallowed) {
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
		if _, err := Parse(args); err == nil {
			t.Errorf("parseCommand(%q) error = nil", args)
		}
	}
}

func TestCommandAndFlagShortcuts(t *testing.T) {
	pairs := [][2][]string{
		{{"v"}, {"version"}}, {{"-v"}, {"version"}}, {{"--version"}, {"version"}},
		{{"list"}, {"environments"}}, {{"env"}, {"environments"}}, {{"envs"}, {"environments"}},
		{{"connect", "-e", "staging"}, {"connect", "--env", "staging"}},
		{{"ssh", "-e=development"}, {"ssh", "--env=development"}},
		{{"connect", "-c", "custom.json"}, {"connect", "--config", "custom.json"}},
		{{"login", "-t", "tenant"}, {"login", "--tenant", "tenant"}},
		{{"acr", "connect", "-e", "staging", "-n"}, {"acr", "connect", "--env", "staging", "--no-login"}},
	}
	for _, pair := range pairs {
		short, err := Parse(pair[0])
		if err != nil {
			t.Fatalf("%q: %v", pair[0], err)
		}
		long, err := Parse(pair[1])
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(short, long) {
			t.Fatalf("%q parsed differently from %q", pair[0], pair[1])
		}
	}
	for _, args := range [][]string{
		{"connect", "-e", "staging", "--config", "file.json"},
		{"connect", "--env", "staging", "-c", "file.json"},
		{"connect", "-e", ""}, {"connect", "-c", ""}, {"login", "-t", ""},
		{"connect", "-e", "staging", "-n"}, {"-v", "extra"}, {"env", "extra"},
	} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted invalid shortcuts %q", args)
		}
	}
}

func TestParseSubscriptionDiscoveryDoesNotChangeListAliases(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{{"list"}, {"environments"}, {"env"}, {"envs"}} {
		command, err := Parse(args)
		if err != nil || command.Kind != Environments {
			t.Errorf("Parse(%q) = %+v, %v; want environments", args, command, err)
		}
	}
	for _, args := range [][]string{{"list", "subscriptions"}, {"list", "subscriptions", "--refresh"}} {
		command, err := Parse(args)
		if err != nil || command.Kind != Subscriptions || command.Refresh != (len(args) == 3) {
			t.Errorf("Parse(%q) = %+v, %v", args, command, err)
		}
	}
	for _, args := range [][]string{
		{"environments", "subscriptions"}, {"env", "subscriptions"}, {"envs", "subscriptions"},
		{"list", "subscriptions", "extra"}, {"list", "subscriptions", "--unknown"},
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%q) accepted invalid subscription discovery syntax", args)
		}
	}
}

func TestParseTerraformDoctorRequiresExplicitValidatedTarget(t *testing.T) {
	t.Parallel()
	command, err := Parse([]string{
		"doctor", "terraform",
		"--subscription", "11111111-1111-4111-8111-111111111111",
		"--account", "examplestate",
		"--container", "tfstate-prod",
		"--debug",
	})
	if err != nil {
		t.Fatal(err)
	}
	if command.Kind != TerraformDoctor || command.Subscription == "" || command.Account != "examplestate" || command.Container != "tfstate-prod" || !command.Debug {
		t.Fatalf("Parse(doctor terraform) = %+v", command)
	}
	for _, args := range [][]string{
		{"doctor", "terraform"},
		{"doctor", "terraform", "--subscription", "sub", "--account", "Example", "--container", "state"},
		{"doctor", "terraform", "--subscription", "sub", "--account", "example", "--container", "bad--name"},
		{"doctor", "terraform", "--subscription", "-other", "--account", "example", "--container", "state"},
		{"doctor", "terraform", "--subscription", "sub", "--account", "example", "--container", "state", "extra"},
		{"doctor", "terraform", "--subscription", "sub", "--account", "example", "--container", "state", "--env", "dev"},
	} {
		if _, err := Parse(args); err == nil {
			t.Errorf("Parse(%q) accepted an incomplete or unsafe target", args)
		}
	}
}

func TestDebugFlagIsOptInAcrossOperationalCommands(t *testing.T) {
	for _, args := range [][]string{
		{"connect", "-e", "staging"}, {"ssh", "-e", "development"}, {"login"},
		{"acr", "proxy", "-e", "staging"}, {"acr", "connect", "-e", "staging"},
		{"acr", "login", "-e", "staging"}, {"acr", "doctor", "-e", "staging"},
	} {
		plain, err := Parse(args)
		if err != nil || plain.Debug {
			t.Fatalf("default debug for %q: %v", args, err)
		}
		for _, flag := range []string{"-d", "--debug"} {
			withFlag := append(append([]string(nil), args...), flag)
			parsed, err := Parse(withFlag)
			if err != nil || !parsed.Debug {
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
			if _, err := Parse(args); err == nil || !strings.Contains(err.Error(), "flag provided but not defined: -engine") {
				t.Errorf("parseCommand(%q) error = %v; want unsupported flag", args, err)
			}
		}
	}
}

func TestSwitchCommandOptions(t *testing.T) {
	for _, args := range [][]string{{"switch", "-e", "example"}, {"switch", "-c", "profile.json", "--acr"}} {
		cmd, err := Parse(args)
		if err != nil || cmd.Kind != Switch {
			t.Fatalf("%v: %+v, %v", args, cmd, err)
		}
		if args[2] == "profile.json" && !cmd.ACR {
			t.Fatal("switch lost explicit ACR option")
		}
	}
	for _, args := range [][]string{{"switch"}, {"switch", "-e", "example", "--no-login"}, {"switch", "-e", "example", "-c", "profile.json"}, {"switch", "-e", "example", "extra"}} {
		if _, err := Parse(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestParseCommandProfiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args        []string
		wantKind    Kind
		wantEnv     string
		wantConfig  string
		wantNoLogin bool
	}{
		{args: []string{"connect", "--env", "staging"}, wantKind: Connect, wantEnv: "staging"},
		{args: []string{"ssh", "--env", "staging"}, wantKind: SSH, wantEnv: "staging"},
		{args: []string{"ssh", "--config", "/tmp/custom.json"}, wantKind: SSH, wantConfig: "/tmp/custom.json"},
		{args: []string{"acr", "proxy", "--env", "prod"}, wantKind: ACRProxy, wantEnv: "prod"},
		{args: []string{"acr", "connect", "--env", "review-42", "--no-login"}, wantKind: ACRConnect, wantEnv: "review-42", wantNoLogin: true},
		{args: []string{"acr", "login", "--config", "relative.json"}, wantKind: ACRLogin, wantConfig: "relative.json"},
		{args: []string{"acr", "doctor", "--env=dev1"}, wantKind: ACRDoctor, wantEnv: "dev1"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tt.args)
			if err != nil {
				t.Fatalf("parseCommand() error = %v", err)
			}
			if got.Kind != tt.wantKind || got.Environment != tt.wantEnv || got.ConfigPath != tt.wantConfig || got.NoLogin != tt.wantNoLogin {
				t.Fatalf("parseCommand() = %+v, want kind %v, env %q, config %q, no-login %v", got, tt.wantKind, tt.wantEnv, tt.wantConfig, tt.wantNoLogin)
			}
		})
	}
}
