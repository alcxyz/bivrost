//go:build !windows

package azure

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublishInitialHeimdalRunsOrderedCredentialFreeCreateOnlyUploads(t *testing.T) {
	directory, callsPath := installFakeHeimdalAzure(t, "success")
	revisionFile := writeHeimdalTestFile(t, directory, "revision.json", "PRIVATE-REVISION-CONTENT")
	pointerFile := writeHeimdalTestFile(t, directory, "pointer.json", "PRIVATE-POINTER-CONTENT")
	target := TerraformBackend{Subscription: "sub", Account: "examplestate", Container: "heimdal"}
	endpoint := "https://examplestate.blob.core.windows.net"
	revision := strings.Repeat("a", 64)
	environment := append(os.Environ(),
		"AZURE_STORAGE_KEY=PRIVATE-STORAGE-KEY",
		"AZURE_STORAGE_CONNECTION_STRING=PRIVATE-CONNECTION",
		"AZURE_STORAGE_SAS_TOKEN=PRIVATE-SAS",
		"AZURE_STORAGE_ACCOUNT=other",
		"AZURE_STORAGE_AUTH_MODE=key",
		"AZURE_STORAGE_SERVICE_ENDPOINT=https://untrusted.example",
		"AZURE_STORAGE_FUTURE_CREDENTIAL=PRIVATE-FUTURE",
	)

	if err := PublishInitialHeimdal(context.Background(), target, endpoint, "environments/dev", revision, revisionFile, pointerFile, environment); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(callsPath)
	if err != nil {
		t.Fatal(err)
	}
	wantRevision := strings.Join(HeimdalBlobUploadArguments(target, endpoint, "environments/dev/revisions/"+revision+".json", revisionFile), "\n")
	wantPointer := strings.Join(HeimdalBlobUploadArguments(target, endpoint, "environments/dev/current.json", pointerFile), "\n")
	want := "CALL\n" + wantRevision + "\nEND\nCALL\n" + wantPointer + "\nEND\n"
	if string(data) != want {
		t.Fatalf("Azure calls = %q, want %q", data, want)
	}
	for _, forbidden := range []string{"PRIVATE-REVISION-CONTENT", "PRIVATE-POINTER-CONTENT", "PRIVATE-STORAGE-KEY", "PRIVATE-CONNECTION", "PRIVATE-SAS", "PRIVATE-FUTURE", "untrusted.example"} {
		if strings.Contains(string(data), forbidden) {
			t.Errorf("Azure call log contains forbidden value %q", forbidden)
		}
	}
}

func TestPublishInitialHeimdalStopsOnRevisionCollision(t *testing.T) {
	directory, callsPath := installFakeHeimdalAzure(t, "fail-first")
	revisionFile := writeHeimdalTestFile(t, directory, "revision.json", "revision")
	pointerFile := writeHeimdalTestFile(t, directory, "pointer.json", "pointer")
	target := TerraformBackend{Subscription: "sub", Account: "examplestate", Container: "heimdal"}
	err := PublishInitialHeimdal(context.Background(), target, "https://examplestate.blob.core.windows.net", "environments/dev", strings.Repeat("a", 64), revisionFile, pointerFile, os.Environ())
	if err == nil {
		t.Fatal("PublishInitialHeimdal() succeeded after revision collision")
	}
	if HeimdalRevisionOrphaned(err) {
		t.Error("revision collision reported an orphaned revision")
	}
	data, readErr := os.ReadFile(callsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got := strings.Count(string(data), "CALL\n"); got != 1 {
		t.Fatalf("Azure call count = %d, want 1", got)
	}
	if strings.Contains(err.Error(), "PRIVATE-STDERR-MARKER") {
		t.Fatalf("publication exposed Azure CLI stderr: %v", err)
	}
}

func TestPublishInitialHeimdalReportsOrphanWithoutDeletingOnPointerCollision(t *testing.T) {
	directory, callsPath := installFakeHeimdalAzure(t, "fail-second")
	revisionFile := writeHeimdalTestFile(t, directory, "revision.json", "revision")
	pointerFile := writeHeimdalTestFile(t, directory, "pointer.json", "pointer")
	target := TerraformBackend{Subscription: "sub", Account: "examplestate", Container: "heimdal"}
	err := PublishInitialHeimdal(context.Background(), target, "https://examplestate.blob.core.windows.net", "environments/dev", strings.Repeat("a", 64), revisionFile, pointerFile, os.Environ())
	if err == nil {
		t.Fatal("PublishInitialHeimdal() succeeded after pointer collision")
	}
	if !HeimdalRevisionOrphaned(err) || !strings.Contains(err.Error(), "creation was not confirmed") || !strings.Contains(err.Error(), "may remain unreferenced") || !strings.Contains(err.Error(), "no cleanup was attempted") {
		t.Fatalf("pointer failure did not report the orphaned revision safely: %v", err)
	}
	if strings.Contains(err.Error(), "PRIVATE-STDERR-MARKER") {
		t.Fatalf("publication exposed Azure CLI stderr: %v", err)
	}
	data, readErr := os.ReadFile(callsPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	text := string(data)
	if got := strings.Count(text, "CALL\n"); got != 2 {
		t.Fatalf("Azure call count = %d, want 2", got)
	}
	for _, forbidden := range []string{"\ndelete\n", "--overwrite\ntrue"} {
		if strings.Contains(text, forbidden) {
			t.Errorf("partial failure attempted unsafe cleanup or overwrite: %q", text)
		}
	}
}

func TestPublishInitialHeimdalHonorsCancellation(t *testing.T) {
	directory, _ := installFakeHeimdalAzure(t, "wait")
	revisionFile := writeHeimdalTestFile(t, directory, "revision.json", "revision")
	pointerFile := writeHeimdalTestFile(t, directory, "pointer.json", "pointer")
	target := TerraformBackend{Subscription: "sub", Account: "examplestate", Container: "heimdal"}
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	err := PublishInitialHeimdal(ctx, target, "https://examplestate.blob.core.windows.net", "environments/dev", strings.Repeat("a", 64), revisionFile, pointerFile, os.Environ())
	if err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("PublishInitialHeimdal() error = %v, want cancellation", err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("PublishInitialHeimdal() did not promptly honor cancellation")
	}
}

func installFakeHeimdalAzure(t *testing.T, mode string) (string, string) {
	t.Helper()
	directory := t.TempDir()
	azPath := filepath.Join(directory, "az")
	callsPath := filepath.Join(directory, "calls")
	script := `#!/bin/sh
{
  printf '%s\n' CALL
  printf '%s\n' "$@"
  printf '%s\n' END
} >> "$BIVROST_TEST_AZ_CALLS"
for name in AZURE_STORAGE_KEY AZURE_STORAGE_CONNECTION_STRING AZURE_STORAGE_SAS_TOKEN AZURE_STORAGE_ACCOUNT AZURE_STORAGE_AUTH_MODE AZURE_STORAGE_SERVICE_ENDPOINT AZURE_STORAGE_FUTURE_CREDENTIAL; do
  eval '[ "${'"$name"'+x}" != x ]' || exit 41
done
printf '%s\n' PRIVATE-STDOUT-MARKER
printf '%s\n' PRIVATE-STDERR-MARKER >&2
blob_name=
previous=
for argument in "$@"; do
  [ "$previous" = "--name" ] && blob_name="$argument"
  previous="$argument"
done
case "$BIVROST_TEST_AZ_MODE" in
  success) exit 0 ;;
  fail-first) exit 17 ;;
  fail-second) [ "$blob_name" = "environments/dev/current.json" ] && exit 17; exit 0 ;;
  wait) while :; do sleep 1; done ;;
esac
`
	if err := os.WriteFile(azPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BIVROST_TEST_AZ_CALLS", callsPath)
	t.Setenv("BIVROST_TEST_AZ_MODE", mode)
	return directory, callsPath
}
