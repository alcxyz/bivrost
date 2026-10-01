//go:build windows

package authbrowser

import (
	"io"
	"os"
)

// Terminal returns the attached console, or os.Stderr without one.
func Terminal() io.Writer {
	if console, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		return console
	}
	return os.Stderr
}
