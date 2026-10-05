package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBrowserRule(t *testing.T, root, name, content string) {
	t.Helper()
	directory := filepath.Join(root, "bivrost", authenticationBrowsersDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

const ruleTenant = "6ee535f2-3064-4ac9-81d8-4ceb2ff790c6"

func TestSelectAuthenticationBrowserMatchesTenantThenDomainThenDefault(t *testing.T) {
	root := isolateSettings(t)
	if got, err := SelectAuthenticationBrowser(ruleTenant, "example.com"); err != nil || got != nil {
		t.Fatalf("SelectAuthenticationBrowser() without configuration = %v, %v", got, err)
	}
	if configured, err := HasAuthenticationBrowsers(); err != nil || configured {
		t.Fatalf("HasAuthenticationBrowsers() = %v, %v", configured, err)
	}
	writeBrowserRule(t, root, "first.json", `{"tenants":["`+strings.ToUpper(ruleTenant)+`"],"executable":"brave","arguments":["--profile-directory=Profile 5","{url}"]}`)
	writeBrowserRule(t, root, "second.json", `{"tenants":["11111111-2222-3333-4444-555555555555"],"account_domains":["Partner.Example"],"executable":"firefox","arguments":["-P","partner"]}`)
	writeBrowserRule(t, root, "._second.json", "copy artifact")
	writeBrowserRule(t, root, "README.md", "ignored")
	writeSettings(t, root, `{"authentication_browser":{"executable":"default-browser"}}`)

	cases := []struct{ tenant, domain, want string }{
		{ruleTenant, "", "brave"},
		{"", "partner.example", "firefox"},
		{"11111111-2222-3333-4444-555555555555", "", "firefox"},
		{"00000000-0000-0000-0000-000000000000", "unknown.example", "default-browser"},
		{"", "", "default-browser"},
	}
	for _, c := range cases {
		got, err := SelectAuthenticationBrowser(c.tenant, c.domain)
		if err != nil || got == nil || got.Executable != c.want {
			t.Errorf("SelectAuthenticationBrowser(%q, %q) = %v, %v; want %s", c.tenant, c.domain, got, err, c.want)
		}
	}
	rules, err := LoadAuthenticationBrowserRules()
	if err != nil || len(rules) != 2 || rules[0].Name != "first" || rules[1].Name != "second" {
		t.Fatalf("LoadAuthenticationBrowserRules() = %+v, %v", rules, err)
	}
	if configured, err := HasAuthenticationBrowsers(); err != nil || !configured {
		t.Fatalf("HasAuthenticationBrowsers() = %v, %v", configured, err)
	}
}

func TestAuthenticationBrowserRuleFollowsSymlinks(t *testing.T) {
	root := isolateSettings(t)
	target := filepath.Join(t.TempDir(), "managed.json")
	if err := os.WriteFile(target, []byte(`{"account_domains":["example.com"],"executable":"browser"}`), 0o444); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "bivrost", authenticationBrowsersDirectory)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "org.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got, err := SelectAuthenticationBrowser("", "example.com")
	if err != nil || got == nil || got.Executable != "browser" {
		t.Fatalf("SelectAuthenticationBrowser() = %v, %v", got, err)
	}
}

func TestLoadAuthenticationBrowserRulesRejectsInvalid(t *testing.T) {
	cases := map[string]string{
		"Upper.json":           `{"account_domains":["example.com"],"executable":"browser"}`,
		"empty.json":           `{"executable":"browser"}`,
		"tenant.json":          `{"tenants":["SECRET_MARKER not a tenant"],"executable":"browser"}`,
		"domain-tenant.json":   `{"tenants":["example.com"],"executable":"browser"}`,
		"domain.json":          `{"account_domains":["user@SECRET_MARKER.example"],"executable":"browser"}`,
		"unknown.json":         `{"account_domains":["example.com"],"executable":"browser","SECRET_MARKER":true}`,
		"browser.json":         `{"account_domains":["example.com"],"executable":"SECRET_MARKER.cmd"}`,
		"trailing.json":        `{"account_domains":["example.com"],"executable":"browser"} {}`,
		"large.json":           `{"account_domains":["example.com"],"executable":"browser","arguments":["` + strings.Repeat("a", maxSettingsBytes) + `"]}`,
		"array.json":           `[]`,
		"default-domain.json":  `{"account_domains":["example.com"],"login_default":true,"executable":"browser"}`,
		"default-tenants.json": `{"tenants":["` + ruleTenant + `","11111111-2222-3333-4444-555555555555"],"login_default":true,"executable":"browser"}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			root := isolateSettings(t)
			writeBrowserRule(t, root, name, content)
			_, err := SelectAuthenticationBrowser("example.com", "")
			if err == nil {
				t.Fatal("expected invalid rule to fail")
			}
			if strings.Contains(err.Error(), "SECRET_MARKER") {
				t.Fatalf("error exposed configuration contents: %v", err)
			}
			if _, err := HasAuthenticationBrowsers(); err == nil {
				t.Fatal("HasAuthenticationBrowsers() accepted an invalid rule")
			}
		})
	}
}

func TestSelectAuthenticationBrowserPrefersTenantOverEarlierDomain(t *testing.T) {
	root := isolateSettings(t)
	// A guest account from home.example signs in to the partner tenant.
	writeBrowserRule(t, root, "a-home.json", `{"account_domains":["home.example"],"executable":"home-browser"}`)
	writeBrowserRule(t, root, "b-partner.json", `{"tenants":["`+ruleTenant+`"],"executable":"partner-browser"}`)
	got, err := SelectAuthenticationBrowser(ruleTenant, "home.example")
	if err != nil || got == nil || got.Executable != "partner-browser" {
		t.Fatalf("SelectAuthenticationBrowser() = %v, %v; want partner-browser", got, err)
	}
}

func TestLoadAuthenticationBrowserRulesRejectsDirectory(t *testing.T) {
	root := isolateSettings(t)
	if err := os.MkdirAll(filepath.Join(root, "bivrost", authenticationBrowsersDirectory, "org.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAuthenticationBrowserRules(); err == nil {
		t.Fatal("LoadAuthenticationBrowserRules() accepted a directory")
	}
}

func TestDefaultLoginTenant(t *testing.T) {
	const other = "11111111-2222-3333-4444-555555555555"
	cases := []struct {
		name            string
		rules           map[string]string
		tenant, rule    string
		wantErrContains string
	}{
		{name: "no rules"},
		{name: "single rule and tenant", rules: map[string]string{
			"home.json": `{"tenants":["` + ruleTenant + `"],"executable":"brave"}`,
		}, tenant: ruleTenant, rule: "home"},
		{name: "single rule with several tenants", rules: map[string]string{
			"home.json": `{"tenants":["` + ruleTenant + `","` + other + `"],"executable":"brave"}`,
		}},
		{name: "single domain rule", rules: map[string]string{
			"home.json": `{"account_domains":["example.com"],"executable":"brave"}`,
		}},
		{name: "several rules without a default", rules: map[string]string{
			"home.json":    `{"tenants":["` + ruleTenant + `"],"executable":"brave"}`,
			"partner.json": `{"tenants":["` + other + `"],"executable":"firefox"}`,
		}},
		{name: "marked rule among several", rules: map[string]string{
			"a-home.json":  `{"tenants":["` + ruleTenant + `"],"executable":"brave"}`,
			"partner.json": `{"tenants":["` + other + `"],"login_default":true,"executable":"firefox"}`,
		}, tenant: other, rule: "partner"},
		{name: "several marked rules", rules: map[string]string{
			"home.json":    `{"tenants":["` + ruleTenant + `"],"login_default":true,"executable":"brave"}`,
			"partner.json": `{"tenants":["` + other + `"],"login_default":true,"executable":"firefox"}`,
		}, wantErrContains: "only one"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := isolateSettings(t)
			for name, content := range c.rules {
				writeBrowserRule(t, root, name, content)
			}
			tenant, rule, err := DefaultLoginTenant()
			if c.wantErrContains != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErrContains) {
					t.Fatalf("DefaultLoginTenant() error = %v; want %q", err, c.wantErrContains)
				}
				// Every command that reads rules reports the conflict.
				if _, err := HasAuthenticationBrowsers(); err == nil {
					t.Fatal("HasAuthenticationBrowsers() accepted two login_default rules")
				}
				return
			}
			if err != nil || tenant != c.tenant || rule != c.rule {
				t.Fatalf("DefaultLoginTenant() = %q, %q, %v; want %q, %q", tenant, rule, err, c.tenant, c.rule)
			}
		})
	}
}
