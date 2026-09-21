package podman

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
)

func executeWrappedPodman(executable string, args []string) int {
	// Windows delivers console interrupts to both the wrapper and its child.
	// Keep waiting for Podman's cleanup and exit status instead of exiting on
	// the wrapper's copy of the event. Forwarding it would interrupt Podman twice.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)

	command := exec.Command(executable, args...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		if failure, ok := err.(*exec.ExitError); ok {
			return failure.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "bivrost: cannot execute the session Podman binary")
		return 125
	}
	return 0
}
