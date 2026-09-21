package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var environmentPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func ResolvePath(environment, explicitPath string) (string, error) {
	if environment != "" && explicitPath != "" {
		return "", errors.New("--env and --config are mutually exclusive")
	}
	if explicitPath != "" {
		return explicitPath, nil
	}
	if !environmentPattern.MatchString(environment) {
		return "", errors.New("invalid environment name")
	}
	root, err := UserRoot()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(root, "bivrost", "environments", environment+".json"), nil
}

func UserRoot() (string, error) {
	if root := os.Getenv("XDG_CONFIG_HOME"); root != "" {
		if !filepath.IsAbs(root) {
			return "", errors.New("XDG_CONFIG_HOME must be an absolute path")
		}
		return root, nil
	}
	return os.UserConfigDir()
}

func ValidEnvironmentName(name string) bool { return environmentPattern.MatchString(name) }
