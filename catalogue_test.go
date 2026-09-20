package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func isolateCatalogueProfiles(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("HOME", root)
	t.Setenv("AppData", root)
	t.Setenv(catalogueFileEnvironment, "")
}

func catalogueTestConfig(requiresPIM bool) config {
	return config{
		RequiresPIM:          requiresPIM,
		Registry:             "exampleregistry",
		RegistrySubscription: "registry-subscription",
		Subscription:         "connection-subscription",
		BastionName:          "example-bastion",
		BastionResourceGroup: "example-network",
		VMResourceID:         "/subscriptions/example-subscription/resourceGroups/example-network/providers/Microsoft.Compute/virtualMachines/example-host",
		ProxyPort:            28080,
		SOCKSPort:            28081,
	}
}

func writeCatalogue(t *testing.T, catalogue map[string]config) string {
	t.Helper()
	data, err := json.Marshal(catalogue)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalogue.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(catalogueFileEnvironment, path)
	return path
}

func writeLocalEnvironment(t *testing.T, name string, c config) string {
	t.Helper()
	path, err := resolveConfigPath(name, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestNoBuiltInEnvironments(t *testing.T) {
	isolateCatalogueProfiles(t)
	if _, err := loadEnvironment("example", ""); err == nil {
		t.Fatal("environment unexpectedly existed without a catalogue or local profile")
	}
	var output bytes.Buffer
	if err := listEnvironments(&output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "example") {
		t.Fatalf("listing contains an unexpected built-in profile: %s", output.String())
	}
}

func TestEnvironmentSelectionUsesExternalCatalogueAndLocalOverride(t *testing.T) {
	isolateCatalogueProfiles(t)
	catalogueProfile := catalogueTestConfig(true)
	writeCatalogue(t, map[string]config{
		"example": catalogueProfile,
		"prod":    catalogueTestConfig(false),
	})

	c, err := loadEnvironment("example", "")
	if err != nil || c.Environment != "example" || !c.RequiresPIM {
		t.Fatalf("catalogue selection = %+v, %v", c, err)
	}
	prod, err := loadEnvironment("prod", "")
	if err != nil || prod.RequiresPIM {
		t.Fatalf("environment name changed explicit requires_pim: %+v, %v", prod, err)
	}

	localProfile := catalogueTestConfig(false)
	localProfile.Registry = "localregistry"
	path := writeLocalEnvironment(t, "example", localProfile)
	c, err = loadEnvironment("example", "")
	if err != nil || c.Registry != "localregistry" || c.RequiresPIM {
		t.Fatalf("local override should replace the catalogue profile: %+v, %v", c, err)
	}
	if err := os.WriteFile(path, []byte(`invalid`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEnvironment("example", ""); err == nil {
		t.Fatal("invalid override silently fell back")
	}
	if _, err := loadEnvironment("example", filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("accepted conflicting selectors")
	}
	if _, err := loadEnvironment("", filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing explicit config silently fell back")
	}
	if _, err := loadEnvironment("unknown", ""); err == nil || !strings.Contains(err.Error(), "bivrost list") {
		t.Fatal("unknown environment lacks guidance")
	}
}

func TestListEnvironmentsUsesSortedUnion(t *testing.T) {
	isolateCatalogueProfiles(t)
	writeCatalogue(t, map[string]config{
		"middle": catalogueTestConfig(true),
		"zulu":   catalogueTestConfig(false),
	})
	alphaProfile := catalogueTestConfig(true)
	alphaProfile.Registry = ""
	alphaProfile.RegistrySubscription = ""
	alphaProfile.AKS = &aksConfig{Name: "example-cluster", ResourceGroup: "example-group", Subscription: "example-subscription"}
	writeLocalEnvironment(t, "alpha", alphaProfile)
	writeLocalEnvironment(t, "middle", catalogueTestConfig(false))
	t.Setenv("PATH", t.TempDir())

	var output bytes.Buffer
	if err := listEnvironments(&output); err != nil {
		t.Fatal(err)
	}
	text := output.String()
	alpha := strings.Index(text, "alpha")
	middle := strings.Index(text, "middle")
	zulu := strings.Index(text, "zulu")
	if alpha < 0 || middle <= alpha || zulu <= middle {
		t.Fatalf("environment list is missing entries or unsorted:\n%s", text)
	}
	rows := map[string][]string{}
	for _, line := range strings.Split(text, "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			rows[fields[0]] = fields[1:]
		}
	}
	for name, want := range map[string]string{
		"alpha":  "local configured - required",
		"middle": "local override - configured not required",
		"zulu":   "catalogue - configured not required",
	} {
		if got := strings.Join(rows[name], " "); got != want {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
	command, err := parseCommand([]string{"environments"})
	if err != nil || command.kind != commandEnvironments {
		t.Fatal("environments command not parsed")
	}
	if _, err := parseCommand([]string{"environments", "extra"}); err == nil {
		t.Fatal("accepted extra arguments")
	}
}

func TestCatalogueRejectsInvalidInput(t *testing.T) {
	isolateCatalogueProfiles(t)
	for _, contents := range []string{
		`[]`,
		`null`,
		`{"Bad_Name":{}}`,
		`{"example":{"unknown":true}}`,
		`{"example":{}}`,
		`{} {}`,
	} {
		path := filepath.Join(t.TempDir(), "catalogue.json")
		if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(catalogueFileEnvironment, path)
		if _, err := loadCatalogue(); err == nil {
			t.Errorf("accepted invalid catalogue %s", contents)
		}
	}
}

func TestCatalogueProfilesUseConfigPortDefaults(t *testing.T) {
	isolateCatalogueProfiles(t)
	c := catalogueTestConfig(false)
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "proxy_port")
	delete(fields, "socks_port")
	data, err = json.Marshal(map[string]any{"example": fields})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalogue.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(catalogueFileEnvironment, path)
	catalogue, err := loadCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	if got := catalogue["example"]; got.ProxyPort != 18080 || got.SOCKSPort != 18081 {
		t.Fatalf("catalogue ports = %d/%d, want defaults 18080/18081", got.ProxyPort, got.SOCKSPort)
	}
	fields["proxy_port"] = 0
	data, err = json.Marshal(map[string]any{"example": fields})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCatalogue(); err == nil {
		t.Fatal("catalogue accepted an explicit zero proxy port")
	}
}

func TestBrokenOverrideSymlinkDoesNotFallBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires extra Windows privileges")
	}
	isolateCatalogueProfiles(t)
	writeCatalogue(t, map[string]config{"example": catalogueTestConfig(false)})
	path, _ := resolveConfigPath("example", "")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEnvironment("example", ""); err == nil {
		t.Fatal("broken override silently fell back")
	}
}
