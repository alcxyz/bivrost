package azure

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestHeimdalBlobUploadArgumentsAreCreateOnlyAndUseLogin(t *testing.T) {
	t.Parallel()
	target := TerraformBackend{Subscription: "sub", Account: "examplestate", Container: "heimdal"}
	endpoint := "https://examplestate.blob.core.windows.net"
	want := []string{
		"storage", "blob", "upload",
		"--subscription", "sub",
		"--account-name", "examplestate",
		"--container-name", "heimdal",
		"--name", "environments/dev/revisions/012345.json",
		"--file", "/private/revision.json",
		"--blob-endpoint", endpoint,
		"--auth-mode", "login",
		"--type", "block",
		"--overwrite", "false",
		"--if-none-match", "*",
		"--content-type", "application/json",
		"--no-progress",
		"--timeout", "30",
		"--output", "none",
		"--only-show-errors",
	}
	if got := HeimdalBlobUploadArguments(target, endpoint, "environments/dev/revisions/012345.json", "/private/revision.json"); !reflect.DeepEqual(got, want) {
		t.Fatalf("HeimdalBlobUploadArguments() = %#v, want %#v", got, want)
	}
	joined := " " + strings.Join(want, " ") + " "
	for _, forbidden := range []string{"--account-key", "--sas-token", "--connection-string", " delete ", " container create ", " account create ", " role assignment ", "--overwrite true"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("Heimdal upload contains forbidden operation %q: %s", forbidden, joined)
		}
	}
}

func TestPublishInitialHeimdalValidatesTargetAndBlobNames(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	revisionFile := writeHeimdalTestFile(t, directory, "revision.json", "revision")
	pointerFile := writeHeimdalTestFile(t, directory, "pointer.json", "pointer")
	target := TerraformBackend{Subscription: "sub", Account: "examplestate", Container: "heimdal"}
	endpoint := "https://examplestate.blob.core.windows.net"
	revision := strings.Repeat("a", 64)

	tests := []struct {
		name         string
		target       TerraformBackend
		endpoint     string
		prefix       string
		revision     string
		revisionFile string
		pointerFile  string
	}{
		{name: "target", target: TerraformBackend{Subscription: "sub", Account: "Bad", Container: "heimdal"}, endpoint: endpoint, prefix: "environments/dev", revision: revision, revisionFile: revisionFile, pointerFile: pointerFile},
		{name: "endpoint", target: target, endpoint: "https://untrusted.example", prefix: "environments/dev", revision: revision, revisionFile: revisionFile, pointerFile: pointerFile},
		{name: "absolute prefix", target: target, endpoint: endpoint, prefix: "/environments/dev", revision: revision, revisionFile: revisionFile, pointerFile: pointerFile},
		{name: "parent prefix", target: target, endpoint: endpoint, prefix: "environments/../dev", revision: revision, revisionFile: revisionFile, pointerFile: pointerFile},
		{name: "uppercase prefix", target: target, endpoint: endpoint, prefix: "environments/Dev", revision: revision, revisionFile: revisionFile, pointerFile: pointerFile},
		{name: "revision", target: target, endpoint: endpoint, prefix: "environments/dev", revision: strings.Repeat("A", 64), revisionFile: revisionFile, pointerFile: pointerFile},
		{name: "revision file", target: target, endpoint: endpoint, prefix: "environments/dev", revision: revision, revisionFile: filepath.Join(directory, "missing"), pointerFile: pointerFile},
		{name: "pointer file", target: target, endpoint: endpoint, prefix: "environments/dev", revision: revision, revisionFile: revisionFile, pointerFile: directory},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := PublishInitialHeimdal(context.Background(), test.target, test.endpoint, test.prefix, test.revision, test.revisionFile, test.pointerFile, nil)
			if err == nil {
				t.Fatal("PublishInitialHeimdal() accepted invalid input")
			}
		})
	}
}

func TestHeimdalPublicationGuidanceDoesNotExposeArbitraryErrors(t *testing.T) {
	t.Parallel()
	privateError := errors.New("PRIVATE-ERROR-MARKER")
	for _, err := range []error{
		privateError,
		&heimdalPublicationError{stage: heimdalRevisionStage, failure: heimdalPublicationFailureUnknown, cause: privateError},
		&heimdalPublicationError{stage: heimdalPointerStage, failure: heimdalPublicationFailureUnknown, cause: privateError},
	} {
		guidance := HeimdalPublicationGuidance(err)
		if strings.Contains(guidance, "PRIVATE") {
			t.Fatalf("HeimdalPublicationGuidance() exposed arbitrary error text: %q", guidance)
		}
	}
	if !HeimdalRevisionOrphaned(&heimdalPublicationError{stage: heimdalPointerStage}) {
		t.Error("pointer failure did not report an orphaned revision")
	}
	if HeimdalRevisionOrphaned(&heimdalPublicationError{stage: heimdalRevisionStage}) {
		t.Error("revision failure incorrectly reported an orphaned revision")
	}
}

func writeHeimdalTestFile(t *testing.T, directory, name, content string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
