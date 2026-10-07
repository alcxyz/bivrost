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
