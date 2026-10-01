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
	t.Setenv(modeVariable, modeLaunch)
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
