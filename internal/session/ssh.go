package session

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strconv"
	"strings"

	profile "github.com/alcxyz/bivrost/internal/config"
)

func baseSSHArguments(c profile.Profile, sshConfig, stateRoot string, port int) []string {
	// A stable alias separates VM identities from ephemeral localhost ports.
	sum := sha256.Sum256([]byte(strings.ToLower(c.VMResourceID)))
	args := []string{"-F", sshConfig, "-p", strconv.Itoa(port),
		"-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3",
		"-o", "ConnectTimeout=15", "-o", "BatchMode=yes", "-o", "ForwardAgent=no",
		"-o", "StrictHostKeyChecking=accept-new", "-o", "HostKeyAlias=bivrost-" + hex.EncodeToString(sum[:16]),
		"-o", "UserKnownHostsFile=\"" + strings.ReplaceAll(filepath.ToSlash(filepath.Join(stateRoot, "known_hosts")), "\"", "\\\"") + "\""}
	if c.SSHUser != "" {
		args = append(args, "-l", c.SSHUser, "-i", c.IdentityFile, "-o", "IdentitiesOnly=yes")
	}
	return args
}

func sshArguments(c profile.Profile, sshConfig, stateRoot string, port int) []string {
	return append(baseSSHArguments(c, sshConfig, stateRoot, port), "-N", "-T", "-D", profile.Loopback(c.SOCKSPort), "127.0.0.1")
}
func interactiveSSHArguments(c profile.Profile, sshConfig, stateRoot string, port int) []string {
	return append(baseSSHArguments(c, sshConfig, stateRoot, port), "-tt", "127.0.0.1")
}
