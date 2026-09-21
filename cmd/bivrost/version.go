package main

import (
	"runtime/debug"
)

// A release build can set this with -ldflags '-X main.version=<version>'.
var version = "dev"

func buildVersion() string {
	if version != "dev" {
		return "bivrost " + version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "bivrost dev"
	}
	revision, modified := "", ""
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			if s.Value == "true" {
				modified = "-dirty"
			}
		}
	}
	if len(revision) > 12 {
		revision = revision[:12]
	}
	if revision == "" {
		return "bivrost dev"
	}
	return "bivrost dev (" + revision + modified + ")"
}
