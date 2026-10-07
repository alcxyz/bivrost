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

func TestDiscoverSubscriptionsRunsBoundedReadOnlyCommand(t *testing.T) {
	argsPath := installFakeAzure(t, "valid")
	subscriptions, err := DiscoverSubscriptions(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(subscriptions) != 2 || subscriptions[0].ID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("DiscoverSubscriptions() = %+v", subscriptions)
	}
	data, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join(SubscriptionListArguments(true), "\n") + "\n"
	if string(data) != want {
		t.Fatalf("az argv = %q, want %q", data, want)
	}
}

func TestDiscoverSubscriptionsRejectsMalformedEmptyAndOversizedOutput(t *testing.T) {
	for _, test := range []struct {
		mode string
		want string
	}{
		{mode: "malformed", want: "invalid subscription list"},
		{mode: "empty", want: "no Azure subscriptions are visible"},
		{mode: "oversized", want: "too much subscription data"},
		{mode: "failure", want: "run bivrost login or az login"},
	} {
		t.Run(test.mode, func(t *testing.T) {
			installFakeAzure(t, test.mode)
			_, err := DiscoverSubscriptions(context.Background(), false)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("DiscoverSubscriptions() error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "PRIVATE-STDERR-MARKER") {
				t.Fatalf("DiscoverSubscriptions() exposed subprocess stderr: %v", err)
			}
		})
	}
}

func TestDiscoverSubscriptionsHonorsCancellation(t *testing.T) {
	installFakeAzure(t, "wait")
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(100*time.Millisecond, cancel)
	defer timer.Stop()
	started := time.Now()
	_, err := DiscoverSubscriptions(ctx, false)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("DiscoverSubscriptions() error = %v, want cancellation", err)
	}
	if time.Since(started) > 4*time.Second {
		t.Fatal("DiscoverSubscriptions() did not promptly honor cancellation")
	}
}

func installFakeAzure(t *testing.T, mode string) string {
	t.Helper()
	directory := t.TempDir()
	az := filepath.Join(directory, "az")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$BIVROST_TEST_AZ_ARGS"
case "$BIVROST_TEST_AZ_MODE" in
  valid) sed -n '1,$p' "$BIVROST_TEST_AZ_FIXTURE" ;;
  empty) printf '%s\n' '[]' ;;
  malformed) printf '%s\n' 'not-json' ;;
  oversized) dd if=/dev/zero bs=1048577 count=1 2>/dev/null | tr '\000' x ;;
  failure) printf '%s\n' 'PRIVATE-STDERR-MARKER' >&2; exit 1 ;;
  wait) while :; do sleep 1; done ;;
esac
`
	if err := os.WriteFile(az, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	argsPath := filepath.Join(directory, "args")
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("BIVROST_TEST_AZ_ARGS", argsPath)
	t.Setenv("BIVROST_TEST_AZ_MODE", mode)
	fixture, err := filepath.Abs("testdata/subscriptions.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BIVROST_TEST_AZ_FIXTURE", fixture)
	return argsPath
}
