package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
)

const (
	catalogueFileEnvironment = "BIVROST_CATALOGUE_FILE"
	maxCatalogueBytes        = 1024 * 1024
)

func loadCatalogue() (map[string]config, error) {
	path := os.Getenv(catalogueFileEnvironment)
	if path == "" {
		return map[string]config{}, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open environment catalogue: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxCatalogueBytes+1))
	if err != nil {
		return nil, errors.New("could not read environment catalogue")
	}
	if len(data) > maxCatalogueBytes {
		return nil, errors.New("environment catalogue exceeds 1 MiB")
	}

	rawCatalogue := map[string]json.RawMessage{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rawCatalogue); err != nil {
		return nil, errors.New("environment catalogue must be a JSON object of environment profiles")
	}
	if rawCatalogue == nil {
		return nil, errors.New("environment catalogue must be a JSON object of environment profiles")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("environment catalogue must contain exactly one JSON object")
	}
	catalogue := make(map[string]config, len(rawCatalogue))
	for name, raw := range rawCatalogue {
		if !environmentPattern.MatchString(name) {
			return nil, fmt.Errorf("environment catalogue contains invalid name %q", name)
		}
		c := config{ProxyPort: 18080, SOCKSPort: 18081}
		profileDecoder := json.NewDecoder(bytes.NewReader(raw))
		profileDecoder.DisallowUnknownFields()
		if err := profileDecoder.Decode(&c); err != nil {
			return nil, fmt.Errorf("environment catalogue contains invalid profile %q", name)
		}
		if err := c.validatePlatform(); err != nil {
			return nil, fmt.Errorf("environment catalogue contains invalid profile %q: %w", name, err)
		}
		catalogue[name] = c
	}
	return catalogue, nil
}

func loadEnvironment(name, explicitPath string) (config, error) {
	path, err := resolveConfigPath(name, explicitPath)
	if err != nil {
		return config{}, err
	}
	if explicitPath != "" {
		return loadConfig(path)
	}
	// Lstat distinguishes an absent override from a broken symlink. Never
	// silently fall back when a user intended to supply another target.
	_, err = os.Lstat(path)
	if err == nil {
		c, err := loadConfig(path)
		if err != nil {
			return c, err
		}
		c.Environment = name
		fmt.Fprintf(os.Stderr, "Using local environment override at %q\n", path)
		return c, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return config{}, errors.New("could not inspect local environment profile")
	}

	catalogue, err := loadCatalogue()
	if err != nil {
		return config{}, err
	}
	c, ok := catalogue[name]
	if !ok {
		return config{}, fmt.Errorf("unknown environment %q; run bivrost list or use --config PATH", name)
	}
	c.Environment = name
	return c, nil
}

func localEnvironmentNames() ([]string, error) {
	root, err := userConfigRoot()
	if err != nil {
		return nil, fmt.Errorf("locate user configuration directory: %w", err)
	}
	directory := filepath.Join(root, "bivrost", "environments")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("could not inspect local environment profiles")
	}
	var names []string
	for _, entry := range entries {
		filename := entry.Name()
		if filepath.Ext(filename) != ".json" {
			continue
		}
		name := strings.TrimSuffix(filename, ".json")
		if environmentPattern.MatchString(name) {
			names = append(names, name)
		}
	}
	return names, nil
}

func listEnvironments(w io.Writer) error {
	catalogue, err := loadCatalogue()
	if err != nil {
		return err
	}
	localNames, err := localEnvironmentNames()
	if err != nil {
		return err
	}

	names := make(map[string]struct{}, len(catalogue)+len(localNames))
	local := make(map[string]struct{}, len(localNames))
	for name := range catalogue {
		names[name] = struct{}{}
	}
	for _, name := range localNames {
		names[name] = struct{}{}
		local[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "ENVIRONMENT\tSOURCE\tKUBERNETES\tREGISTRY\tPIM")
	for _, name := range ordered {
		c := catalogue[name]
		source := "catalogue"
		if _, ok := local[name]; ok {
			path, err := resolveConfigPath(name, "")
			if err != nil {
				return err
			}
			c, err = loadConfig(path)
			if err != nil {
				return err
			}
			if _, ok := catalogue[name]; ok {
				source = "local override"
			} else {
				source = "local"
			}
		}
		pim := "not required"
		if c.RequiresPIM {
			pim = "required"
		}
		kube, registry := "-", "-"
		if c.AKS != nil {
			kube = "configured"
		}
		if c.Registry != "" {
			registry = "configured"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", name, source, kube, registry, pim)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	fmt.Fprintln(w, "Capabilities describe configuration, not verified connectivity.")
	fmt.Fprintln(w, "Availability is not authorization. Connections use your own identity and existing permissions.")
	return nil
}
