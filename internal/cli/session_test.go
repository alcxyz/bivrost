package cli

import "testing"

func TestSessionCommands(t *testing.T) {
	if c, err := Parse([]string{"session", "--help"}); err != nil || c.Kind != Help {
		t.Fatal("missing session help", err)
	}
	for action, kind := range map[string]Kind{"publish": SessionPublish, "unpublish": SessionUnpublish, "path": SessionPath, "clean": SessionClean} {
		got, err := Parse([]string{"session", action})
		if err != nil || got.Kind != kind {
			t.Fatalf("%s: %+v %v", action, got, err)
		}
		help, err := Parse([]string{"session", action, "--help"})
		if err != nil || help.Kind != Help || HelpText(help.HelpTopic) == "" {
			t.Fatal("missing help", action)
		}
		if _, err = Parse([]string{"session", action, "--env", "other"}); err == nil {
			t.Fatal("accepted retargeting flag")
		}
	}
	if _, err := Parse([]string{"session", "other"}); err == nil {
		t.Fatal("accepted unknown action")
	}
}
