//go:build !windows

package session

import (
	"os"
	"path/filepath"
	"testing"
)

const loginRuleTenant = "6ee535f2-3064-4ac9-81d8-4ceb2ff790c6"

// fakeLoginAzureCLI records the arguments bivrost login passes to az.
func fakeLoginAzureCLI(t *testing.T) (arguments string) {
	t.Helper()
	tools := t.TempDir()
	arguments = filepath.Join(t.TempDir(), "arguments")
	writeTestExecutable(t, filepath.Join(tools, "az"), "#!/bin/sh\necho \"$*\" > "+arguments+"\n")
	t.Setenv("PATH", tools)
	config := isolateSettings(t)
	rules := filepath.Join(config, "bivrost", "authentication-browsers.d")
	if err := os.MkdirAll(rules, 0o700); err != nil {
		t.Fatal(err)
	}
	rule := `{"tenants":["` + loginRuleTenant + `"],"executable":"browser"}`
	if err := os.WriteFile(filepath.Join(rules, "home.json"), []byte(rule), 0o600); err != nil {
		t.Fatal(err)
	}
	return arguments
}

func TestLoginUsesDefaultTenantUnlessNamed(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"login"}, "login --output none --tenant " + loginRuleTenant},
		{[]string{"login", "-t", "partner.example"}, "login --output none --tenant partner.example"},
	}
	for _, c := range cases {
		arguments := fakeLoginAzureCLI(t)
		if err := Run(c.args, "bivrost dev"); err != nil {
			t.Fatalf("Run(%q) = %v", c.args, err)
		}
		if got := readRecord(t, arguments); got != c.want {
			t.Errorf("Run(%q) called az %q; want %q", c.args, got, c.want)
		}
	}
}
