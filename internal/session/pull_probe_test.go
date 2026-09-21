package session

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestDoctorPullProbeUsesSessionAndDiscardsOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable")
	}
	directory := t.TempDir()
	script := `#!/bin/sh
[ "$#" = 4 ] && [ "$1" = pull ] && [ "$2" = --quiet ] && [ "$3" = --policy=always ] && [ "$4" = "$TEST_IMAGE" ] || exit 4
[ "$CONTAINER_HOST" = 'ssh://user@localhost:1234/run/user/1000/session/api.sock' ] || exit 5
[ "$CONTAINER_SSHKEY" = '/session/identity' ] || exit 6
[ "$CONTAINERS_CONF_OVERRIDE" = '/session/containers.conf' ] || exit 7
[ "$HTTPS_PROXY" = 'http://127.0.0.1:28080' ] || exit 8
printf 'untrusted registry output\n'
printf 'untrusted registry error\n' >&2
exit "$TEST_EXIT"
`
	if err := os.WriteFile(filepath.Join(directory, "podman"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	image := "registry.example/alpine@sha256:0123456789; no-shell"
	t.Setenv("TEST_IMAGE", image)
	t.Setenv("CONTAINER_HOST", "ssh://user@localhost:1234/run/user/1000/session/api.sock")
	t.Setenv("CONTAINER_SSHKEY", "/session/identity")
	t.Setenv("CONTAINERS_CONF_OVERRIDE", "/session/containers.conf")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:28080")
	output, err := os.CreateTemp(directory, "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	originalOut, originalErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = output, output
	defer func() { os.Stdout, os.Stderr = originalOut, originalErr }()
	for _, code := range []string{"0", "1"} {
		t.Setenv("TEST_EXIT", code)
		if got := doctorPullProbe(context.Background(), image); got != (code == "0") {
			t.Fatalf("exit %s: result %v", code, got)
		}
	}
	info, err := output.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatal("subprocess output leaked")
	}
}

func TestDoctorPullProbeCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "podman"), []byte("#!/bin/sh\nexec \"$TEST_SLEEP\" 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("TEST_SLEEP", sleep)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if doctorPullProbe(ctx, "registry.example/alpine:probe") {
		t.Fatal("cancelled pull reported success")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("cancelled pull did not return promptly")
	}
}
