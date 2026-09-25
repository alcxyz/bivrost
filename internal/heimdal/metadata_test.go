package heimdal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestRevisionRoundtripAndRejection(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	data, pointer, revision, err := Create("example", []string{"b.example", "a.example", "b.example"}, now, DefaultValidity)
	if err != nil {
		t.Fatal(err)
	}
	d, err := Decode(data, "example", revision, now)
	if err != nil || len(d.PrivateHosts) != 2 || d.PrivateHosts[0] != "a.example" {
		t.Fatalf("roundtrip failed: %+v %v", d, err)
	}
	p, err := DecodePointer(pointer, "example")
	if err != nil || p.Revision != revision {
		t.Fatalf("pointer roundtrip failed: %+v %v", p, err)
	}
	cases := []struct {
		data     []byte
		env, rev string
		now      time.Time
	}{
		{append(append([]byte{}, data...), ' '), "example", revision, now},
		{data, "another", revision, now},
		{data, "example", revision, now.Add(DefaultValidity)},
		{data, "example", revision, now.Add(-time.Second)},
	}
	for _, c := range cases {
		if _, err := Decode(c.data, c.env, c.rev, c.now); err == nil {
			t.Fatal("accepted invalid immutable metadata")
		}
	}
	bad := bytes.Replace(data, []byte(`"schema_version":1`), []byte(`"schema_version":1,"hook":"do-something"`), 1)
	digest := sha256.Sum256(bad)
	if _, err := Decode(bad, "example", hex.EncodeToString(digest[:]), now); err == nil {
		t.Fatal("accepted unknown executable field")
	}
}

func TestDecodeRejectsNonExactDocumentSchema(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	data, _, _, err := Create("example", nil, now, DefaultValidity)
	if err != nil {
		t.Fatal(err)
	}
	replace := func(old, replacement string) []byte {
		t.Helper()
		changed := bytes.Replace(data, []byte(old), []byte(replacement), 1)
		if bytes.Equal(changed, data) {
			t.Fatalf("test fixture does not contain %q", old)
		}
		return changed
	}
	tests := map[string][]byte{
		"null root":           []byte("null"),
		"array root":          []byte("[]"),
		"malformed":           []byte("{"),
		"duplicate field":     replace(`"schema_version":1`, `"schema_version":1,"schema_version":1`),
		"case variant":        replace(`"environment"`, `"Environment"`),
		"unknown field":       replace(`"schema_version":1`, `"schema_version":1,"command":"run"`),
		"missing version":     replace(`"schema_version":1,`, ``),
		"missing environment": replace(`"environment":"example",`, ``),
		"missing issued at":   replace(`"issued_at":"2026-09-25T12:00:00Z",`, ``),
		"missing expires at":  replace(`"expires_at":"2026-09-26T12:00:00Z",`, ``),
		"missing hosts":       replace(`,"private_hosts":[]`, ``),
		"null version":        replace(`"schema_version":1`, `"schema_version":null`),
		"null environment":    replace(`"environment":"example"`, `"environment":null`),
		"null issued at":      replace(`"issued_at":"2026-09-25T12:00:00Z"`, `"issued_at":null`),
		"null expires at":     replace(`"expires_at":"2026-09-26T12:00:00Z"`, `"expires_at":null`),
		"null hosts":          replace(`"private_hosts":[]`, `"private_hosts":null`),
		"trailing document":   append(append([]byte{}, data...), []byte("{}")...),
	}
	for name, malformed := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(malformed, "example", digest(malformed), now); err == nil {
				t.Fatal("accepted non-exact metadata document")
			}
		})
	}
	oversize := bytes.Repeat([]byte(" "), MaxDocumentSize+1)
	if _, err := Decode(oversize, "example", digest(oversize), now); err == nil {
		t.Fatal("accepted oversized metadata document")
	}
}

func TestDecodePointerRejectsNonExactSchema(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	_, data, revision, err := Create("example", nil, now, DefaultValidity)
	if err != nil {
		t.Fatal(err)
	}
	replace := func(old, replacement string) []byte {
		t.Helper()
		changed := bytes.Replace(data, []byte(old), []byte(replacement), 1)
		if bytes.Equal(changed, data) {
			t.Fatalf("test fixture does not contain %q", old)
		}
		return changed
	}
	tests := map[string][]byte{
		"null root":           []byte("null"),
		"array root":          []byte("[]"),
		"malformed":           []byte("{"),
		"duplicate field":     replace(`"revision":"`+revision+`"`, `"revision":"`+revision+`","revision":"`+revision+`"`),
		"case variant":        replace(`"environment"`, `"Environment"`),
		"unknown field":       replace(`"schema_version":1`, `"schema_version":1,"path":"other.json"`),
		"missing version":     replace(`"schema_version":1,`, ``),
		"missing environment": replace(`"environment":"example",`, ``),
		"missing revision":    replace(`,"revision":"`+revision+`"`, ``),
		"null version":        replace(`"schema_version":1`, `"schema_version":null`),
		"null environment":    replace(`"environment":"example"`, `"environment":null`),
		"null revision":       replace(`"revision":"`+revision+`"`, `"revision":null`),
		"unsupported version": replace(`"schema_version":1`, `"schema_version":2`),
		"short revision":      replace(revision, "abc"),
		"non-hex revision":    replace(revision, string(bytes.Repeat([]byte("g"), 64))),
		"uppercase revision":  replace(revision, string(bytes.Repeat([]byte("A"), 64))),
		"trailing document":   append(append([]byte{}, data...), []byte("{}")...),
	}
	for name, malformed := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePointer(malformed, "example"); err == nil {
				t.Fatal("accepted non-exact Heimdal pointer")
			}
		})
	}
	if _, err := DecodePointer(data, "another"); err == nil {
		t.Fatal("accepted pointer for a different environment")
	}
	invalidEnvironment := replace(`"environment":"example"`, `"environment":"bad/name"`)
	if _, err := DecodePointer(invalidEnvironment, "bad/name"); err == nil {
		t.Fatal("accepted pointer with an invalid environment")
	}
	oversize := bytes.Repeat([]byte(" "), MaxPointerSize+1)
	if _, err := DecodePointer(oversize, "example"); err == nil {
		t.Fatal("accepted oversized Heimdal pointer")
	}
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestInitializationRejectsUnsafeRoutesAndLocations(t *testing.T) {
	for _, host := range []string{"*.example", "https://example.com", "login.microsoftonline.com", "127.0.0.1", "a.example;whoami"} {
		if _, _, _, err := Create("example", []string{host}, time.Now(), DefaultValidity); err == nil {
			t.Fatalf("accepted %q", host)
		}
	}
	for _, prefix := range []string{"", "/absolute", "../outside", "x//y", "x/..", "x?sig=secret", "X/y"} {
		if ValidPrefix(prefix) {
			t.Fatalf("accepted %q", prefix)
		}
	}
	if !ValidPrefix(DefaultPrefix("example")) {
		t.Fatal("invalid default prefix")
	}
	if _, _, _, err := Create("example", nil, time.Now(), MaxValidity+time.Second); err == nil {
		t.Fatal("accepted excessive validity")
	}
}
