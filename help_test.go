package main

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
		{[]string{"config", "init", "-h"}, "config init"},
		{[]string{"env", "--help"}, "environments"},
		{[]string{"help", "envs"}, "environments"},
		{[]string{"acr", "help"}, "acr"},
	} {
		cmd, err := parseCommand(tc.args)
		if err != nil || cmd.kind != commandHelp || cmd.helpTopic != tc.topic {
			t.Fatalf("help for %v: %+v, %v", tc.args, cmd, err)
		}
		if helpText(cmd.helpTopic) == "" {
			t.Fatalf("missing help for %q", cmd.helpTopic)
		}
	}
	if _, err := parseCommand([]string{"help", "acr", "unknown"}); err == nil {
		t.Fatal("unknown help topic must not silently show unrelated help")
	}
	if strings.Contains(helpText("connect"), "--engine") || strings.Contains(helpText("acr login"), "--no-login") {
		t.Fatal("command help advertises unsupported options")
	}
}

func TestHelpDescribesPodmanOnly(t *testing.T) {
	for _, topic := range []string{"", "acr", "connect", "ssh", "doctor", "acr proxy", "acr connect", "acr login", "acr doctor", "config init"} {
		text := helpText(topic)
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
