package session

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alcxyz/bivrost/internal/cli"
	profile "github.com/alcxyz/bivrost/internal/config"
)

func TestTerraformRouteHintPreservesOriginalConnectionChoice(t *testing.T) {
	for _, connected := range []bool{false, true} {
		var out bytes.Buffer
		var session *doctorSessionStatus
		if connected {
			session = &doctorSessionStatus{}
		}
		writeTerraformRouteHint(&out, "examplestate.blob.core.windows.net", session, false)
		text := out.String()
		for _, want := range []string{"If this backend requires private access", "original bivrost connect command", "--private-host examplestate.blob.core.windows.net", "repeat this diagnostic"} {
			if !strings.Contains(text, want) {
				t.Fatalf("missing %q in %s", want, text)
			}
		}
		if strings.Contains(text, "--env") || strings.Contains(text, "--config") {
			t.Fatal("hint invents a target instead of preserving the original command")
		}
		if strings.Contains(text, "Exit this shell") != connected {
			t.Fatal("exit instruction must only appear inside a session")
		}
	}
}

func TestTerraformRouteHintSwitchPreservesRoutesAndACR(t *testing.T) {
	session := &doctorSessionStatus{ProfileEnvironment: "example", Config: profile.Profile{PrivateHosts: []string{"existing.example"}}, Enabled: true, LoginRefreshed: true}
	var out bytes.Buffer
	writeTerraformRouteHint(&out, "state.example", session, true)
	var args []string
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.HasPrefix(line, "  bivrost switch ") {
			args = strings.Fields(strings.TrimSpace(line))[1:]
		}
	}
	command, err := cli.Parse(args)
	if err != nil || command.Kind != cli.Switch || command.Environment != "example" || !command.ACR || strings.Join(command.PrivateHosts, ",") != "existing.example,state.example" {
		t.Fatalf("invalid switch recommendation: %+v, %v, %s", command, err, out.String())
	}
	if strings.Contains(out.String(), "Exit this shell first") {
		t.Fatal("supported session should use switch")
	}
}

func TestTerraformRouteHintDoesNotInventCustomTargetOrRefreshSkippedLogin(t *testing.T) {
	for _, session := range []*doctorSessionStatus{
		{ProfileEnvironment: "custom-profile"},
		{ProfileEnvironment: ""},
		{ProfileEnvironment: "bad;command"},
		{ProfileEnvironment: "example", Enabled: true, LoginRefreshed: false},
	} {
		var out bytes.Buffer
		writeTerraformRouteHint(&out, "state.example", session, true)
		if strings.Contains(out.String(), "bivrost switch") || !strings.Contains(out.String(), "original bivrost connect command") {
			t.Fatal(out.String())
		}
	}
}
