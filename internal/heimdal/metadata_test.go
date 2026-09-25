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
	if !bytes.Contains(pointer, []byte(revision)) {
		t.Fatal("pointer doesn't identify revision")
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
