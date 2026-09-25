package config

import (
	"errors"
	"regexp"
	"strings"
)

// HeimdalSource is trusted bootstrap configuration, never supplied by metadata.
type HeimdalSource struct {
	Subscription       string `json:"subscription"`
	Account            string `json:"account"`
	Container          string `json:"container,omitempty"`
	Environment        string `json:"environment"`
	Prefix             string `json:"prefix,omitempty"`
	AllowLocalFallback bool   `json:"allow_local_fallback,omitempty"`
}

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
	return nil
}
