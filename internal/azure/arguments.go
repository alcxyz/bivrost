package azure

// LoginArguments returns Azure CLI arguments for an interactive local login.
func LoginArguments(tenant string) []string {
	args := []string{"login", "--output", "none"}
	if tenant != "" {
		args = append(args, "--tenant", tenant)
	}
	return args
}

// AKSCredentialsArguments returns Azure CLI arguments for a session-owned kubeconfig.
func AKSCredentialsArguments(name, resourceGroup, subscription, path string) []string {
	return []string{
		"aks", "get-credentials",
		"--subscription", subscription,
		"--resource-group", resourceGroup,
		"--name", name,
		"--file", path,
		"--format", "exec",
		"--only-show-errors",
	}
}
