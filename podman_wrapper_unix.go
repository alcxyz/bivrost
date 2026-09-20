//go:build !windows

package main

import (
	"fmt"
	"os"
	"syscall"
)

func executeWrappedPodman(executable string, args []string) int {
	// Replacing this process preserves terminal handling, signals and exit status.
	if err := syscall.Exec(executable, append([]string{executable}, args...), os.Environ()); err != nil {
		fmt.Fprintln(os.Stderr, "bivrost: cannot execute the session Podman binary")
		return 125
	}
	return 0
}
