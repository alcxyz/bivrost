//go:build !windows

package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alcxyz/bivrost/internal/cli"
	metadata "github.com/alcxyz/bivrost/internal/heimdal"
)

func TestRunHeimdalInitRejectsInvalidDataBeforeAzure(t *testing.T) {
	fixture := installSessionHeimdalAzure(t, "success")
	t.Setenv("BIVROST_SESSION", "")
	t.Setenv("BIVROST_CONTROL_FILE", "")

	for _, test := range []struct {
		name   string
		change func(*cli.Command)
	}{
		{name: "private route", change: func(command *cli.Command) { command.PrivateHosts = []string{"*.internal.example"} }},
		{name: "blob prefix", change: func(command *cli.Command) { command.MetadataPrefix = "../outside" }},
		{name: "validity", change: func(command *cli.Command) { command.MetadataValidity = 0 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := sessionHeimdalCommand()
			test.change(&command)
			var out bytes.Buffer
			if err := runHeimdalInit(context.Background(), command, &out); err == nil {
				t.Fatal("runHeimdalInit() accepted invalid data")
			}
			if out.Len() != 0 {
				t.Fatalf("failed initialization wrote success output: %q", out.String())
			}
		})
	}

	assertNoSessionHeimdalAzureCalls(t, fixture)
	assertSessionHeimdalStagingClean(t, fixture)
}

func TestRunHeimdalInitRejectsIncompleteAndStaleSessionsBeforeAzure(t *testing.T) {
	for _, test := range []struct {
		name, session, control, want string
	}{
		{name: "session marker only", session: "1", want: "incomplete active session"},
		{name: "control marker only", control: "/missing/control.json", want: "incomplete active session"},
		{name: "stale control", session: "1", control: "/missing/control.json", want: "active session unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := installSessionHeimdalAzure(t, "success")
			t.Setenv("BIVROST_SESSION", test.session)
			t.Setenv("BIVROST_CONTROL_FILE", test.control)
			var out bytes.Buffer
			err := runHeimdalInit(context.Background(), sessionHeimdalCommand(), &out)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("runHeimdalInit() error = %v, want %q", err, test.want)
			}
			if out.Len() != 0 {
				t.Fatalf("failed initialization wrote success output: %q", out.String())
			}
			assertNoSessionHeimdalAzureCalls(t, fixture)
			assertSessionHeimdalStagingClean(t, fixture)
		})
	}
}

func TestRunHeimdalInitStagesPublishesAndCleansUp(t *testing.T) {
	fixture := installSessionHeimdalAzure(t, "success")
	t.Setenv("BIVROST_SESSION", "")
	t.Setenv("BIVROST_CONTROL_FILE", "")
	command := sessionHeimdalCommand()
	var out bytes.Buffer

	if err := runHeimdalInit(context.Background(), command, &out); err != nil {
		t.Fatal(err)
	}
	if text := out.String(); !strings.Contains(text, "Heimdal initial metadata published.") || !strings.Contains(text, "https://examplestate.blob.core.windows.net/heimdal/environments/example/current.json") {
		t.Fatalf("success output does not identify the publication: %q", text)
	}

	calls := readSessionHeimdalCalls(t, fixture)
	if len(calls) != 3 || calls[0] != "cloud" || !strings.HasPrefix(calls[1], "upload\tenvironments/example/revisions/") || !strings.HasPrefix(calls[2], "upload\tenvironments/example/current.json\t") {
		t.Fatalf("Azure calls = %q, want cloud discovery followed by revision and pointer uploads", calls)
	}
	for _, call := range calls[1:] {
		fields := strings.Split(call, "\t")
		if len(fields) != 4 || fields[3] != "600" {
			t.Fatalf("staged upload call = %q, want a private regular source file", call)
		}
		if _, err := os.Stat(fields[2]); !os.IsNotExist(err) {
			t.Errorf("staged source remains after publication: %s (%v)", fields[2], err)
		}
	}

	revisionData, err := os.ReadFile(filepath.Join(fixture.capture, "revision.json"))
	if err != nil {
		t.Fatal(err)
	}
	pointerData, err := os.ReadFile(filepath.Join(fixture.capture, "pointer.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pointer metadata.Pointer
	if err := json.Unmarshal(pointerData, &pointer); err != nil {
		t.Fatal(err)
	}
	document, err := metadata.Decode(revisionData, command.Environment, pointer.Revision, time.Now())
	if err != nil {
		t.Fatalf("staged revision is not valid immutable metadata: %v", err)
	}
	if pointer.Environment != command.Environment || !reflect.DeepEqual(document.PrivateHosts, []string{"api.internal.example", "db.internal.example"}) {
		t.Fatalf("published metadata = %+v, pointer = %+v", document, pointer)
	}
	assertSessionHeimdalStagingClean(t, fixture)
}

func TestRunHeimdalInitCleansStagingAfterUploadFailure(t *testing.T) {
	for _, mode := range []string{"fail-revision", "fail-pointer"} {
		t.Run(mode, func(t *testing.T) {
			fixture := installSessionHeimdalAzure(t, mode)
			t.Setenv("BIVROST_SESSION", "")
			t.Setenv("BIVROST_CONTROL_FILE", "")
			var out bytes.Buffer
			if err := runHeimdalInit(context.Background(), sessionHeimdalCommand(), &out); err == nil {
				t.Fatal("runHeimdalInit() succeeded after an upload failure")
			}
			if out.Len() != 0 {
				t.Fatalf("failed upload wrote success output: %q", out.String())
			}
			calls := readSessionHeimdalCalls(t, fixture)
			wantCalls := 2
			if mode == "fail-pointer" {
				wantCalls = 3
			}
			if len(calls) != wantCalls {
				t.Fatalf("Azure call count = %d, want %d: %q", len(calls), wantCalls, calls)
			}
			assertSessionHeimdalStagingClean(t, fixture)
		})
	}
}

func TestRunHeimdalInitCleansStagingAfterCancellation(t *testing.T) {
	fixture := installSessionHeimdalAzure(t, "wait")
	t.Setenv("BIVROST_SESSION", "")
	t.Setenv("BIVROST_CONTROL_FILE", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan error, 1)
	go func() {
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-deadline.C:
				started <- errors.New("timed out waiting for fake Azure upload")
				cancel()
				return
			case <-ticker.C:
				if _, err := os.Stat(fixture.started); err == nil {
					started <- nil
					cancel()
					return
				} else if !os.IsNotExist(err) {
					started <- err
					cancel()
					return
				}
			}
		}
	}()
	var out bytes.Buffer

	err := runHeimdalInit(ctx, sessionHeimdalCommand(), &out)
	if startErr := <-started; startErr != nil {
		t.Fatal(startErr)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runHeimdalInit() error = %v, want cancellation", err)
	}
	if out.Len() != 0 {
		t.Fatalf("canceled upload wrote success output: %q", out.String())
	}
	if calls := readSessionHeimdalCalls(t, fixture); len(calls) != 2 {
		t.Fatalf("Azure calls = %q, want cloud discovery and one canceled upload", calls)
	}
	assertSessionHeimdalStagingClean(t, fixture)
}

func TestRunHeimdalInitUsesValidatedActiveSessionProxy(t *testing.T) {
	fixture := installSessionHeimdalAzure(t, "success")
	t.Setenv("BIVROST_UPSTREAM_PROXY", "")
	t.Setenv("BIVROST_ACR_UPSTREAM_PROXY", "")
	c := platformTestConfig(t)
	proxy, err := startProxy(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxy.close)
	var starts, logins atomic.Int32
	doctorTestController(t, c, activationTestServices(t, &starts, &logins))
	t.Setenv("BIVROST_TEST_EXPECT_PROXY", c.ProxyURL())
	var out bytes.Buffer

	if err := runHeimdalInit(context.Background(), sessionHeimdalCommand(), &out); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 0 || logins.Load() != 0 {
		t.Fatal("metadata publication activated ACR")
	}
	if calls := readSessionHeimdalCalls(t, fixture); len(calls) != 3 {
		t.Fatalf("Azure calls through active session = %q", calls)
	}
	assertSessionHeimdalStagingClean(t, fixture)
}

type sessionHeimdalFixture struct {
	calls, capture, staging, started string
}

func sessionHeimdalCommand() cli.Command {
	return cli.Command{
		Kind:             cli.HeimdalInit,
		Environment:      "example",
		PrivateHosts:     []string{"db.internal.example", "api.internal.example"},
		Subscription:     "sub",
		Account:          "examplestate",
		Container:        "heimdal",
		MetadataPrefix:   "environments/example",
		MetadataValidity: metadata.DefaultValidity,
	}
}

func installSessionHeimdalAzure(t *testing.T, mode string) sessionHeimdalFixture {
	t.Helper()
	root := t.TempDir()
	bin := filepath.Join(root, "bin")
	capture := filepath.Join(root, "capture")
	staging := filepath.Join(root, "staging")
	for _, directory := range []string{bin, capture, staging} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	calls := filepath.Join(root, "calls")
	started := filepath.Join(root, "started")
	script := `#!/bin/sh
if [ "$1 $2" = "cloud show" ]; then
	printf '%s\n' cloud >> "$BIVROST_TEST_AZ_CALLS"
	printf '%s\n' '["AzureCloud","core.windows.net"]'
	exit 0
fi
[ "$1 $2 $3" = "storage blob upload" ] || exit 42
blob_name=
source_file=
previous=
for argument in "$@"; do
	[ "$previous" = "--name" ] && blob_name="$argument"
	[ "$previous" = "--file" ] && source_file="$argument"
	previous="$argument"
done
[ -f "$source_file" ] && [ ! -L "$source_file" ] || exit 43
mode=$(stat "$BIVROST_TEST_STAT_FLAG" "$BIVROST_TEST_STAT_FORMAT" "$source_file") || exit 44
printf 'upload\t%s\t%s\t%s\n' "$blob_name" "$source_file" "$mode" >> "$BIVROST_TEST_AZ_CALLS"
case "$blob_name" in
	*/current.json) capture="$BIVROST_TEST_AZ_CAPTURE/pointer.json"; stage=pointer ;;
	*/revisions/*.json) capture="$BIVROST_TEST_AZ_CAPTURE/revision.json"; stage=revision ;;
	*) exit 45 ;;
esac
cp "$source_file" "$capture" || exit 46
case "$BIVROST_TEST_AZ_MODE:$stage" in
	fail-revision:revision|fail-pointer:pointer) exit 17 ;;
	wait:revision) : > "$BIVROST_TEST_AZ_STARTED"; while :; do sleep 1; done ;;
esac
expected_proxy=${BIVROST_TEST_EXPECT_PROXY-}
if [ -n "$expected_proxy" ]; then
	[ "$HTTPS_PROXY" = "$expected_proxy" ] || exit 47
	[ "$HTTP_PROXY" = "$expected_proxy" ] || exit 48
	[ "$NO_PROXY" = "127.0.0.1,localhost" ] || exit 49
fi
exit 0
`
	if err := os.WriteFile(filepath.Join(bin, "az"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("TMPDIR", staging)
	t.Setenv("BIVROST_TEST_AZ_CALLS", calls)
	t.Setenv("BIVROST_TEST_AZ_CAPTURE", capture)
	t.Setenv("BIVROST_TEST_AZ_MODE", mode)
	t.Setenv("BIVROST_TEST_AZ_STARTED", started)
	t.Setenv("BIVROST_TEST_EXPECT_PROXY", "")
	statFlag, statFormat := "-c", "%a"
	if runtime.GOOS == "darwin" {
		statFlag, statFormat = "-f", "%Lp"
	}
	t.Setenv("BIVROST_TEST_STAT_FLAG", statFlag)
	t.Setenv("BIVROST_TEST_STAT_FORMAT", statFormat)
	return sessionHeimdalFixture{calls: calls, capture: capture, staging: staging, started: started}
}

func readSessionHeimdalCalls(t *testing.T, fixture sessionHeimdalFixture) []string {
	t.Helper()
	data, err := os.ReadFile(fixture.calls)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func assertNoSessionHeimdalAzureCalls(t *testing.T, fixture sessionHeimdalFixture) {
	t.Helper()
	if data, err := os.ReadFile(fixture.calls); err == nil {
		t.Fatalf("Azure CLI was called before validation completed: %q", data)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func assertSessionHeimdalStagingClean(t *testing.T, fixture sessionHeimdalFixture) {
	t.Helper()
	entries, err := os.ReadDir(fixture.staging)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Heimdal staging directory was not cleaned: %v", entries)
	}
}
