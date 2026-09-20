package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/alcxyz/bivrost/internal/azure"
)

const kubeconfigTestSecret = "fixture-secret-must-not-escape"

func TestAKSCredentialsArgumentsAreExplicitAndUnprivileged(t *testing.T) {
	t.Parallel()
	got := azure.AKSCredentialsArguments("cluster-one", "rg-platform", "subscription-one", "/private/session/kubeconfig")
	want := []string{
		"aks", "get-credentials",
		"--subscription", "subscription-one",
		"--resource-group", "rg-platform",
		"--name", "cluster-one",
		"--file", "/private/session/kubeconfig",
		"--format", "exec",
		"--only-show-errors",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("azure.AKSCredentialsArguments() did not produce the required explicit argument set")
	}
	for _, argument := range got {
		if argument == "--admin" {
			t.Fatal("azure.AKSCredentialsArguments() requested administrator credentials")
		}
	}
}

func TestTransformKubeconfigKeepsOnlySafeActiveConfiguration(t *testing.T) {
	t.Parallel()
	input := kubeconfigFixture("https://api.private.example:8443/", map[string]interface{}{
		"proxy-url":             "http://user:" + kubeconfigTestSecret + "@proxy.private.example:8080",
		"certificate-authority": "/tmp/ambient-ca",
		"extension":             map[string]interface{}{"preserved": true},
	}, nil)

	rewritten, target, err := transformKubeconfig(input, 19443)
	if err != nil {
		t.Fatalf("transformKubeconfig() returned an unexpected error")
	}
	if target.path != "" || target.host != "api.private.example" || target.port != "8443" {
		t.Fatalf("transformKubeconfig() target = %+v, want host and original port", target)
	}
	if bytes.Contains(rewritten, []byte(kubeconfigTestSecret)) {
		t.Fatal("rewritten kubeconfig retained a credential from removed configuration")
	}

	var document map[string]json.RawMessage
	if json.Unmarshal(rewritten, &document) != nil {
		t.Fatal("rewritten kubeconfig is not JSON")
	}
	assertSingleNamedEntry(t, document["contexts"], "session", "context")
	cluster := assertSingleNamedEntry(t, document["clusters"], "active-cluster", "cluster")
	user := assertSingleNamedEntry(t, document["users"], "active-user", "user")
	if server, _ := rawString(cluster["server"]); server != "https://127.0.0.1:19443" {
		t.Fatalf("rewritten server = %q", server)
	}
	if tlsName, _ := rawString(cluster["tls-server-name"]); tlsName != "api.private.example" {
		t.Fatalf("TLS server name = %q", tlsName)
	}
	if ca, _ := rawString(cluster["certificate-authority-data"]); ca != "ZmFrZS1jYQ==" {
		t.Fatal("cluster certificate authority was not preserved")
	}
	if _, exists := cluster["proxy-url"]; exists {
		t.Fatal("active cluster proxy-url was retained")
	}
	if _, exists := cluster["certificate-authority"]; exists {
		t.Fatal("active cluster retained a certificate authority file path")
	}
	if _, exists := cluster["insecure-skip-tls-verify"]; exists {
		t.Fatal("active cluster retained insecure-skip-tls-verify")
	}
	if _, exists := cluster["extension"]; !exists {
		t.Fatal("unrelated active cluster data was not preserved")
	}
	if err := validateAzureCLIExec(user); err != nil {
		t.Fatal("active Azure CLI exec authentication was not preserved")
	}
}

func TestTransformKubeconfigPreservesExplicitTLSServerName(t *testing.T) {
	t.Parallel()
	input := kubeconfigFixture("https://api.private.example", map[string]interface{}{
		"tls-server-name":          "certificate.private.example",
		"insecure-skip-tls-verify": false,
	}, nil)
	rewritten, target, err := transformKubeconfig(input, 44321)
	if err != nil {
		t.Fatal("transformKubeconfig() rejected a valid explicit TLS name")
	}
	if target.port != "443" {
		t.Fatalf("default API port = %q, want 443", target.port)
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(rewritten, &document) != nil {
		t.Fatal("rewritten kubeconfig is not JSON")
	}
	cluster := assertSingleNamedEntry(t, document["clusters"], "active-cluster", "cluster")
	if tlsName, _ := rawString(cluster["tls-server-name"]); tlsName != "certificate.private.example" {
		t.Fatalf("explicit TLS server name = %q", tlsName)
	}
	if _, exists := cluster["insecure-skip-tls-verify"]; exists {
		t.Fatal("false insecure-skip-tls-verify flag was not removed")
	}
}

func TestTransformKubeconfigRejectsUnsafeAPIServersWithoutLeakingInput(t *testing.T) {
	t.Parallel()
	servers := []string{
		"http://api.private.example",
		"https://user:" + kubeconfigTestSecret + "@api.private.example",
		"https://api.private.example/" + kubeconfigTestSecret,
		"https://api.private.example/?token=" + kubeconfigTestSecret,
		"https://api.private.example/#" + kubeconfigTestSecret,
		"https://localhost:443",
		"https://127.0.0.1:443",
		"https://api.private.example:0",
		"https://api.private.example:65536",
		"https://api_private.example:443",
		"https://single-label:443",
	}
	for _, server := range servers {
		server := server
		t.Run(strings.ReplaceAll(server, kubeconfigTestSecret, "secret"), func(t *testing.T) {
			t.Parallel()
			_, _, err := transformKubeconfig(kubeconfigFixture(server, nil, nil), 19443)
			if err == nil {
				t.Fatal("transformKubeconfig() accepted an unsafe API server")
			}
			if strings.Contains(err.Error(), kubeconfigTestSecret) {
				t.Fatal("transformKubeconfig() exposed kubeconfig input in its error")
			}
		})
	}
}

func TestTransformKubeconfigRequiresTLSVerificationAndCA(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		cluster map[string]interface{}
	}{
		{name: "skip verification", cluster: map[string]interface{}{"insecure-skip-tls-verify": true}},
		{name: "invalid skip flag", cluster: map[string]interface{}{"insecure-skip-tls-verify": kubeconfigTestSecret}},
		{name: "missing certificate authority", cluster: map[string]interface{}{"certificate-authority-data": nil}},
		{name: "invalid TLS name", cluster: map[string]interface{}{"tls-server-name": "localhost"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := transformKubeconfig(kubeconfigFixture("https://api.private.example", test.cluster, nil), 19443)
			if err == nil {
				t.Fatal("transformKubeconfig() accepted an unsafe TLS configuration")
			}
			if strings.Contains(err.Error(), kubeconfigTestSecret) {
				t.Fatal("transformKubeconfig() exposed kubeconfig input in its error")
			}
		})
	}
}

func TestTransformKubeconfigRejectsUnexpectedCredentialExecution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user map[string]interface{}
	}{
		{name: "arbitrary executable", user: map[string]interface{}{"exec": map[string]interface{}{"command": "sh", "args": []string{"-c", kubeconfigTestSecret}}}},
		{name: "absolute executable", user: map[string]interface{}{"exec": map[string]interface{}{"command": "/tmp/kubelogin", "args": []string{"get-token", "-l", "azurecli"}}}},
		{name: "device code mode", user: map[string]interface{}{"exec": map[string]interface{}{"command": "kubelogin", "args": []string{"get-token", "-l", "devicecode"}}}},
		{name: "missing get token", user: map[string]interface{}{"exec": map[string]interface{}{"command": "kubelogin", "args": []string{"-l", "azurecli"}}}},
		{name: "ambiguous mode", user: map[string]interface{}{"exec": map[string]interface{}{"command": "kubelogin", "args": []string{"get-token", "-l", "azurecli", "--login", "azurecli"}}}},
		{name: "exec environment", user: map[string]interface{}{"exec": map[string]interface{}{"command": "kubelogin", "args": []string{"get-token", "-l", "azurecli"}, "env": []map[string]string{{"name": "AZURE_CONFIG_DIR", "value": kubeconfigTestSecret}}}}},
		{name: "embedded token", user: map[string]interface{}{"token": kubeconfigTestSecret}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := transformKubeconfig(kubeconfigFixture("https://api.private.example", nil, test.user), 19443)
			if err == nil {
				t.Fatal("transformKubeconfig() accepted unsafe user authentication")
			}
			if strings.Contains(err.Error(), kubeconfigTestSecret) {
				t.Fatal("transformKubeconfig() exposed a credential in its error")
			}
		})
	}
}

func TestTransformKubeconfigRejectsInvalidStructureAndPort(t *testing.T) {
	t.Parallel()
	for _, port := range []int{-1, 0, 65536} {
		if _, _, err := transformKubeconfig(kubeconfigFixture("https://api.private.example", nil, nil), port); err == nil {
			t.Fatalf("transformKubeconfig() accepted local port %d", port)
		}
	}
	for _, input := range [][]byte{
		nil,
		[]byte(`{"current-context":"missing","contexts":[],"clusters":[],"users":[]}`),
		[]byte(`{"current-context":"session","contexts":[{"name":"session","context":{}}],"clusters":[],"users":[]}`),
	} {
		if _, _, err := transformKubeconfig(input, 19443); err == nil {
			t.Fatal("transformKubeconfig() accepted an invalid kubeconfig")
		}
	}
}

func kubeconfigFixture(server string, clusterOverrides, userOverrides map[string]interface{}) []byte {
	cluster := map[string]interface{}{
		"server":                     server,
		"certificate-authority-data": "ZmFrZS1jYQ==",
	}
	for key, value := range clusterOverrides {
		if value == nil {
			delete(cluster, key)
		} else {
			cluster[key] = value
		}
	}
	user := map[string]interface{}{
		"exec": map[string]interface{}{
			"apiVersion": "client.authentication.k8s.io/v1beta1",
			"command":    "kubelogin",
			"args":       []string{"get-token", "--login", "azurecli", "--server-id", "fake-server-id"},
		},
	}
	if userOverrides != nil {
		user = userOverrides
	}
	document := map[string]interface{}{
		"apiVersion":      "v1",
		"kind":            "Config",
		"current-context": "session",
		"contexts": []map[string]interface{}{
			{"name": "session", "context": map[string]interface{}{"cluster": "active-cluster", "user": "active-user", "namespace": "default"}},
			{"name": "inactive", "context": map[string]interface{}{"cluster": "inactive-cluster", "user": "inactive-user"}},
		},
		"clusters": []map[string]interface{}{
			{"name": "active-cluster", "cluster": cluster},
			{"name": "inactive-cluster", "cluster": map[string]interface{}{"server": "https://inactive.private.example", "certificate-authority-data": kubeconfigTestSecret}},
		},
		"users": []map[string]interface{}{
			{"name": "active-user", "user": user},
			{"name": "inactive-user", "user": map[string]interface{}{"token": kubeconfigTestSecret, "exec": map[string]interface{}{"command": "sh"}}},
		},
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		panic("test kubeconfig fixture could not be encoded")
	}
	return encoded
}

func assertSingleNamedEntry(t *testing.T, section json.RawMessage, name, objectField string) map[string]json.RawMessage {
	t.Helper()
	var entries []json.RawMessage
	if json.Unmarshal(section, &entries) != nil || len(entries) != 1 {
		t.Fatalf("%s section does not contain exactly one entry", objectField)
	}
	var outer map[string]json.RawMessage
	if json.Unmarshal(entries[0], &outer) != nil {
		t.Fatalf("%s entry is invalid", objectField)
	}
	if got, _ := rawString(outer["name"]); got != name {
		t.Fatalf("%s entry name = %q, want %q", objectField, got, name)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(outer[objectField], &object) != nil {
		t.Fatalf("%s object is invalid", objectField)
	}
	return object
}
