// Package config loads and validates connection profiles and user settings.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/alcxyz/bivrost/internal/proxy"
)

type AKS struct {
	Name          string `json:"name"`
	ResourceGroup string `json:"resource_group"`
	Subscription  string `json:"subscription"`
}

type Profile struct {
	ACRProbeImage        string          `json:"acr_probe_image,omitempty"`
	SkipPullProbe        bool            `json:"-"`
	ACRSession           bool            `json:"-"`
	SkipRegistryLogin    bool            `json:"-"`
	RequiresPIM          bool            `json:"requires_pim,omitempty"`
	Prompt               *PromptSettings `json:"-"`
	Environment          string          `json:"-"`
	AKS                  *AKS            `json:"aks,omitempty"`
	PrivateHosts         []string        `json:"private_hosts,omitempty"`
	Registry             string          `json:"registry"`
	RegistrySubscription string          `json:"registry_subscription"`
	Subscription         string          `json:"subscription"`
	BastionName          string          `json:"bastion_name"`
	BastionResourceGroup string          `json:"bastion_resource_group"`
	VMResourceID         string          `json:"vm_resource_id"`
	ProxyPort            int             `json:"proxy_port"`
	SOCKSPort            int             `json:"socks_port"`
	SSHUser              string          `json:"ssh_user,omitempty"`
	IdentityFile         string          `json:"identity_file,omitempty"`
}

var registryPattern = regexp.MustCompile(`^[a-z0-9]{5,50}$`)
var resourcePattern = regexp.MustCompile(`(?i)^/subscriptions/[a-z0-9-]+/resourceGroups/[a-z0-9_.()-]+/providers/Microsoft\.Compute/virtualMachines/[a-z0-9_.-]+$`)
var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_.()][a-zA-Z0-9_.()-]*$`)

func Load(path string) (Profile, error) {
	c := Profile{ProxyPort: 18080, SOCKSPort: 18081}
	f, err := os.Open(path)
	if err != nil {
		return c, fmt.Errorf("open configuration: %w (copy config.example.json and fill in connection details)", err)
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 64*1024))
	d.DisallowUnknownFields()
	if err = d.Decode(&c); err != nil {
		return c, errors.New("configuration must be a JSON object using the fields in config.example.json")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return c, errors.New("unexpected data after configuration")
	}
	return c, nil
}

var probeRepositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*@sha256:[a-f0-9]{64}$`)

func (c Profile) ValidateACR() error {
	if c.Registry == "" {
		return errors.New("set registry explicitly in the configuration to enable ACR access")
	}
	return c.ValidateProxy()
}

func (c Profile) ValidateProxy() error {
	if c.Registry != "" && !registryPattern.MatchString(c.Registry) {
		return errors.New("registry must be a lowercase ACR name, without .azurecr.io")
	}
	if c.ACRProbeImage != "" {
		prefix := c.Registry + ".azurecr.io/"
		if c.Registry == "" || !strings.HasPrefix(c.ACRProbeImage, prefix) || !probeRepositoryPattern.MatchString(strings.TrimPrefix(c.ACRProbeImage, prefix)) {
			return errors.New("acr_probe_image must be a digest-pinned image in this environment's ACR registry")
		}
	}
	if c.ProxyPort < 1024 || c.ProxyPort > 65535 || c.SOCKSPort < 1024 || c.SOCKSPort > 65535 || c.ProxyPort == c.SOCKSPort {
		return errors.New("proxy_port and socks_port must be different ports between 1024 and 65535")
	}
	return nil
}

func (c Profile) ValidatePlatform() error {
	if err := c.ValidateConnection(); err != nil {
		return err
	}
	if err := c.ValidateProxy(); err != nil {
		return err
	}
	if c.AKS != nil && (!namePattern.MatchString(c.AKS.Name) || !namePattern.MatchString(c.AKS.ResourceGroup) || !namePattern.MatchString(c.AKS.Subscription)) {
		return errors.New("aks must specify name, resource_group, and subscription")
	}
	return c.ValidatePrivateHosts()
}

func (c Profile) ValidatePrivateHosts() error {
	for _, host := range c.PrivateHosts {
		if !proxy.ValidRemoteDNSName(host) {
			return errors.New("private_hosts must contain exact lowercase DNS names without URLs, wildcards, IP addresses, or ports")
		}
		if host == "management.azure.com" || host == "login.microsoftonline.com" {
			return errors.New("private_hosts must not contain public Azure management or login endpoints")
		}
	}
	return nil
}

func (c Profile) ValidateConnection() error {
	if !namePattern.MatchString(c.Subscription) || !namePattern.MatchString(c.BastionName) || !namePattern.MatchString(c.BastionResourceGroup) || !resourcePattern.MatchString(c.VMResourceID) {
		return errors.New("fill in subscription, bastion_name, bastion_resource_group and a VM resource ID in the configuration")
	}
	if (c.SSHUser == "") != (c.IdentityFile == "") {
		return errors.New("ssh_user and identity_file must both be set for SSH key authentication; omit both for Entra authentication")
	}
	if strings.ContainsAny(c.SSHUser, "\r\n\t ") || strings.HasPrefix(c.SSHUser, "-") {
		return errors.New("invalid ssh_user")
	}
	if c.IdentityFile != "" && !filepath.IsAbs(c.IdentityFile) {
		return errors.New("identity_file must be an absolute path")
	}
	return nil
}

func Loopback(port int) string     { return net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) }
func (c Profile) ProxyURL() string { return "http://" + Loopback(c.ProxyPort) }

// This value contains no credentials and lets connect reject a mismatched proxy.
func (c Profile) ProxyID() string {
	hosts := append([]string(nil), c.PrivateHosts...)
	sort.Strings(hosts)
	sum := sha256.Sum256([]byte(c.Registry + ":" + strconv.Itoa(c.SOCKSPort) + ":" + strings.Join(hosts, ",")))
	return hex.EncodeToString(sum[:])
}

func ValidResourceName(name string) bool { return namePattern.MatchString(name) }
