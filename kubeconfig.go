package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	kubeconfigFilename       = "kubeconfig"
	aksCredentialsTimeout    = 2 * time.Minute
	kubeconfigCommandTimeout = 30 * time.Second
	maxKubeconfigJSONSize    = 4 * 1024 * 1024
)

type kubeTarget struct {
	path string
	host string
	port string
}

type limitedOutput struct {
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	remaining := w.limit - w.buffer.Len()
	if remaining > 0 {
		if remaining > len(p) {
			remaining = len(p)
		}
		_, _ = w.buffer.Write(p[:remaining])
	}
	if remaining < len(p) {
		w.overflow = true
	}
	return len(p), nil
}

func prepareKubeconfig(ctx context.Context, c config, directory string, localPort int) (_ kubeTarget, resultErr error) {
	finish := diagnosticStep(ctx, eventKubeconfig)
	defer func() { finish(resultErr) }()
	if err := ctx.Err(); err != nil {
		return kubeTarget{}, err
	}
	if c.AKS == nil {
		return kubeTarget{}, errors.New("AKS is not configured for this environment")
	}
	if localPort < 1 || localPort > 65535 {
		return kubeTarget{}, errors.New("invalid local Kubernetes port")
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return kubeTarget{}, errors.New("temporary session directory is unavailable")
	}
	for _, tool := range []string{"az", "kubelogin", "kubectl"} {
		if _, err := exec.LookPath(tool); err != nil {
			return kubeTarget{}, errors.New(tool + " is missing from PATH")
		}
	}

	path := filepath.Join(directory, kubeconfigFilename)
	if _, err := os.Lstat(path); err == nil {
		return kubeTarget{}, errors.New("temporary kubeconfig path is already in use")
	} else if !errors.Is(err, os.ErrNotExist) {
		return kubeTarget{}, errors.New("temporary kubeconfig path is unavailable")
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.Remove(path)
		}
	}()

	credentialsCtx, cancelCredentials := context.WithTimeout(ctx, aksCredentialsTimeout)
	cmd, err := azureCommand(credentialsCtx, aksCredentialsArguments(*c.AKS, path)...)
	if err == nil {
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		err = cmd.Run()
	}
	cancelCredentials()
	if err != nil {
		return kubeTarget{}, commandFailure(ctx, credentialsCtx, "AKS credential preparation failed", "AKS credential preparation timed out")
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return kubeTarget{}, errors.New("Azure CLI did not create a regular kubeconfig file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return kubeTarget{}, errors.New("could not secure the temporary kubeconfig")
	}

	convertCtx, cancelConvert := context.WithTimeout(ctx, kubeconfigCommandTimeout)
	cmd = exec.CommandContext(convertCtx, "kubelogin", "convert-kubeconfig", "-l", "azurecli", "--kubeconfig", path)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	err = cmd.Run()
	cancelConvert()
	if err != nil {
		return kubeTarget{}, commandFailure(ctx, convertCtx, "kubelogin could not prepare Azure CLI authentication", "kubelogin timed out")
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return kubeTarget{}, errors.New("kubelogin did not preserve a regular kubeconfig file")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return kubeTarget{}, errors.New("could not secure the temporary kubeconfig")
	}

	viewCtx, cancelView := context.WithTimeout(ctx, kubeconfigCommandTimeout)
	output := &limitedOutput{limit: maxKubeconfigJSONSize}
	cmd = exec.CommandContext(viewCtx, "kubectl", "--kubeconfig", path, "config", "view", "--raw", "--output", "json")
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	err = cmd.Run()
	cancelView()
	if err != nil {
		return kubeTarget{}, commandFailure(ctx, viewCtx, "kubectl could not inspect the temporary kubeconfig", "kubectl kubeconfig inspection timed out")
	}
	if output.overflow {
		return kubeTarget{}, errors.New("generated kubeconfig is unexpectedly large")
	}

	rewritten, target, err := transformKubeconfig(output.buffer.Bytes(), localPort)
	if err != nil {
		return kubeTarget{}, err
	}
	if err := writePrivateFile(path, rewritten); err != nil {
		return kubeTarget{}, err
	}
	target.path = path
	complete = true
	return target, nil
}

func aksCredentialsArguments(aks aksConfig, path string) []string {
	return []string{
		"aks", "get-credentials",
		"--subscription", aks.Subscription,
		"--resource-group", aks.ResourceGroup,
		"--name", aks.Name,
		"--file", path,
		"--format", "exec",
		"--only-show-errors",
	}
}

func commandFailure(parent, child context.Context, failed, timedOut string) error {
	if err := parent.Err(); err != nil {
		return err
	}
	if errors.Is(child.Err(), context.DeadlineExceeded) {
		return errors.New(timedOut)
	}
	return errors.New(failed)
}

func writePrivateFile(path string, contents []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return errors.New("could not rewrite the temporary kubeconfig")
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return errors.New("could not secure the temporary kubeconfig")
	}
	if _, err := f.Write(contents); err != nil {
		_ = f.Close()
		return errors.New("could not rewrite the temporary kubeconfig")
	}
	if err := f.Close(); err != nil {
		return errors.New("could not finish the temporary kubeconfig")
	}
	return nil
}

func transformKubeconfig(input []byte, localPort int) ([]byte, kubeTarget, error) {
	if localPort < 1 || localPort > 65535 {
		return nil, kubeTarget{}, errors.New("invalid local Kubernetes port")
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(input, &document) != nil || document == nil {
		return nil, kubeTarget{}, errors.New("kubectl returned an invalid kubeconfig")
	}
	currentContext, ok := rawString(document["current-context"])
	if !ok || currentContext == "" {
		return nil, kubeTarget{}, errors.New("kubeconfig has no active context")
	}

	contextOuter, activeContext, err := namedKubeObject(document["contexts"], currentContext, "context")
	if err != nil {
		return nil, kubeTarget{}, errors.New("kubeconfig active context is invalid")
	}
	clusterName, clusterOK := rawString(activeContext["cluster"])
	userName, userOK := rawString(activeContext["user"])
	if !clusterOK || clusterName == "" || !userOK || userName == "" {
		return nil, kubeTarget{}, errors.New("kubeconfig active context is incomplete")
	}

	clusterOuter, cluster, err := namedKubeObject(document["clusters"], clusterName, "cluster")
	if err != nil {
		return nil, kubeTarget{}, errors.New("kubeconfig active cluster is invalid")
	}
	userOuter, user, err := namedKubeObject(document["users"], userName, "user")
	if err != nil {
		return nil, kubeTarget{}, errors.New("kubeconfig active user is invalid")
	}
	if err := validateAzureCLIExec(user); err != nil {
		return nil, kubeTarget{}, err
	}

	server, ok := rawString(cluster["server"])
	if !ok {
		return nil, kubeTarget{}, errors.New("kubeconfig API server is invalid")
	}
	host, port, err := kubeAPITarget(server)
	if err != nil {
		return nil, kubeTarget{}, err
	}
	caData, ok := rawString(cluster["certificate-authority-data"])
	if !ok || caData == "" {
		return nil, kubeTarget{}, errors.New("kubeconfig does not contain an embedded cluster certificate authority")
	}
	if insecure, exists := cluster["insecure-skip-tls-verify"]; exists {
		var skip bool
		if json.Unmarshal(insecure, &skip) != nil || skip {
			return nil, kubeTarget{}, errors.New("kubeconfig must require API server TLS verification")
		}
		delete(cluster, "insecure-skip-tls-verify")
	}
	if existingName, exists := cluster["tls-server-name"]; exists {
		tlsName, valid := rawString(existingName)
		if !valid || !validRemoteDNSName(tlsName) {
			return nil, kubeTarget{}, errors.New("kubeconfig TLS server name is invalid")
		}
	} else {
		cluster["tls-server-name"], _ = json.Marshal(host)
	}
	cluster["server"], _ = json.Marshal("https://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)))
	delete(cluster, "certificate-authority")
	delete(cluster, "proxy-url")

	if document["contexts"], err = singleNamedSection(contextOuter, "context", activeContext); err != nil {
		return nil, kubeTarget{}, errors.New("could not serialize the active kubeconfig context")
	}
	if document["clusters"], err = singleNamedSection(clusterOuter, "cluster", cluster); err != nil {
		return nil, kubeTarget{}, errors.New("could not serialize the active kubeconfig cluster")
	}
	if document["users"], err = singleNamedSection(userOuter, "user", user); err != nil {
		return nil, kubeTarget{}, errors.New("could not serialize the active kubeconfig user")
	}
	rewritten, err := json.Marshal(document)
	if err != nil {
		return nil, kubeTarget{}, errors.New("could not serialize the temporary kubeconfig")
	}
	return rewritten, kubeTarget{host: host, port: port}, nil
}

func namedKubeObject(section json.RawMessage, name, objectField string) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	var entries []json.RawMessage
	if json.Unmarshal(section, &entries) != nil {
		return nil, nil, errors.New("invalid named kubeconfig section")
	}
	var matchedOuter map[string]json.RawMessage
	var matchedObject map[string]json.RawMessage
	for _, entry := range entries {
		var outer map[string]json.RawMessage
		if json.Unmarshal(entry, &outer) != nil {
			return nil, nil, errors.New("invalid named kubeconfig entry")
		}
		entryName, ok := rawString(outer["name"])
		if !ok || entryName != name {
			continue
		}
		if matchedOuter != nil {
			return nil, nil, errors.New("duplicate named kubeconfig entry")
		}
		var object map[string]json.RawMessage
		if json.Unmarshal(outer[objectField], &object) != nil || object == nil {
			return nil, nil, errors.New("invalid named kubeconfig object")
		}
		matchedOuter = outer
		matchedObject = object
	}
	if matchedOuter == nil {
		return nil, nil, errors.New("named kubeconfig entry not found")
	}
	return matchedOuter, matchedObject, nil
}

func singleNamedSection(outer map[string]json.RawMessage, objectField string, object map[string]json.RawMessage) (json.RawMessage, error) {
	encodedObject, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	outer[objectField] = encodedObject
	encodedOuter, err := json.Marshal(outer)
	if err != nil {
		return nil, err
	}
	return json.Marshal([]json.RawMessage{encodedOuter})
}

func rawString(raw json.RawMessage) (string, bool) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func kubeAPITarget(server string) (string, string, error) {
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", "", errors.New("kubeconfig API server must be a plain HTTPS DNS URL")
	}
	host := strings.ToLower(u.Hostname())
	if !validRemoteDNSName(host) {
		return "", "", errors.New("kubeconfig API server must use a remote DNS name")
	}
	port := u.Port()
	if port == "" {
		if strings.HasSuffix(u.Host, ":") {
			return "", "", errors.New("kubeconfig API server port is invalid")
		}
		port = "443"
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", "", errors.New("kubeconfig API server port is invalid")
	}
	return host, port, nil
}

func validRemoteDNSName(host string) bool {
	return host == strings.ToLower(host) && strings.Contains(host, ".") && !strings.HasSuffix(host, ".") && validDNSName(host) && !forbiddenTargetName(host)
}

func validateAzureCLIExec(user map[string]json.RawMessage) error {
	for _, field := range []string{"auth-provider", "client-certificate", "client-certificate-data", "client-key", "client-key-data", "password", "token", "tokenFile", "username"} {
		if _, exists := user[field]; exists {
			return errors.New("kubeconfig active user contains an unexpected credential source")
		}
	}
	var plugin struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
		Env     []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"env"`
	}
	if json.Unmarshal(user["exec"], &plugin) != nil {
		return errors.New("kubeconfig active user has no valid exec authentication")
	}
	if plugin.Command != "kubelogin" && plugin.Command != "kubelogin.exe" {
		return errors.New("kubeconfig active user does not use kubelogin")
	}
	if len(plugin.Args) == 0 || plugin.Args[0] != "get-token" || len(plugin.Env) != 0 {
		return errors.New("kubeconfig kubelogin configuration is unsafe")
	}
	loginMode := ""
	for i := 1; i < len(plugin.Args); i++ {
		argument := plugin.Args[i]
		var mode string
		switch {
		case argument == "-l" || argument == "--login":
			if i+1 >= len(plugin.Args) {
				return errors.New("kubeconfig kubelogin mode is invalid")
			}
			i++
			mode = plugin.Args[i]
		case strings.HasPrefix(argument, "-l="):
			mode = strings.TrimPrefix(argument, "-l=")
		case strings.HasPrefix(argument, "--login="):
			mode = strings.TrimPrefix(argument, "--login=")
		}
		if mode != "" {
			if loginMode != "" {
				return errors.New("kubeconfig kubelogin mode is ambiguous")
			}
			loginMode = mode
		}
	}
	if loginMode != "azurecli" {
		return errors.New("kubeconfig kubelogin must use the local Azure CLI login")
	}
	return nil
}
