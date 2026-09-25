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
