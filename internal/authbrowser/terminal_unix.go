//go:build !windows

package authbrowser

import (
	"io"
	"os"
)

// Terminal returns the controlling terminal, or os.Stderr without one.
func Terminal() io.Writer {
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		return tty
	}
	return os.Stderr
}
