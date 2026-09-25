package cli

import (
	"testing"

	"github.com/alcxyz/bivrost/internal/heimdal"
)

func TestHeimdalInitDefaultsAndOverrides(t *testing.T) {
	base := []string{"heimdal", "init", "-e", "example", "--subscription", "example-sub", "--account", "exampleaccount"}
	c, err := Parse(base)
	if err != nil || c.Kind != HeimdalInit || c.Container != "heimdal" || c.MetadataPrefix != "environments/example" || c.MetadataValidity != heimdal.DefaultValidity {
		t.Fatalf("bad defaults: %+v %v", c, err)
	}
	c, err = Parse(append(base, "--container", "metadata", "--prefix", "team/example", "--private-host", "state.example", "--valid-for", "2h"))
	if err != nil || c.Container != "metadata" || c.MetadataPrefix != "team/example" || len(c.PrivateHosts) != 1 {
		t.Fatalf("bad overrides: %+v %v", c, err)
	}
	for _, extra := range [][]string{{"--prefix", "../x"}, {"--valid-for", "0s"}, {"--valid-for", "169h"}, {"--private-host", "*.example"}, {"--overwrite"}} {
		if _, err := Parse(append(base, extra...)); err == nil {
			t.Fatalf("accepted invalid options %v", extra)
		}
	}
	for _, args := range [][]string{{"heimdal", "--help"}, {"heimdal", "init", "--help"}, {"help", "heimdal", "init"}} {
		c, err := Parse(args)
		if err != nil || c.Kind != Help || HelpText(c.HelpTopic) == "" {
			t.Fatalf("missing help %v", args)
		}
	}
}
