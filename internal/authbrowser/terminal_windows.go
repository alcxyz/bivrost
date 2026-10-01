//go:build windows

package authbrowser

import (
	"io"
	"os"
)

// Terminal returns the attached console, or nil without one.
func Terminal() io.Writer {
	if terminal, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		return terminal
	}
	return nil
}
