package session

import (
	"context"
	"strings"
	"testing"

	"github.com/alcxyz/bivrost/internal/cli"
	profile "github.com/alcxyz/bivrost/internal/config"
)

func TestProbeImageMustBePinnedInSelectedRegistry(t *testing.T) {
	c := platformTestConfig(t)
	valid := c.Registry + ".azurecr.io/docker.io/library/alpine@sha256:" + strings.Repeat("a", 64)
	for _, image := range []string{"", valid} {
		c.ACRProbeImage = image
		if err := c.ValidateACR(); err != nil {
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
		if err := c.ValidateACR(); err == nil {
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
			reportDoctorPull(context.Background(), profile.Profile{ACRProbeImage: test.image, SkipPullProbe: test.skip}, test.ready, func(status, label, detail string) { result = status + " " + label + " " + detail })
			if !strings.Contains(result, test.want) || !strings.HasPrefix(result, "NOT VERIFIED ") {
				t.Fatal(result)
			}
		})
	}
	parsed, err := cli.Parse([]string{"doctor", "--no-pull"})
	if err != nil || !parsed.NoPull {
		t.Fatalf("no-pull option: %+v %v", parsed, err)
	}
	if _, err := cli.Parse([]string{"connect", "-e", "staging", "--no-pull"}); err == nil {
		t.Fatal("accepted doctor-only option")
	}
}

func TestCatalogueDiagnosticImagesAreValidated(t *testing.T) {
	isolateCatalogueProfiles(t)
	c := catalogueTestConfig(false)
	c.ACRProbeImage = c.Registry + ".azurecr.io/example/image@sha256:" + strings.Repeat("a", 64)
	writeCatalogue(t, map[string]profile.Profile{"example": c})
	catalogue, err := profile.LoadCatalogue()
	if err != nil {
		t.Fatal(err)
	}
	if got := catalogue["example"]; got.ACRProbeImage == "" || got.ValidateACR() != nil {
		t.Fatalf("catalogue diagnostic image was not preserved or validated: %+v", got)
	}
}
