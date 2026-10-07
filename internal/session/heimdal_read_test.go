package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alcxyz/bivrost/internal/azure"
	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/heimdal"
)

func TestFetchHeimdalRoutesValidatesBeforeReturningRoutes(t *testing.T) {
	doc, pointer, revision, err := heimdal.Create("example", []string{"state.example.net"}, time.Now().Add(-time.Minute), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	expiredDoc, expiredPointer, _, err := heimdal.Create("example", []string{"old.example.net"}, time.Now().Add(-2*time.Hour), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name              string
		pointer, document []byte
		readErr           error
		wantCalls         int
		wantOK            bool
	}{
		{"valid", pointer, doc, nil, 2, true},
		{"denied", pointer, doc, errors.New("access denied"), 1, false},
		{"pointer path injection", []byte(`{"schema_version":1,"environment":"example","revision":"../other"}`), doc, nil, 1, false},
		{"wrong environment", []byte(strings.ReplaceAll(string(pointer), "example", "other")), doc, nil, 1, false},
		{"digest mismatch", pointer, append(append([]byte(nil), doc...), ' '), nil, 2, false},
		{"expired", expiredPointer, expiredDoc, nil, 2, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := profile.Profile{ProxyPort: 18080, Heimdal: &profile.HeimdalSource{Subscription: "metadata-sub", Account: "examplemetadata", Environment: "example"}}
			calls := 0
			endpointFor := func(_ context.Context, account string, env []string) (string, error) {
				if account != "examplemetadata" || !containsString(env, "HTTPS_PROXY="+c.ProxyURL()) {
					t.Fatal("cloud discovery lost bootstrap account or proxy")
				}
				return "https://examplemetadata.blob.core.windows.net", nil
			}
			read := func(_ context.Context, target azure.TerraformBackend, endpoint, blob, proxy string, env []string, limit int) ([]byte, error) {
				calls++
				if target.Subscription != "metadata-sub" || target.Container != "heimdal" || proxy != c.ProxyURL() || !containsString(env, "HTTPS_PROXY="+proxy) {
					t.Fatal("read lost explicit target or session proxy")
				}
				if calls == 1 {
					if blob != "environments/example/current.json" || limit != heimdal.MaxPointerSize {
						t.Fatalf("unexpected pointer request %q limit %d", blob, limit)
					}
					return test.pointer, test.readErr
				}
				if limit != heimdal.MaxDocumentSize || !strings.HasPrefix(blob, "environments/example/revisions/") || (test.wantOK && blob != "environments/example/revisions/"+revision+".json") {
					t.Fatalf("unexpected revision request %q limit %d", blob, limit)
				}
				return test.document, test.readErr
			}
			hosts, err := fetchHeimdalRoutesWith(context.Background(), c, endpointFor, read)
			if (err == nil) != test.wantOK || calls != test.wantCalls {
				t.Fatalf("error=%v calls=%d", err, calls)
			}
			if test.wantOK && !reflect.DeepEqual(hosts, []string{"state.example.net"}) {
				t.Fatalf("routes=%v", hosts)
			}
			if !test.wantOK && len(hosts) != 0 {
				t.Fatal("invalid document exposed partial routes")
			}
		})
	}
}

// heimdalTestReaders serves a fixed pointer and revision without Azure.
func heimdalTestReaders(pointer, document []byte) (func(context.Context, string, []string) (string, error), func(context.Context, azure.TerraformBackend, string, string, string, []string, int) ([]byte, error)) {
	endpointFor := func(context.Context, string, []string) (string, error) {
		return "https://examplemetadata.blob.core.windows.net", nil
	}
	read := func(_ context.Context, _ azure.TerraformBackend, _, blob, _ string, _ []string, _ int) ([]byte, error) {
		if strings.HasSuffix(blob, "/current.json") {
			return pointer, nil
		}
		return document, nil
	}
	return endpointFor, read
}

func TestFetchHeimdalRoutesEnforcesAllowedRouteSuffixes(t *testing.T) {
	routes := []string{"db.private.example.net", "state.example.net"}
	doc, pointer, _, err := heimdal.Create("example", routes, time.Now().Add(-time.Minute), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	endpointFor, read := heimdalTestReaders(pointer, doc)
	for _, test := range []struct {
		name     string
		suffixes []string
		wantOK   bool
	}{
		{"absent list", nil, true},
		{"common parent", []string{"example.net"}, true},
		{"exact route and parent", []string{"state.example.net", "private.example.net"}, true},
		{"one route outside", []string{"private.example.net"}, false},
		{"suffix without label boundary", []string{"ate.example.net", "private.example.net"}, false},
		{"unrelated suffix", []string{"other.example"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			c := profile.Profile{ProxyPort: 18080, Heimdal: &profile.HeimdalSource{Subscription: "metadata-sub", Account: "examplemetadata", Environment: "example", AllowedRouteSuffixes: test.suffixes}}
			hosts, err := fetchHeimdalRoutesWith(context.Background(), c, endpointFor, read)
			if (err == nil) != test.wantOK {
				t.Fatalf("error = %v, want success %v", err, test.wantOK)
			}
			if test.wantOK && !reflect.DeepEqual(hosts, routes) {
				t.Fatalf("routes = %v, want %v", hosts, routes)
			}
			if !test.wantOK {
				if len(hosts) != 0 {
					t.Fatal("rejected revision exposed partial routes")
				}
				for _, route := range routes {
					if strings.Contains(err.Error(), route) {
						t.Fatalf("validation error names a metadata route: %v", err)
					}
				}
			}
		})
	}
}
