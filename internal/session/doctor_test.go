package session

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/alcxyz/bivrost/internal/cli"
	profile "github.com/alcxyz/bivrost/internal/config"
)

func TestDoctorReportsAllMissingTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	c := catalogueTestConfig(false)
	c.AKS = &profile.AKS{Name: "example-cluster", ResourceGroup: "example-cluster-rg", Subscription: "cluster-subscription"}
	var out bytes.Buffer
	if err := platformDoctor(context.Background(), c, &out); err == nil {
		t.Fatal("missing prerequisites must produce a failure")
	}
	for _, name := range []string{"az", "ssh", "kubectl", "kubelogin", "podman"} {
		if !strings.Contains(out.String(), "[MISSING] "+name+":") {
			t.Errorf("missing independent result for %s", name)
		}
	}
	if strings.Contains(out.String(), "Docker") || strings.Contains(out.String(), "docker") {
		t.Fatal("doctor must not require or probe Docker")
	}
	if !strings.Contains(out.String(), "[NOT VERIFIED] Azure authentication") {
		t.Fatal("authentication must not be reported as verified without Azure CLI")
	}
}

func TestDoctorValidatesProfileBeforeProbes(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	valid := catalogueTestConfig(false)

	tests := []struct {
		name    string
		config  profile.Profile
		wantErr string
	}{
		{
			name: "negative proxy port",
			config: func() profile.Profile {
				c := valid
				c.ProxyPort = -1
				return c
			}(),
			wantErr: "proxy_port and socks_port must be different ports between 1024 and 65535",
		},
		{
			name: "invalid registry",
			config: func() profile.Profile {
				c := valid
				c.Registry = "not.valid"
				return c
			}(),
			wantErr: "registry must be a lowercase ACR name, without .azurecr.io",
		},
		{
			name: "missing connection",
			config: func() profile.Profile {
				c := valid
				c.BastionName = ""
				return c
			}(),
			wantErr: "fill in subscription, bastion_name, bastion_resource_group and a VM resource ID in the configuration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			err := platformDoctor(context.Background(), tt.config, &out)
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("platformDoctor() error = %v, want %q", err, tt.wantErr)
			}
			if out.Len() != 0 {
				t.Fatalf("platformDoctor() probed prerequisites before validation:\n%s", out.String())
			}
		})
	}
}

func TestDoctorCommandSelectors(t *testing.T) {
	for _, args := range [][]string{{"doctor", "-e", "staging"}, {"doctor", "--config", "profile.json", "-d"}} {
		c, err := cli.Parse(args)
		if err != nil || c.Kind != cli.Doctor {
			t.Fatalf("parse %v: %v", args, err)
		}
	}
	if _, err := cli.Parse([]string{"doctor"}); err != nil {
		t.Fatal("doctor accepts an implicit session target")
	}
}
