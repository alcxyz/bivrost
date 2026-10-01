//go:build !windows

package authbrowser

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunLaunchStartsConfiguredBrowserWithoutShell(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv(modeVariable, modeInteractive)
	record := filepath.Join(root, "argv")
	browser := filepath.Join(root, "browser")
	script := "#!/bin/sh\nfor argument in \"$@\"; do printf '%s\\n' \"$argument\"; done > \"$0.tmp\" && mv \"$0.tmp\" " + record + "\n"
	if err := os.WriteFile(browser, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	writeBrowserSettings(t, root, `{"authentication_browser":{"executable":"`+browser+`","arguments":["-P","Work $(touch injected)","{url}"]}}`)
	address := testAddress + "&x=$(touch%20injected);id"
	var stderr bytes.Buffer
	if status := Run([]string{address}, &stderr); status != 0 {
		t.Fatalf("Run() = %d", status)
	}
	if strings.Contains(stderr.String(), address) {
		t.Fatalf("successful launch printed the sign-in address: %q", stderr.String())
	}
	deadline := time.Now().Add(5 * time.Second)
	var data []byte
	for {
		var err error
		if data, err = os.ReadFile(record); err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	want := "-P\nWork $(touch injected)\n" + address + "\n"
	if string(data) != want {
		t.Fatalf("browser arguments = %q, want %q", data, want)
	}
}

func TestRunLaunchSelectsBrowserForSignInTenant(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv(modeVariable, modeInteractive)
	rules := filepath.Join(root, "bivrost", "authentication-browsers.d")
	if err := os.MkdirAll(rules, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"work", "partner"} {
		browser := filepath.Join(root, name)
		if err := os.WriteFile(browser, []byte("#!/bin/sh\n: > "+filepath.Join(root, name+".opened")+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(rules, "work.json"), []byte(`{"tenants":["6ee535f2-3064-4ac9-81d8-4ceb2ff790c6"],"executable":"`+filepath.Join(root, "work")+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rules, "partner.json"), []byte(`{"account_domains":["partner.example"],"executable":"`+filepath.Join(root, "partner")+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	for _, address := range []string{
		"https://login.microsoftonline.com/6ee535f2-3064-4ac9-81d8-4ceb2ff790c6/oauth2/v2.0/authorize?client_id=x",
		"https://login.microsoftonline.com/11111111-2222-3333-4444-555555555555/oauth2/v2.0/authorize?login_hint=a%40partner.example",
	} {
		if status := Run([]string{address}, &out); status != 0 {
			t.Fatalf("Run(%q) = %d, output %q", address, status, out.String())
		}
	}
	for _, name := range []string{"work", "partner"} {
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(root, name+".opened")); err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s browser was not opened", name)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// An unmatched organization falls through to the user's normal browser.
	if status := Run([]string{"https://login.microsoftonline.com/organizations/oauth2/v2.0/authorize"}, &out); status != 1 {
		t.Fatalf("unmatched Run() = %d, want 1", status)
	}
}
