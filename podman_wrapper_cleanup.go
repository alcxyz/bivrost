package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"time"
)

func cleanupPodmanWrapper(directory string, out io.Writer) {
	attempts := 1
	if runtime.GOOS == "windows" {
		// A running executable is locked on Windows. Allow an interrupted child
		// to finish, but never hold session teardown open indefinitely.
		attempts = 21
	}
	if err := removeWrapperWithRetry(directory, attempts, 100*time.Millisecond, os.RemoveAll); err != nil {
		// Do not expose raw OS errors or command arguments.
		fmt.Fprintf(out, "Bivrost could not remove its Podman wrapper directory %q. After its Podman commands finish, remove this directory manually.\n", directory)
	}
}

func removeWrapperWithRetry(directory string, attempts int, delay time.Duration, remove func(string) error) error {
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if err = remove(directory); err == nil {
			return nil
		}
		if attempt+1 < attempts {
			time.Sleep(delay)
		}
	}
	return err
}
