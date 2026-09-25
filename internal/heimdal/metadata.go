// Package heimdal validates runtime metadata independently of its storage provider.
package heimdal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
)

const MaxDocumentSize = 64 * 1024
const DefaultValidity = 24 * time.Hour
const MaxValidity = 7 * 24 * time.Hour

// Document deliberately permits only exact private routes in the first schema.
type Document struct {
	SchemaVersion int       `json:"schema_version"`
	Environment   string    `json:"environment"`
	IssuedAt      time.Time `json:"issued_at"`
	ExpiresAt     time.Time `json:"expires_at"`
	PrivateHosts  []string  `json:"private_hosts"`
}

type Pointer struct {
	SchemaVersion int    `json:"schema_version"`
	Environment   string `json:"environment"`
	Revision      string `json:"revision"`
}

var prefixSegment = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)

func DefaultPrefix(environment string) string { return "environments/" + environment }

func ValidPrefix(prefix string) bool {
	if len(prefix) == 0 || len(prefix) > 256 {
		return false
	}
	for _, segment := range strings.Split(prefix, "/") {
		if !prefixSegment.MatchString(segment) {
			return false
		}
	}
	return true
}

func Create(environment string, hosts []string, now time.Time, validity time.Duration) ([]byte, []byte, string, error) {
	if validity <= 0 || validity > MaxValidity {
		return nil, nil, "", errors.New("metadata validity must be greater than zero and at most 168h")
	}
	routes := append([]string{}, hosts...)
	sort.Strings(routes)
	unique := routes[:0]
	for _, host := range routes {
		if len(unique) == 0 || unique[len(unique)-1] != host {
			unique = append(unique, host)
		}
	}
	d := Document{1, environment, now.UTC(), now.UTC().Add(validity), unique}
	if err := d.Validate(environment, now); err != nil {
		return nil, nil, "", err
	}
	data, err := json.Marshal(d)
	if err != nil {
		return nil, nil, "", errors.New("cannot encode Heimdal metadata")
	}
	data = append(data, '\n')
	if len(data) > MaxDocumentSize {
		return nil, nil, "", errors.New("Heimdal metadata exceeds size limit")
	}
	sum := sha256.Sum256(data)
	revision := hex.EncodeToString(sum[:])
	pointer, err := json.Marshal(Pointer{1, environment, revision})
	if err != nil {
		return nil, nil, "", errors.New("cannot encode Heimdal pointer")
	}
	return data, append(pointer, '\n'), revision, nil
}

func (d Document) Validate(environment string, now time.Time) error {
	if d.SchemaVersion != 1 {
		return errors.New("unsupported Heimdal schema version")
	}
	if !profile.ValidEnvironmentName(environment) || d.Environment != environment {
		return errors.New("Heimdal environment does not match the requested environment")
	}
	if d.IssuedAt.IsZero() || d.IssuedAt.After(now) || !d.ExpiresAt.After(now) || !d.ExpiresAt.After(d.IssuedAt) || d.ExpiresAt.Sub(d.IssuedAt) > MaxValidity {
		return errors.New("Heimdal metadata is expired or has an invalid validity interval")
	}
	if len(d.PrivateHosts) > 128 {
		return errors.New("Heimdal metadata contains too many private routes")
	}
	return (profile.Profile{PrivateHosts: d.PrivateHosts}).ValidatePrivateHosts()
}

// Decode validates an immutable revision before a caller may use any routes.
func Decode(data []byte, environment, revision string, now time.Time) (Document, error) {
	var d Document
	if len(data) > MaxDocumentSize {
		return d, errors.New("Heimdal metadata exceeds size limit")
	}
	sum := sha256.Sum256(data)
	if revision != hex.EncodeToString(sum[:]) {
		return d, errors.New("Heimdal metadata revision does not match its contents")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return Document{}, errors.New("invalid Heimdal metadata document")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Document{}, errors.New("unexpected data after Heimdal metadata")
	}
	if err := d.Validate(environment, now); err != nil {
		return Document{}, err
	}
	return d, nil
}
