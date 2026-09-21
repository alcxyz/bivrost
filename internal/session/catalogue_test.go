package session

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alcxyz/bivrost/internal/cli"
	profile "github.com/alcxyz/bivrost/internal/config"
)

func isolateCatalogueProfiles(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("HOME", root)
	t.Setenv("AppData", root)
	t.Setenv(profile.CatalogueFileEnvironment, "")
}

func catalogueTestConfig(requiresPIM bool) profile.Profile {
	return profile.Profile{
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

func writeCatalogue(t *testing.T, catalogue map[string]profile.Profile) string {
	t.Helper()
	data, err := json.Marshal(catalogue)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalogue.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(profile.CatalogueFileEnvironment, path)
	return path
}

func writeLocalEnvironment(t *testing.T, name string, c profile.Profile) string {
	t.Helper()
	path, err := profile.ResolvePath(name, "")
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
	if _, err := profile.LoadEnvironment("example", ""); err == nil {
		t.Fatal("environment unexpectedly existed without a catalogue or local profile")
	}
	var output bytes.Buffer
	if err := profile.ListEnvironments(&output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "example") {
		t.Fatalf("listing contains an unexpected built-in profile: %s", output.String())
	}
}

func TestEnvironmentSelectionUsesExternalCatalogueAndLocalOverride(t *testing.T) {
	isolateCatalogueProfiles(t)
	catalogueProfile := catalogueTestConfig(true)
	writeCatalogue(t, map[string]profile.Profile{
		"example": catalogueProfile,
		"prod":    catalogueTestConfig(false),
	})

	c, err := profile.LoadEnvironment("example", "")
	if err != nil || c.Environment != "example" || !c.RequiresPIM {
		t.Fatalf("catalogue selection = %+v, %v", c, err)
	}
	prod, err := profile.LoadEnvironment("prod", "")
	if err != nil || prod.RequiresPIM {
		t.Fatalf("environment name changed explicit requires_pim: %+v, %v", prod, err)
	}

	localProfile := catalogueTestConfig(false)
	localProfile.Registry = "localregistry"
	path := writeLocalEnvironment(t, "example", localProfile)
	c, err = profile.LoadEnvironment("example", "")
	if err != nil || c.Registry != "localregistry" || c.RequiresPIM {
		t.Fatalf("local override should replace the catalogue profile: %+v, %v", c, err)
	}
	if err := os.WriteFile(path, []byte(`invalid`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := profile.LoadEnvironment("example", ""); err == nil {
		t.Fatal("invalid override silently fell back")
	}
	if _, err := profile.LoadEnvironment("example", filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("accepted conflicting selectors")
	}
	if _, err := profile.LoadEnvironment("", filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing explicit config silently fell back")
	}
	if _, err := profile.LoadEnvironment("unknown", ""); err == nil || !strings.Contains(err.Error(), "bivrost list") {
		t.Fatal("unknown environment lacks guidance")
	}
}

func TestListEnvironmentsUsesSortedUnion(t *testing.T) {
	isolateCatalogueProfiles(t)
	writeCatalogue(t, map[string]profile.Profile{
		"middle": catalogueTestConfig(true),
		"zulu":   catalogueTestConfig(false),
	})
	alphaProfile := catalogueTestConfig(true)
	alphaProfile.Registry = ""
	alphaProfile.RegistrySubscription = ""
	alphaProfile.AKS = &profile.AKS{Name: "example-cluster", ResourceGroup: "example-group", Subscription: "example-subscription"}
	writeLocalEnvironment(t, "alpha", alphaProfile)
	writeLocalEnvironment(t, "middle", catalogueTestConfig(false))
	t.Setenv("PATH", t.TempDir())

	var output bytes.Buffer
	if err := profile.ListEnvironments(&output); err != nil {
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
	command, err := cli.Parse([]string{"environments"})
	if err != nil || command.Kind != cli.Environments {
		t.Fatal("environments command not parsed")
	}
	if _, err := cli.Parse([]string{"environments", "extra"}); err == nil {
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
		t.Setenv(profile.CatalogueFileEnvironment, path)
		if _, err := profile.LoadCatalogue(); err == nil {
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
	t.Setenv(profile.CatalogueFileEnvironment, path)
	catalogue, err := profile.LoadCatalogue()
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
	if _, err := profile.LoadCatalogue(); err == nil {
		t.Fatal("catalogue accepted an explicit zero proxy port")
	}
}

func TestBrokenOverrideSymlinkDoesNotFallBack(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires extra Windows privileges")
	}
	isolateCatalogueProfiles(t)
	writeCatalogue(t, map[string]profile.Profile{"example": catalogueTestConfig(false)})
	path, _ := profile.ResolvePath("example", "")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := profile.LoadEnvironment("example", ""); err == nil {
		t.Fatal("broken override silently fell back")
	}
}
