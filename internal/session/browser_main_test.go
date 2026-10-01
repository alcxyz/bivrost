package session

import (
	"os"
	"testing"

	"github.com/alcxyz/bivrost/internal/authbrowser"
)

// Azure CLI fakes reach this test binary through BROWSER, as they would reach
// the Bivrost executable in production.
func TestMain(m *testing.M) {
	if authbrowser.Active(os.Args[1:]) {
		os.Exit(authbrowser.Run(os.Args[1:], os.Stderr))
	}
	os.Exit(m.Run())
}
