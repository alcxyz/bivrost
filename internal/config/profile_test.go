package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	t.Parallel()

	valid := `{
		"registry": "exampleregistry",
		"registry_subscription": "acr-subscription",
		"subscription": "bastion-subscription",
		"bastion_name": "bastion",
		"bastion_resource_group": "network-rg",
		"vm_resource_id": "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/jump",
		"proxy_port": 28080,
		"socks_port": 28081
	}`

	tests := []struct {
		name         string
		contents     string
		wantErr      string
		wantProxy    int
		wantSOCKS    int
		wantRegistry string
	}{
		{
			name:         "valid",
			contents:     valid,
			wantProxy:    28080,
			wantSOCKS:    28081,
			wantRegistry: "exampleregistry",
		},
		{
			name:         "port defaults",
			contents:     `{"registry":"exampleregistry"}`,
			wantProxy:    18080,
			wantSOCKS:    18081,
			wantRegistry: "exampleregistry",
		},
		{
			name:     "unknown field",
			contents: strings.Replace(valid, `"socks_port": 28081`, `"socks_port": 28081, "sock_port": 9999`, 1),
			wantErr:  "configuration must be a JSON object",
		},
		{
			name:     "trailing JSON value",
			contents: valid + ` {"registry":"other"}`,
			wantErr:  "unexpected data after configuration",
		},
		{
			name:     "trailing malformed data",
			contents: valid + ` definitely-not-json`,
			wantErr:  "unexpected data after configuration",
		},
		{
			name:     "uppercase registry",
			contents: strings.Replace(valid, `"exampleregistry"`, `"ExampleRegistry"`, 1),
			wantErr:  "registry must be a lowercase ACR name",
		},
		{
			name:     "same ports",
			contents: strings.Replace(valid, `"socks_port": 28081`, `"socks_port": 28080`, 1),
			wantErr:  "proxy_port and socks_port must be different",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tt.contents), 0o600); err != nil {
				t.Fatal(err)
			}

			got, err := Load(path)
			if err == nil {
				err = got.ValidateACR()
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("loadConfig() error = %v, want error containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("loadConfig() error = %v", err)
			}
			if got.Registry != tt.wantRegistry || got.ProxyPort != tt.wantProxy || got.SOCKSPort != tt.wantSOCKS {
				t.Fatalf("loadConfig() = registry %q, proxy %d, SOCKS %d; want %q, %d, %d", got.Registry, got.ProxyPort, got.SOCKSPort, tt.wantRegistry, tt.wantProxy, tt.wantSOCKS)
			}
		})
	}
}

func TestConfigValidateConnection(t *testing.T) {
	t.Parallel()

	valid := Profile{
		Subscription:         "subscription-1",
		BastionName:          "bastion-1",
		BastionResourceGroup: "network-rg",
		VMResourceID:         "/subscriptions/subscription-1/resourceGroups/network-rg/providers/Microsoft.Compute/virtualMachines/jump-1",
	}
	withKey := valid
	withKey.SSHUser = "azureuser"
	withKey.IdentityFile = filepath.Join(t.TempDir(), "keys", "jump_ed25519")

	tests := []struct {
		name    string
		config  Profile
		wantErr string
	}{
		{name: "Entra authentication", config: valid},
		{name: "key authentication", config: withKey},
		{name: "missing connection field", config: func() Profile { c := valid; c.BastionName = ""; return c }(), wantErr: "fill in subscription"},
		{name: "option-shaped subscription", config: func() Profile { c := valid; c.Subscription = "--help"; return c }(), wantErr: "fill in subscription"},
		{name: "option-shaped bastion name", config: func() Profile { c := valid; c.BastionName = "--help"; return c }(), wantErr: "fill in subscription"},
		{name: "option-shaped bastion resource group", config: func() Profile { c := valid; c.BastionResourceGroup = "--help"; return c }(), wantErr: "fill in subscription"},
		{name: "malformed VM resource ID", config: func() Profile { c := valid; c.VMResourceID += "/extensions/extra"; return c }(), wantErr: "fill in subscription"},
		{name: "only SSH user", config: func() Profile { c := valid; c.SSHUser = "azureuser"; return c }(), wantErr: "must both be set"},
		{name: "only identity file", config: func() Profile { c := valid; c.IdentityFile = "/keys/id"; return c }(), wantErr: "must both be set"},
		{name: "SSH option injection", config: func() Profile { c := withKey; c.SSHUser = "-oProxyCommand=bad"; return c }(), wantErr: "invalid ssh_user"},
		{name: "SSH whitespace", config: func() Profile { c := withKey; c.SSHUser = "azure user"; return c }(), wantErr: "invalid ssh_user"},
		{name: "relative identity file", config: func() Profile { c := withKey; c.IdentityFile = "keys/id"; return c }(), wantErr: "absolute path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.config.ValidateConnection()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateConnection() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateConnection() error = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestConfigValidatePlatformRejectsOptionShapedAKSNames(t *testing.T) {
	t.Parallel()

	valid := Profile{
		Subscription:         "subscription-1",
		BastionName:          "bastion-1",
		BastionResourceGroup: "network-rg",
		VMResourceID:         "/subscriptions/subscription-1/resourceGroups/network-rg/providers/Microsoft.Compute/virtualMachines/jump-1",
		ProxyPort:            18080,
		SOCKSPort:            18081,
		AKS: &AKS{
			Name:          "cluster-1",
			ResourceGroup: "cluster-rg",
			Subscription:  "cluster-subscription",
		},
	}

	tests := []struct {
		name   string
		mutate func(*AKS)
	}{
		{name: "name", mutate: func(aks *AKS) { aks.Name = "--help" }},
		{name: "resource group", mutate: func(aks *AKS) { aks.ResourceGroup = "--help" }},
		{name: "subscription", mutate: func(aks *AKS) { aks.Subscription = "--help" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := valid
			aks := *valid.AKS
			config.AKS = &aks
			tt.mutate(config.AKS)
			if err := config.ValidatePlatform(); err == nil || !strings.Contains(err.Error(), "aks must specify") {
				t.Fatalf("ValidatePlatform() error = %v, want AKS validation error", err)
			}
		})
	}
}

func TestValidResourceNameRejectsLeadingDash(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want bool
	}{
		{name: "resource-name", want: true},
		{name: "resource_name.1()", want: true},
		{name: "-resource-name", want: false},
		{name: "--help", want: false},
	}

	for _, tt := range tests {
		if got := ValidResourceName(tt.name); got != tt.want {
			t.Errorf("ValidResourceName(%q) = %t, want %t", tt.name, got, tt.want)
		}
	}
}
