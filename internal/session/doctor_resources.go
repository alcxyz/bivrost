package session

import (
	"context"
	"os/exec"
	"time"

	profile "github.com/alcxyz/bivrost/internal/config"
)

func doctorResourceChecks(ctx context.Context, c profile.Profile, session *doctorSessionStatus, kubectlAvailable bool, report func(string, string, string)) {
	if session == nil {
		report("NOT VERIFIED", "Bastion/VM transport", "requires an active matching Bivrost session")
	} else {
		report("OK", "Bastion/VM transport", "the authenticated connection owner is active; transport processes are monitored")
	}
	if c.AKS == nil {
		return
	}
	if session == nil || session.Kubeconfig == "" || !kubectlAvailable {
		report("NOT VERIFIED", "Kubernetes API", "requires kubectl and an active matching Bivrost session")
		report("NOT VERIFIED", "Kubernetes list nodes", "requires a live read-only request in the matching session")
		return
	}
	// Explicitly select the owner's isolated kubeconfig, never the caller's
	// ambient Kubernetes context. Discard responses and authentication errors.
	if doctorKubernetesRead(ctx, session.Kubeconfig, "/version") {
		report("OK", "Kubernetes API", "API version endpoint reachable using the session kubeconfig")
	} else {
		report("NOT VERIFIED", "Kubernetes API", "version request failed; check session connectivity and authentication")
	}
	// A paginated read verifies this exact permission without fetching every node
	// or implying authorization for unrelated resources or operations.
	if doctorKubernetesRead(ctx, session.Kubeconfig, "/api/v1/nodes?limit=1") {
		report("OK", "Kubernetes list nodes", "read-only node list request succeeded; other Kubernetes permissions are not inferred")
	} else {
		report("NOT VERIFIED", "Kubernetes list nodes", "node list request failed; connectivity, authentication, or list permission may prevent it")
	}
}

func doctorKubernetesRead(ctx context.Context, kubeconfig, path string) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "kubectl", "--kubeconfig", kubeconfig, "--request-timeout=8s", "get", "--raw="+path)
	return cmd.Run() == nil
}
