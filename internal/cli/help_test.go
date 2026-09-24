package cli

import (
	"strings"
	"testing"
)

func TestContextualHelpRouting(t *testing.T) {
	for _, tc := range []struct {
		args  []string
		topic string
	}{
		{[]string{"help", "acr", "connect"}, "acr connect"},
		{[]string{"acr", "connect", "--help"}, "acr connect"},
		{[]string{"connect", "-e", "staging", "-h"}, "connect"},
		{[]string{"doctor", "--config", "/missing", "--help"}, "doctor"},
		{[]string{"login", "--help"}, "login"},
		{[]string{"switch", "--help"}, "switch"},
		{[]string{"help", "switch"}, "switch"},
		{[]string{"config", "init", "-h"}, "config init"},
		{[]string{"list", "--help"}, "list"},
		{[]string{"list", "subscriptions", "--help"}, "list subscriptions"},
		{[]string{"help", "list", "subscriptions"}, "list subscriptions"},
		{[]string{"environments", "--help"}, "list"},
		{[]string{"env", "--help"}, "list"},
		{[]string{"help", "envs"}, "list"},
		{[]string{"acr", "help"}, "acr"},
	} {
		cmd, err := Parse(tc.args)
		if err != nil || cmd.Kind != Help || cmd.HelpTopic != tc.topic {
			t.Fatalf("help for %v: %+v, %v", tc.args, cmd, err)
		}
		if HelpText(cmd.HelpTopic) == "" {
			t.Fatalf("missing help for %q", cmd.HelpTopic)
		}
	}
	if _, err := Parse([]string{"help", "acr", "unknown"}); err == nil {
		t.Fatal("unknown help topic must not silently show unrelated help")
	}
	if strings.Contains(HelpText("connect"), "--engine") || strings.Contains(HelpText("acr login"), "--no-login") {
		t.Fatal("command help advertises unsupported options")
	}
}

func TestSubscriptionHelpDescribesReadOnlyDiscovery(t *testing.T) {
	t.Parallel()
	if !strings.Contains(HelpText("list"), "bivrost list subscriptions [--refresh]") {
		t.Fatal("list help does not point to subscription discovery")
	}
	text := HelpText("list subscriptions")
	for _, want := range []string{"Usage: bivrost list subscriptions [--refresh]", "current Azure CLI login", "does not change the selected subscription", "local subscription cache"} {
		if !strings.Contains(text, want) {
			t.Errorf("subscription help missing %q", want)
		}
	}
}

func TestHelpDescribesPodmanOnly(t *testing.T) {
	for _, topic := range []string{"", "acr", "connect", "ssh", "doctor", "acr proxy", "acr connect", "acr login", "acr doctor", "config init"} {
		text := HelpText(topic)
		for _, removed := range []string{"Docker", "docker", "--engine", "container_engine", "selected engine", "preferred container engine"} {
			if strings.Contains(text, removed) {
				t.Errorf("help for %q includes removed engine selection %q", topic, removed)
			}
		}
		if strings.HasPrefix(topic, "acr") && !strings.Contains(text, "Podman") {
			t.Errorf("help for %q does not describe Podman", topic)
		}
	}
}

func TestHelpScopesPrivateHostsToNewSessions(t *testing.T) {
	for _, topic := range []string{"connect", "acr connect", "switch"} {
		text := HelpText(topic)
		if !strings.Contains(text, "--private-host HOST") || !strings.Contains(text, "exact DNS host") {
			t.Errorf("help for %q does not describe exact private host routing", topic)
		}
	}
	for _, topic := range []string{"doctor", "ssh", "acr proxy", "acr login", "acr doctor"} {
		if strings.Contains(HelpText(topic), "--private-host") {
			t.Errorf("help for %q advertises unsupported private host routing", topic)
		}
	}
	if text := HelpText("connect"); !strings.Contains(text, "not saved") {
		t.Fatal("connect help does not state that private host additions are temporary")
	}
	if text := HelpText("switch"); !strings.Contains(text, "not carried over") {
		t.Fatal("switch help does not state that private host additions are not inherited")
	}
}
