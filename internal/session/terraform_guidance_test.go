package session

import (
	"bytes"
	"strings"
	"testing"
)

func TestTerraformRouteHintPreservesOriginalConnectionChoice(t *testing.T) {
	for _, connected := range []bool{false, true} {
		var out bytes.Buffer
		writeTerraformRouteHint(&out, "examplestate.blob.core.windows.net", connected)
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
