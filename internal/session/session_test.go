package session

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	profile "github.com/alcxyz/bivrost/internal/config"
)

func TestBastionSessionCloseStopsChildCancelsContextAndRemovesDirectory(t *testing.T) {
	stateRoot := t.TempDir()
	directory := filepath.Join(stateRoot, "session-test")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "credential"), []byte("temporary"), 0o600); err != nil {
		t.Fatal(err)
	}

	process, err := startChild(helperCommand(t, "wait"))
	if err != nil {
		t.Fatalf("startChild() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	session := &bastionSession{
		stateRoot: stateRoot,
		directory: directory,
		process:   process,
		cancel:    cancel,
	}

	session.close()

	select {
	case <-process.done:
	default:
		t.Error("close() returned before the Bastion child was reaped")
	}
	select {
	case <-ctx.Done():
	default:
		t.Error("close() did not cancel the Bastion context")
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Errorf("temporary session directory still exists after close(): %v", err)
	}
	if _, err := os.Stat(stateRoot); err != nil {
		t.Errorf("close() removed or damaged the persistent state root: %v", err)
	}

	// Cleanup is routinely reached through more than one deferred path.
	session.close()
}

func TestSharedSSHArgumentsPreserveEachMode(t *testing.T) {
	t.Parallel()

	c := profile.Profile{
		VMResourceID: "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/jump",
		SOCKSPort:    18081,
		SSHUser:      "azureuser",
		IdentityFile: "/keys/jump_ed25519",
	}
	sshConfig := "/tmp/session/ssh_config"
	stateRoot := "/tmp/bivrost"
	port := 32022
	base := baseSSHArguments(c, sshConfig, stateRoot, port)

	acr := sshArguments(c, sshConfig, stateRoot, port)
	wantACRSuffix := []string{"-N", "-T", "-D", "127.0.0.1:18081", "127.0.0.1"}
	if len(acr) < len(base) || !reflect.DeepEqual(acr[:len(base)], base) {
		t.Fatalf("ACR arguments do not preserve the shared SSH options: %q", acr)
	}
	if got := acr[len(base):]; !reflect.DeepEqual(got, wantACRSuffix) {
		t.Fatalf("ACR mode suffix = %q, want %q", got, wantACRSuffix)
	}

	interactive := interactiveSSHArguments(c, sshConfig, stateRoot, port)
	wantInteractiveSuffix := []string{"-tt", "127.0.0.1"}
	if len(interactive) < len(base) || !reflect.DeepEqual(interactive[:len(base)], base) {
		t.Fatalf("interactive arguments do not preserve the shared SSH options: %q", interactive)
	}
	if got := interactive[len(base):]; !reflect.DeepEqual(got, wantInteractiveSuffix) {
		t.Fatalf("interactive mode suffix = %q, want %q", got, wantInteractiveSuffix)
	}
	for _, forbidden := range []string{"-N", "-T", "-D", "-L", "-R", "RemoteCommand"} {
		if containsString(interactive, forbidden) {
			t.Errorf("interactive SSH unexpectedly contains %q: %q", forbidden, interactive)
		}
	}
	for i := 0; i+1 < len(interactive); i++ {
		if interactive[i] == "-o" && strings.HasPrefix(interactive[i+1], "RemoteCommand=") {
			t.Errorf("interactive SSH unexpectedly configures a remote command: %q", interactive)
		}
	}
	if got := sshOption(t, interactive, "ForwardAgent"); got != "no" {
		t.Errorf("interactive ForwardAgent = %q, want no", got)
	}
}
