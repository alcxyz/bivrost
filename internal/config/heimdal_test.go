package config

import "testing"

func TestHeimdalBootstrapValidation(t *testing.T) {
	valid := HeimdalSource{Subscription: "subscription-id", Account: "examplemetadata", Environment: "example"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	if valid.ContainerName() != "heimdal" || valid.BlobPrefix() != "environments/example" || valid.AllowLocalFallback {
		t.Fatal("unexpected bootstrap defaults")
	}
	for _, mutate := range []func(*HeimdalSource){
		func(s *HeimdalSource) { s.Account = "https://untrusted.example" },
		func(s *HeimdalSource) { s.Subscription = "--help" },
		func(s *HeimdalSource) { s.Environment = "../example" },
		func(s *HeimdalSource) { s.Container = "invalid--name" },
		func(s *HeimdalSource) { s.Prefix = "../other" },
		func(s *HeimdalSource) { s.Prefix = "environments/example?token=x" },
	} {
		bad := valid
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Fatalf("accepted invalid source: %+v", bad)
		}
	}
}

func TestHeimdalAllowedRouteSuffixesValidation(t *testing.T) {
	source := HeimdalSource{Subscription: "subscription-id", Account: "examplemetadata", Environment: "example"}
	source.AllowedRouteSuffixes = []string{"private.example.net", "vault.azure.net"}
	if err := source.Validate(); err != nil {
		t.Fatal(err)
	}
	tooMany := make([]string, maxAllowedRouteSuffixes+1)
	for i := range tooMany {
		tooMany[i] = "example.net"
	}
	for _, suffixes := range [][]string{
		{},
		tooMany,
		{""},
		{"net"},
		{".example.net"},
		{"*.example.net"},
		{"Example.net"},
		{"example.net."},
		{"example.net:443"},
		{"https://example.net"},
		{"10.0.0.1"},
		{"example.localhost"},
		{"private.example.net", "bad example"},
	} {
		bad := source
		bad.AllowedRouteSuffixes = suffixes
		if err := bad.Validate(); err == nil {
			t.Errorf("accepted allowed_route_suffixes %q", suffixes)
		}
	}
}

func TestHeimdalAllowsRouteMatchesLabelBoundaries(t *testing.T) {
	unrestricted := HeimdalSource{}
	if !unrestricted.AllowsRoute("anything.example.org") {
		t.Fatal("absent allow-list restricted metadata routes")
	}
	source := HeimdalSource{AllowedRouteSuffixes: []string{"private.example.net", "db.example.org"}}
	for host, want := range map[string]bool{
		"private.example.net":         true,
		"api.private.example.net":     true,
		"a.b.private.example.net":     true,
		"db.example.org":              true,
		"replica.db.example.org":      true,
		"notprivate.example.net":      false,
		"private.example.net.evil.io": false,
		"example.net":                 false,
		"mydb.example.org":            false,
		"other.example.org":           false,
	} {
		if got := source.AllowsRoute(host); got != want {
			t.Errorf("AllowsRoute(%q) = %v, want %v", host, got, want)
		}
	}
}
