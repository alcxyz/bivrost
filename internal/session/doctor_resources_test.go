package session

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	profile "github.com/alcxyz/bivrost/internal/config"
)

func TestDoctorResourceChecksScopeAndPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX fake executable")
	}
	directory := t.TempDir()
	log := filepath.Join(directory, "calls")
	kube := filepath.Join(directory, "owned kubeconfig")
	script := `#!/bin/sh
[ "$1" = --kubeconfig ] && [ "$2" = "$TEST_KUBECONFIG" ] && [ "$3" = --request-timeout=8s ] && [ "$4" = get ] || exit 4
printf '%s\n' "$5" >> "$TEST_CALLS"
printf 'untrusted subprocess output\n'
case "$5" in
 --raw=/version) exit 0 ;;
 '--raw=/api/v1/nodes?limit=1') exit "${TEST_DENY_NODES:-0}" ;;
 *) exit 5 ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("TEST_KUBECONFIG", kube)
	t.Setenv("TEST_CALLS", log)
	t.Setenv("KUBECONFIG", "/unrelated/personal/config")
	c := platformTestConfig(t)
	c.AKS = &profile.AKS{Name: "test", ResourceGroup: "rg", Subscription: "sub"}
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "before ACR", true: "after ACR"}[enabled], func(t *testing.T) {
			for _, deny := range []string{"0", "1"} {
				t.Setenv("TEST_DENY_NODES", deny)
				results := map[string]string{}
				doctorResourceChecks(context.Background(), c, &doctorSessionStatus{Kubeconfig: kube, Enabled: enabled}, true, func(status, label, detail string) { results[label] = status + " " + detail })
				if !strings.HasPrefix(results["Kubernetes API"], "OK ") {
					t.Fatal(results)
				}
				want := "OK "
				if deny == "1" {
					want = "NOT VERIFIED "
				}
				if !strings.HasPrefix(results["Kubernetes list nodes"], want) {
					t.Fatal(results)
				}
				for _, v := range results {
					if strings.Contains(v, "untrusted") {
						t.Fatal("subprocess output leaked")
					}
				}
			}
		})
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "--raw=/version\n") != 4 || strings.Count(string(data), "--raw=/api/v1/nodes?limit=1\n") != 4 {
		t.Fatal("unexpected probes")
	}
	os.Remove(log)
	doctorResourceChecks(context.Background(), c, nil, true, func(string, string, string) {})
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("probed ambient cluster without a matching session")
	}
}
