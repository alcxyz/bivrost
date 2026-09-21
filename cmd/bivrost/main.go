// Bivrost provides local platform access through a managed connection.
package main

import (
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/alcxyz/bivrost/internal/podman"
	"github.com/alcxyz/bivrost/internal/session"
)

func main() {
	if strings.EqualFold(filepath.Base(os.Args[0]), "podman") || strings.EqualFold(filepath.Base(os.Args[0]), "podman.exe") {
		os.Exit(podman.RunWrapper())
	}
	log.SetFlags(0)
	if err := session.Run(os.Args[1:], buildVersion()); err != nil {
		if errors.Is(err, session.ErrSwitchAccepted) {
			os.Exit(session.SwitchShellExitCode)
		}
		log.Print("bivrost: ", err)
		os.Exit(1)
	}
}
