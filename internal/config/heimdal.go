package config

import (
	"errors"
	"regexp"
	"strings"

	"github.com/alcxyz/bivrost/internal/proxy"
)

// HeimdalSource is trusted bootstrap configuration, never supplied by metadata.
type HeimdalSource struct {
	Subscription       string `json:"subscription"`
	Account            string `json:"account"`
	Container          string `json:"container,omitempty"`
	Environment        string `json:"environment"`
	Prefix             string `json:"prefix,omitempty"`
	AllowLocalFallback bool   `json:"allow_local_fallback,omitempty"`
	// AllowedRouteSuffixes, when set, limits the routes accepted from
	// metadata. Local and command-line routes are not subject to it.
	AllowedRouteSuffixes []string `json:"allowed_route_suffixes,omitempty"`
}

const maxAllowedRouteSuffixes = 64

var metadataAccountPattern = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
var metadataContainerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
var metadataPrefixSegmentPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

func (s HeimdalSource) ContainerName() string {
	if s.Container == "" {
		return "heimdal"
	}
	return s.Container
}

func (s HeimdalSource) BlobPrefix() string {
	if s.Prefix == "" {
		return "environments/" + s.Environment
	}
	return s.Prefix
}

func (s HeimdalSource) Validate() error {
	if !ValidResourceName(s.Subscription) || !metadataAccountPattern.MatchString(s.Account) || !ValidEnvironmentName(s.Environment) {
		return errors.New("heimdal requires a valid subscription, storage account and metadata environment")
	}
	container := s.ContainerName()
	if !metadataContainerPattern.MatchString(container) || strings.Contains(container, "--") {
		return errors.New("invalid Heimdal container name")
	}
	prefix := s.BlobPrefix()
	if len(prefix) > 256 {
		return errors.New("Heimdal prefix is too long")
	}
	for _, segment := range strings.Split(prefix, "/") {
		if !metadataPrefixSegmentPattern.MatchString(segment) {
			return errors.New("invalid Heimdal prefix")
		}
	}
	// An explicit empty list is rejected rather than read as "no restriction";
	// JSON null decodes like an absent field and leaves routes unrestricted.
	if s.AllowedRouteSuffixes != nil && (len(s.AllowedRouteSuffixes) == 0 || len(s.AllowedRouteSuffixes) > maxAllowedRouteSuffixes) {
		return errors.New("Heimdal allowed_route_suffixes must list between 1 and 64 domain suffixes when set")
	}
	for _, suffix := range s.AllowedRouteSuffixes {
		if !proxy.ValidRemoteDNSName(suffix) {
			return errors.New("Heimdal allowed_route_suffixes must contain lowercase DNS names of at least two labels, without wildcards, leading dots, IP addresses, or ports")
		}
	}
	return nil
}

// AllowsRoute reports whether a metadata route is within the allowed suffixes:
// equal to one, or below one at a label boundary. Routes are always allowed
// when no suffixes are configured.
func (s HeimdalSource) AllowsRoute(host string) bool {
	if s.AllowedRouteSuffixes == nil {
		return true
	}
	for _, suffix := range s.AllowedRouteSuffixes {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}
