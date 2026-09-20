package main

import (
	"context"
	"strings"
	"testing"
)

func TestProbeImageMustBePinnedInSelectedRegistry(t *testing.T) {
	c := platformTestConfig(t)
	valid := c.Registry + ".azurecr.io/docker.io/library/alpine@sha256:" + strings.Repeat("a", 64)
	for _, image := range []string{"", valid} {
		c.ACRProbeImage = image
		if err := c.validateACR(); err != nil {
			t.Fatal(err)
		}
	}
	for _, image := range []string{
		"docker.io/library/alpine@sha256:" + strings.Repeat("a", 64),
		c.Registry + ".azurecr.io.evil.example/alpine@sha256:" + strings.Repeat("a", 64),
		c.Registry + ".azurecr.io/alpine:latest", valid + "\n", valid + " --tls-verify=false",
		c.Registry + ".azurecr.io/../alpine@sha256:" + strings.Repeat("a", 64),
	} {
		c.ACRProbeImage = image
		if err := c.validateACR(); err == nil {
			t.Fatalf("accepted %q", image)
		}
	}
}

func TestPullProbeOnlyRunsWhenEligible(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, test := range []struct {
		name        string
		image       string
		skip, ready bool
		want        string
	}{
		{name: "opt out", image: "configured", skip: true, ready: true, want: "skipped with --no-pull"},
		{name: "no image", ready: true, want: "no diagnostic image configured"},
		{name: "not active", image: "configured", want: "requires an enabled ACR session"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var result string
			reportDoctorPull(context.Background(), config{ACRProbeImage: test.image, SkipPullProbe: test.skip}, test.ready, func(status, label, detail string) { result = status + " " + label + " " + detail })
			if !strings.Contains(result, test.want) || !strings.HasPrefix(result, "NOT VERIFIED ") {
				t.Fatal(result)
			}
		})
	}
	parsed, err := parseCommand([]string{"doctor", "--no-pull"})
	if err != nil || !parsed.noPull {
		t.Fatalf("no-pull option: %+v %v", parsed, err)
	}
	if _, err := parseCommand([]string{"connect", "-e", "staging", "--no-pull"}); err == nil {
		t.Fatal("accepted doctor-only option")
	}
}

func TestCatalogueDiagnosticImagesAreValidated(t *testing.T) {
	isolateCatalogueProfiles(t)
	c := catalogueTestConfig(false)
	c.ACRProbeImage = c.Registry + ".azurecr.io/example/image@sha256:" + strings.Repeat("a", 64)
	writeCatalogue(t, map[string]config{"example": c})
	catalogue, err := loadCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	if got := catalogue["example"]; got.ACRProbeImage == "" || got.validateACR() != nil {
		t.Fatalf("catalogue diagnostic image was not preserved or validated: %+v", got)
	}
}
