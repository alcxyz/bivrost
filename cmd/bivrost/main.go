// Bivrost provides local platform access through a managed connection.
package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/alcxyz/bivrost/internal/authbrowser"
	"github.com/alcxyz/bivrost/internal/podman"
	"github.com/alcxyz/bivrost/internal/session"
)

func main() {
	// Azure CLI child processes reach Bivrost through BROWSER for sign-in pages.
	if authbrowser.Active(os.Args[1:]) {
		os.Exit(authbrowser.Run(os.Args[1:], authbrowser.Terminal()))
	}
	if strings.EqualFold(filepath.Base(os.Args[0]), "podman") || strings.EqualFold(filepath.Base(os.Args[0]), "podman.exe") {
		os.Exit(podman.RunWrapper())
	}
	log.SetFlags(0)
	if err := session.Run(os.Args[1:], buildVersion()); err != nil {
		if errors.Is(err, session.ErrSwitchAccepted) {
			os.Exit(session.SwitchShellExitCode)
		}
		var exit *session.ExitError
		if errors.As(err, &exit) {
			// A command's own failure was already reported by the command.
			if exit.Err != nil {
				log.Print("bivrost: ", exit.Err)
			}
			os.Exit(exit.Code)
		}
		log.Print("bivrost: ", err)
		os.Exit(1)
	}
}
