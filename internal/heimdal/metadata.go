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
const MaxPointerSize = 4 * 1024
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

// DecodePointer validates the exact schema used to locate an immutable revision.
func DecodePointer(data []byte, environment string) (Pointer, error) {
	var pointer Pointer
	if len(data) > MaxPointerSize {
		return pointer, errors.New("Heimdal pointer exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return pointer, errors.New("invalid Heimdal pointer")
	}
	var schemaVersion, pointerEnvironment, revision bool
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return Pointer{}, errors.New("invalid Heimdal pointer")
		}
		name, ok := token.(string)
		if !ok {
			return Pointer{}, errors.New("invalid Heimdal pointer")
		}
		switch name {
		case "schema_version":
			if schemaVersion || decodeNonNull(decoder, &pointer.SchemaVersion) != nil {
				return Pointer{}, errors.New("invalid Heimdal pointer")
			}
			schemaVersion = true
		case "environment":
			if pointerEnvironment || decodeNonNull(decoder, &pointer.Environment) != nil {
				return Pointer{}, errors.New("invalid Heimdal pointer")
			}
			pointerEnvironment = true
		case "revision":
			if revision || decodeNonNull(decoder, &pointer.Revision) != nil {
				return Pointer{}, errors.New("invalid Heimdal pointer")
			}
			revision = true
		default:
			return Pointer{}, errors.New("invalid Heimdal pointer")
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return Pointer{}, errors.New("invalid Heimdal pointer")
	}
	if !schemaVersion || !pointerEnvironment || !revision || !atEOF(decoder) {
		return Pointer{}, errors.New("invalid Heimdal pointer")
	}
	if pointer.SchemaVersion != 1 {
		return Pointer{}, errors.New("unsupported Heimdal pointer schema version")
	}
	if !profile.ValidEnvironmentName(environment) || pointer.Environment != environment {
		return Pointer{}, errors.New("Heimdal pointer environment does not match the requested environment")
	}
	if !validRevision(pointer.Revision) {
		return Pointer{}, errors.New("invalid Heimdal pointer revision")
	}
	return pointer, nil
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
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return Document{}, errors.New("invalid Heimdal metadata document")
	}
	var schemaVersion, documentEnvironment, issuedAt, expiresAt, privateHosts bool
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return Document{}, errors.New("invalid Heimdal metadata document")
		}
		name, ok := token.(string)
		if !ok {
			return Document{}, errors.New("invalid Heimdal metadata document")
		}
		switch name {
		case "schema_version":
			if schemaVersion || decodeNonNull(decoder, &d.SchemaVersion) != nil {
				return Document{}, errors.New("invalid Heimdal metadata document")
			}
			schemaVersion = true
		case "environment":
			if documentEnvironment || decodeNonNull(decoder, &d.Environment) != nil {
				return Document{}, errors.New("invalid Heimdal metadata document")
			}
			documentEnvironment = true
		case "issued_at":
			if issuedAt || decodeNonNull(decoder, &d.IssuedAt) != nil {
				return Document{}, errors.New("invalid Heimdal metadata document")
			}
			issuedAt = true
		case "expires_at":
			if expiresAt || decodeNonNull(decoder, &d.ExpiresAt) != nil {
				return Document{}, errors.New("invalid Heimdal metadata document")
			}
			expiresAt = true
		case "private_hosts":
			if privateHosts || decodeNonNull(decoder, &d.PrivateHosts) != nil {
				return Document{}, errors.New("invalid Heimdal metadata document")
			}
			privateHosts = true
		default:
			return Document{}, errors.New("invalid Heimdal metadata document")
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return Document{}, errors.New("invalid Heimdal metadata document")
	}
	if !schemaVersion || !documentEnvironment || !issuedAt || !expiresAt || !privateHosts {
		return Document{}, errors.New("invalid Heimdal metadata document")
	}
	if !atEOF(decoder) {
		return Document{}, errors.New("unexpected data after Heimdal metadata")
	}
	if err := d.Validate(environment, now); err != nil {
		return Document{}, err
	}
	return d, nil
}

func decodeNonNull(decoder *json.Decoder, target any) error {
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("required field is null")
	}
	return json.Unmarshal(raw, target)
}

func atEOF(decoder *json.Decoder) bool {
	var extra json.RawMessage
	return decoder.Decode(&extra) == io.EOF
}

func validRevision(revision string) bool {
	if len(revision) != sha256.Size*2 {
		return false
	}
	for _, c := range revision {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
