package azure

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestValidHeimdalReadBlobName(t *testing.T) {
	t.Parallel()
	revision := strings.Repeat("a", 64)
	for _, name := range []string{
		"environments/dev/current.json",
		"environments/dev/revisions/" + revision + ".json",
		"team/revisions/dev/revisions/" + revision + ".json",
	} {
		if !validHeimdalReadBlobName(name) {
			t.Errorf("validHeimdalReadBlobName(%q) = false", name)
		}
	}
	for _, name := range []string{
		"current.json",
		"environments/dev/current.json?sig=secret",
		"environments/dev/current.json#fragment",
		"https://examplestate.blob.core.windows.net/heimdal/environments/dev/current.json",
		"/environments/dev/current.json",
		"environments/../dev/current.json",
		"environments/dev//current.json",
		"environments/dev/revisions/" + strings.Repeat("A", 64) + ".json",
		"environments/dev/revisions/" + revision + ".json/extra",
		"environments/dev/revisions/" + revision + ".json?sig=secret",
	} {
		if validHeimdalReadBlobName(name) {
			t.Errorf("validHeimdalReadBlobName(%q) = true", name)
		}
	}
}

func TestParseHeimdalProxyURLAcceptsOnlyLoopbackHTTP(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"http://127.0.0.1:18080", "http://[::1]:18080/"} {
		if _, err := parseHeimdalProxyURL(value); err != nil {
			t.Errorf("parseHeimdalProxyURL(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{
		"", "https://127.0.0.1:18080", "http://localhost:18080",
		"http://127.0.0.1", "http://127.0.0.1:0", "http://127.0.0.1:65536", "http://127.0.0.1:18080/path",
		"http://user:password@127.0.0.1:18080", "http://127.0.0.1:18080?target=other",
		"http://192.0.2.1:18080",
	} {
		if _, err := parseHeimdalProxyURL(value); err == nil {
			t.Errorf("parseHeimdalProxyURL(%q) succeeded", value)
		}
	}
}

func TestReadHeimdalBlobRejectsInvalidInputsBeforeTokenAcquisition(t *testing.T) {
	t.Parallel()
	target := TerraformBackend{Subscription: "sub", Account: "examplestate", Container: "heimdal"}
	endpoint := "https://examplestate.blob.core.windows.net"
	client := &neverRoundTripClient{}
	tests := []struct {
		name     string
		target   TerraformBackend
		endpoint string
		blobName string
		maxBytes int
	}{
		{name: "target", target: TerraformBackend{Subscription: "sub", Account: "BAD", Container: "heimdal"}, endpoint: endpoint, blobName: "environments/dev/current.json", maxBytes: 64},
		{name: "endpoint", target: target, endpoint: "https://untrusted.example", blobName: "environments/dev/current.json", maxBytes: 64},
		{name: "blob name", target: target, endpoint: endpoint, blobName: "environments/../current.json", maxBytes: 64},
		{name: "zero limit", target: target, endpoint: endpoint, blobName: "environments/dev/current.json", maxBytes: 0},
		{name: "excessive limit", target: target, endpoint: endpoint, blobName: "environments/dev/current.json", maxBytes: maxHeimdalReadBytes + 1},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := readHeimdalBlobWithClient(context.Background(), test.target, test.endpoint, test.blobName, nil, test.maxBytes, client.client()); err == nil {
				t.Fatal("readHeimdalBlobWithClient() accepted invalid input")
			}
		})
	}
}

func TestHeimdalReadErrorsNeverExposeCauses(t *testing.T) {
	t.Parallel()
	private := errors.New("PRIVATE-TRANSPORT-ERROR")
	for failure := heimdalReadFailureUnknown; failure <= heimdalReadFailureOversize; failure++ {
		err := &heimdalReadError{failure: failure}
		if failure == heimdalReadFailureTokenTimeout || failure == heimdalReadFailureRequestTimeout {
			err.cause = context.DeadlineExceeded
		}
		if strings.Contains(err.Error(), private.Error()) {
			t.Fatalf("failure %d exposed private cause", failure)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type neverRoundTripClient struct{}

func (*neverRoundTripClient) client() *http.Client {
	return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unexpected request")
	})}
}
