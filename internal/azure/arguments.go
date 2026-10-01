package azure

// SSHLoginScope is the Microsoft Entra application that issues Azure Linux VM
// SSH certificates. Signing in for it satisfies that application's
// interactive requirements before connect requests a certificate silently.
const SSHLoginScope = "ce6ff14a-7fdc-4685-bbe0-f6afdfcfa8e0/.default"

// LoginArguments returns Azure CLI arguments for an interactive local login.
func LoginArguments(tenant string, deviceCode, ssh bool) []string {
	args := []string{"login", "--output", "none"}
	if tenant != "" {
		args = append(args, "--tenant", tenant)
	}
	if deviceCode {
		args = append(args, "--use-device-code")
	}
	if ssh {
		args = append(args, "--scope", SSHLoginScope)
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
