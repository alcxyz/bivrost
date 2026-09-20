//go:build windows

package azure

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCommandUsesBundledPythonForCommandScript(t *testing.T) {
	root := t.TempDir()
	wbin := filepath.Join(root, "wbin")
	if err := os.Mkdir(wbin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommandFixture(t, filepath.Join(wbin, "az.cmd"))
	python := filepath.Join(root, "python.exe")
	writeCommandFixture(t, python)
	t.Setenv("PATH", wbin)

	args := []string{"network", "bastion", "tunnel", "--name", "name with spaces", "--target-resource-id", `%PATH% & whoami | more <input >output ^!`}
	cmd, err := Command(context.Background(), args...)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{python, "-IBm", "azure.cli"}, args...)
	if cmd.Path != python {
		t.Fatalf("Command() path = %q, want bundled Python %q", cmd.Path, python)
	}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("Command() arguments = %#v, want %#v", cmd.Args, want)
	}
}

func TestCommandRejectsCommandScriptWithoutBundledPython(t *testing.T) {
	wbin := filepath.Join(t.TempDir(), "wbin")
	if err := os.Mkdir(wbin, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCommandFixture(t, filepath.Join(wbin, "az.bat"))
	t.Setenv("PATH", wbin)

	_, err := Command(context.Background(), "account", "show")
	if err == nil || !strings.Contains(err.Error(), "bundled python.exe was not found") {
		t.Fatalf("Command() error = %v, want missing bundled Python error", err)
	}
}

func TestCommandUsesNativeExecutableAndPreservesArguments(t *testing.T) {
	directory := t.TempDir()
	az := filepath.Join(directory, "az.exe")
	writeCommandFixture(t, az)
	t.Setenv("PATH", directory)

	args := []string{"acr", "login", "--name", "name with spaces", "--subscription", `%SUBSCRIPTION% & whoami | more`}
	cmd, err := Command(context.Background(), args...)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{az}, args...)
	if cmd.Path != az {
		t.Fatalf("Command() path = %q, want %q", cmd.Path, az)
	}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("Command() arguments = %#v, want %#v", cmd.Args, want)
	}
}

func writeCommandFixture(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}
