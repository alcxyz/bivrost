package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// AuthenticationBrowserRule selects a browser for sign-in to particular
// Microsoft Entra tenants or account domains. Each rule lives in its own file
// so that separate deployments can contribute one per organization.
type AuthenticationBrowserRule struct {
	Name           string   `json:"-"`
	Tenants        []string `json:"tenants"`
	AccountDomains []string `json:"account_domains"`
	AuthenticationBrowser
}

const authenticationBrowsersDirectory = "authentication-browsers.d"

var (
	ruleNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	tenantIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	domainPattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)
)

// AuthenticationBrowsersPath returns the directory of per-organization
// authentication browser rules.
func AuthenticationBrowsersPath() (string, error) {
	root, err := UserRoot()
	if err != nil {
		return "", errors.New("could not locate the Bivrost settings directory")
	}
	return filepath.Join(root, "bivrost", authenticationBrowsersDirectory), nil
}

// LoadAuthenticationBrowserRules reads every NAME.json rule, in name order.
func LoadAuthenticationBrowserRules() ([]AuthenticationBrowserRule, error) {
	directory, err := AuthenticationBrowsersPath()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("could not read Bivrost authentication-browsers.d")
	}
	var rules []AuthenticationBrowserRule
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok {
			continue
		}
		if !ruleNamePattern.MatchString(name) {
			return nil, errors.New("authentication-browsers.d file names must be lowercase NAME.json")
		}
		rule, err := loadAuthenticationBrowserRule(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("authentication-browsers.d/%s.json: %w", name, err)
		}
		rule.Name = name
		rules = append(rules, rule)
	}
	return rules, nil
}

func loadAuthenticationBrowserRule(path string) (AuthenticationBrowserRule, error) {
	// Stat follows symlinks, which configuration managers commonly create.
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return AuthenticationBrowserRule{}, errors.New("must be a readable regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return AuthenticationBrowserRule{}, errors.New("must be a readable regular file")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSettingsBytes+1))
	if err != nil {
		return AuthenticationBrowserRule{}, errors.New("could not be read")
	}
	if len(data) > maxSettingsBytes {
		return AuthenticationBrowserRule{}, errors.New("exceeds 64 KiB")
	}
	var rule AuthenticationBrowserRule
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rule); err != nil {
		return AuthenticationBrowserRule{}, errors.New("contains invalid or unsupported settings")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return AuthenticationBrowserRule{}, errors.New("must contain exactly one JSON object")
	}
	if err := ValidateAuthenticationBrowserRule(rule); err != nil {
		return AuthenticationBrowserRule{}, err
	}
	return rule, nil
}

func ValidateAuthenticationBrowserRule(rule AuthenticationBrowserRule) error {
	if len(rule.Tenants) == 0 && len(rule.AccountDomains) == 0 {
		return errors.New("needs tenants or account_domains; put a browser for every sign-in in settings.json authentication_browser")
	}
	if len(rule.Tenants) > 32 || len(rule.AccountDomains) > 32 {
		return errors.New("supports at most 32 tenants and 32 account_domains")
	}
	for _, tenant := range rule.Tenants {
		tenant = strings.ToLower(tenant)
		if !tenantIDPattern.MatchString(tenant) && !domainPattern.MatchString(tenant) {
			return errors.New("tenants must be tenant IDs or domain names")
		}
	}
	for _, domain := range rule.AccountDomains {
		if !domainPattern.MatchString(strings.ToLower(domain)) {
			return errors.New("account_domains must be domain names such as example.com")
		}
	}
	return ValidateAuthenticationBrowser(rule.AuthenticationBrowser)
}

// SelectAuthenticationBrowser returns the browser for a sign-in to tenant by an
// account in accountDomain; either may be empty when the request does not name
// it. The first matching rule wins; otherwise settings.json
// authentication_browser applies. A nil result leaves the choice to Azure CLI.
func SelectAuthenticationBrowser(tenant, accountDomain string) (*AuthenticationBrowser, error) {
	rules, err := LoadAuthenticationBrowserRules()
	if err != nil {
		return nil, err
	}
	for _, rule := range rules {
		if matchesFold(rule.Tenants, tenant) || matchesFold(rule.AccountDomains, accountDomain) {
			browser := rule.AuthenticationBrowser
			return &browser, nil
		}
	}
	return LoadAuthenticationBrowser()
}

// HasAuthenticationBrowsers reports whether any authentication browser is
// configured, validating the configuration.
func HasAuthenticationBrowsers() (bool, error) {
	rules, err := LoadAuthenticationBrowserRules()
	if err != nil {
		return false, err
	}
	browser, err := LoadAuthenticationBrowser()
	return len(rules) != 0 || browser != nil, err
}

func matchesFold(values []string, want string) bool {
	if want == "" {
		return false
	}
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}
