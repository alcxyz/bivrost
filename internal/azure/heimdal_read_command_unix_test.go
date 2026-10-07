//go:build !windows

package azure

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadHeimdalBlobUsesExactAudienceAndAuthorization(t *testing.T) {
	calls := installFakeHeimdalReadAzure(t, "success")
	t.Setenv("BIVROST_TEST_AZ_TOKEN", "private-token.value")
	target := TerraformBackend{Subscription: "Example Subscription", Account: "examplestate", Container: "heimdal"}
	endpoint := "https://examplestate.blob.core.windows.net"
	blobName := "environments/dev/current.json"
	body := `{"revision":"abc"}`

	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			t.Errorf("method = %q", request.Method)
		}
		if request.URL.Path != "/heimdal/"+blobName {
			t.Errorf("path = %q", request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer private-token.value" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("x-ms-version"); got != heimdalStorageAPIVersion {
			t.Errorf("x-ms-version = %q", got)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, body)
	}))
	defer server.Close()
	client := heimdalRewriteTLSClient(t, server)

	got, err := readHeimdalBlobWithClient(context.Background(), target, endpoint, blobName, os.Environ(), 1024, client)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
	data, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"account", "get-access-token",
		"--subscription", target.Subscription,
		"--resource", endpoint,
		"--query", "accessToken",
		"--output", "tsv",
		"--only-show-errors",
	}, "\n") + "\n"
	if string(data) != want {
		t.Fatalf("Azure CLI arguments = %q, want %q", data, want)
	}
	for _, secret := range []string{"private-token.value", "AZURE-STORAGE-SECRET", body} {
		if strings.Contains(string(data), secret) {
			t.Errorf("Azure CLI call log exposed %q", secret)
		}
	}
}

func TestReadHeimdalBlobUsesSupportedCloudEndpointAsTokenAudience(t *testing.T) {
	for _, endpoint := range []string{
		"https://examplestate.blob.core.windows.net",
		"https://examplestate.blob.core.usgovcloudapi.net",
		"https://examplestate.blob.core.chinacloudapi.cn",
	} {
		endpoint := endpoint
		t.Run(endpoint, func(t *testing.T) {
			calls := installFakeHeimdalReadAzure(t, "success")
			t.Setenv("BIVROST_TEST_AZ_TOKEN", "private-token")
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    http.StatusOK,
					Body:          io.NopCloser(strings.NewReader("{}")),
					ContentLength: 2,
					Request:       request,
					Header:        make(http.Header),
				}, nil
			})}
			if _, err := readHeimdalBlobWithClient(context.Background(), heimdalReadTarget(), endpoint, "environments/dev/current.json", os.Environ(), 64, client); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), "--resource\n"+endpoint+"\n") {
				t.Fatalf("Azure CLI arguments do not use exact endpoint audience: %q", data)
			}
		})
	}
}

func TestReadHeimdalBlobClassifiesHTTPResponsesWithoutExposingBodies(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		body       string
		contentLen string
		maxBytes   int
		want       string
	}{
		{name: "denied", status: http.StatusForbidden, body: "PRIVATE-DENIAL-BODY", maxBytes: 64, want: "Azure denied access"},
		{name: "redirect", status: http.StatusFound, body: "PRIVATE-REDIRECT-BODY", maxBytes: 64, want: "redirected"},
		{name: "content length oversize", status: http.StatusOK, body: "PRIVATE-OVERSIZE-BODY", contentLen: "999", maxBytes: 4, want: "size limit"},
		{name: "stream oversize", status: http.StatusOK, body: "PRIVATE-OVERSIZE-BODY", maxBytes: 4, want: "size limit"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installFakeHeimdalReadAzure(t, "success")
			t.Setenv("BIVROST_TEST_AZ_TOKEN", "private-token")
			var requests int
			server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				requests++
				if test.status == http.StatusFound {
					response.Header().Set("Location", "https://untrusted.example/private")
				}
				if test.contentLen != "" {
					response.Header().Set("Content-Length", test.contentLen)
				}
				response.WriteHeader(test.status)
				_, _ = io.WriteString(response, test.body)
			}))
			defer server.Close()
			client := heimdalRewriteTLSClient(t, server)
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

			_, err := readHeimdalBlobWithClient(context.Background(), heimdalReadTarget(), heimdalReadEndpoint(), "environments/dev/current.json", os.Environ(), test.maxBytes, client)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readHeimdalBlobWithClient() error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "untrusted.example") || strings.Contains(err.Error(), "private-token") {
				t.Fatalf("error exposed response, redirect, or token data: %v", err)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
		})
	}
}

func TestReadHeimdalBlobClassifiesTimeoutAndTransportErrors(t *testing.T) {
	tests := []struct {
		name      string
		transport http.RoundTripper
		timeout   time.Duration
		want      string
	}{
		{
			name: "timeout",
			transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				return nil, request.Context().Err()
			}),
			timeout: 20 * time.Millisecond,
			want:    "timed out",
		},
		{
			name: "transport",
			transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("PRIVATE-TRANSPORT-ERROR")
			}),
			want: "request failed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installFakeHeimdalReadAzure(t, "success")
			t.Setenv("BIVROST_TEST_AZ_TOKEN", "private-token")
			client := &http.Client{Transport: test.transport, Timeout: test.timeout}
			_, err := readHeimdalBlobWithClient(context.Background(), heimdalReadTarget(), heimdalReadEndpoint(), "environments/dev/current.json", os.Environ(), 64, client)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readHeimdalBlobWithClient() error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), "private-token") {
				t.Fatalf("error exposed transport or token data: %v", err)
			}
			if test.name == "timeout" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("timeout error is not detectable with errors.Is: %v", err)
			}
		})
	}
}

func TestReadHeimdalBlobHonorsCancellationDuringRequest(t *testing.T) {
	installFakeHeimdalReadAzure(t, "success")
	t.Setenv("BIVROST_TEST_AZ_TOKEN", "private-token")
	started := make(chan struct{})
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(started)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := readHeimdalBlobWithClient(ctx, heimdalReadTarget(), heimdalReadEndpoint(), "environments/dev/current.json", os.Environ(), 64, client)
		done <- err
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP request did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("readHeimdalBlobWithClient() error = %v, want cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("read did not promptly honor cancellation")
	}
}

func TestReadHeimdalBlobBoundsAndRedactsTokenOutput(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "oversize stdout", mode: "large", want: "invalid Azure Storage token"},
		{name: "invalid multiline stdout", mode: "multiline", want: "invalid Azure Storage token"},
		{name: "command failure", mode: "fail", want: "token acquisition failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			installFakeHeimdalReadAzure(t, test.mode)
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Fatal("HTTP request started with an invalid token")
				return nil, errors.New("unexpected request")
			})}
			_, err := readHeimdalBlobWithClient(context.Background(), heimdalReadTarget(), heimdalReadEndpoint(), "environments/dev/current.json", os.Environ(), 64, client)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readHeimdalBlobWithClient() error = %v, want %q", err, test.want)
			}
			if strings.Contains(err.Error(), "PRIVATE") || strings.Contains(err.Error(), strings.Repeat("x", 32)) {
				t.Fatalf("token error exposed Azure CLI output: %v", err)
			}
		})
	}
}

type rewriteTLSRoundTripper struct {
	base   http.RoundTripper
	target *url.URL
}

func (transport *rewriteTLSRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	originalRequest := request
	request = request.Clone(request.Context())
	request.URL.Scheme = transport.target.Scheme
	request.URL.Host = transport.target.Host
	request.Host = ""
	response, err := transport.base.RoundTrip(request)
	if response != nil {
		response.Request = originalRequest
	}
	return response, err
}

func heimdalRewriteTLSClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Transport = &rewriteTLSRoundTripper{base: client.Transport, target: target}
	return client
}

func installFakeHeimdalReadAzure(t *testing.T, mode string) string {
	t.Helper()
	directory := t.TempDir()
	calls := filepath.Join(directory, "calls")
	script := `#!/bin/sh
printf '%s\n' "$@" >> "$BIVROST_TEST_AZ_CALLS"
case "$BIVROST_TEST_AZ_MODE" in
success) printf '%s\n' "$BIVROST_TEST_AZ_TOKEN" ;;
large) i=0; while [ "$i" -le 65536 ]; do printf x; i=$((i + 1)); done ;;
multiline) printf '%s\n' PRIVATE-TOKEN PRIVATE-SECOND-LINE ;;
fail) printf '%s\n' PRIVATE-STDERR-MARKER >&2; exit 17 ;;
*) exit 42 ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "az"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("BIVROST_TEST_AZ_CALLS", calls)
	t.Setenv("BIVROST_TEST_AZ_MODE", mode)
	t.Setenv("BIVROST_TEST_AZ_TOKEN", "")
	t.Setenv("AZURE_STORAGE_KEY", "AZURE-STORAGE-SECRET")
	return calls
}

func heimdalReadTarget() TerraformBackend {
	return TerraformBackend{Subscription: "sub", Account: "examplestate", Container: "heimdal"}
}

func heimdalReadEndpoint() string {
	return "https://examplestate.blob.core.windows.net"
}
