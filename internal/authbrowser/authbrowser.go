// Package authbrowser decides whether delegated Azure CLI authentication may
// open a browser, and which browser it uses.
//
// Azure CLI opens sign-in pages through Python's webbrowser module, which
// honors the BROWSER environment variable. Bivrost points BROWSER at its own
// executable for Azure CLI child processes, so every browser request returns to
// Bivrost instead of the operating system default.
package authbrowser

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"

	profile "github.com/alcxyz/bivrost/internal/config"
)

const (
	browserVariable = "BROWSER"
	modeVariable    = "BIVROST_AUTH_BROWSER"
	signalVariable  = "BIVROST_AUTH_SIGNAL"
	modeRefuse      = "refuse"
	modeLaunch      = "launch"
)

// Active reports whether Azure CLI started this process to open a sign-in page:
// Python's webbrowser module passes exactly one web address.
func Active(args []string) bool {
	return os.Getenv(modeVariable) != "" && len(args) == 1 &&
		(strings.HasPrefix(args[0], "https://") || strings.HasPrefix(args[0], "http://"))
}

// Refuse routes browser requests from cmd back to Bivrost, which opens nothing.
// When signal is not empty, the first refused request creates that file so the
// caller can stop waiting for a sign-in that will never complete.
func Refuse(cmd *exec.Cmd, signal string) error {
	return route(cmd, modeRefuse, signal)
}

// Launch routes browser requests from cmd to the configured authentication
// browser. Callers use it only for explicit sign-in commands.
func Launch(cmd *exec.Cmd) error {
	return route(cmd, modeLaunch, "")
}

// Requested reports whether a refused browser request created signal.
func Requested(signal string) bool {
	_, err := os.Lstat(signal)
	return err == nil
}

func route(cmd *exec.Cmd, mode, signal string) error {
	executable, err := os.Executable()
	if err != nil {
		return errors.New("could not locate the Bivrost executable to control Azure CLI browser sign-in")
	}
	// webbrowser splits BROWSER on the path list separator and treats %s as a
	// command template; either would let Azure CLI run something else.
	if strings.ContainsRune(executable, os.PathListSeparator) || strings.Contains(executable, "%") || !plain(executable) {
		return errors.New("the Bivrost executable path cannot be used to control Azure CLI browser sign-in; install Bivrost in a path without " + string(os.PathListSeparator) + " or % characters")
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	env = withoutVariables(env, browserVariable, modeVariable, signalVariable)
	env = append(env, browserVariable+"="+executable, modeVariable+"="+mode)
	if signal != "" {
		env = append(env, signalVariable+"="+signal)
	}
	cmd.Env = env
	return nil
}

func withoutVariables(env []string, names ...string) []string {
	result := make([]string, 0, len(env))
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		matched := false
		for _, name := range names {
			if key == name || runtime.GOOS == "windows" && strings.EqualFold(key, name) {
				matched = true
				break
			}
		}
		if !matched {
			result = append(result, entry)
		}
	}
	return result
}

// Run handles one browser request from Azure CLI. It always returns exit
// status 0: Python's webbrowser module treats failure as permission to try the
// operating system default browser, which is exactly what this guard prevents.
func Run(args []string, stderr io.Writer) int {
	address := ""
	if len(args) == 1 && validAddress(args[0]) {
		address = args[0]
	}
	switch os.Getenv(modeVariable) {
	case modeLaunch:
		if address == "" {
			fmt.Fprintln(stderr, "Bivrost refused an invalid Azure sign-in address.")
			return 0
		}
		launch(address, stderr)
	default:
		// Never print or record the address: it carries sign-in request parameters.
		if signal := os.Getenv(signalVariable); signal != "" {
			if file, err := os.OpenFile(signal, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err == nil {
				_ = file.Close()
			}
		}
	}
	return 0
}

func launch(address string, stderr io.Writer) {
	browser, err := profile.LoadAuthenticationBrowser()
	if err == nil && browser == nil {
		err = errors.New("authentication_browser is not configured")
	}
	if err == nil {
		err = start(*browser, address)
	}
	if err != nil {
		// Azure CLI keeps waiting for the redirect, so a manually opened page
		// still completes this sign-in. The address is shown only on the terminal.
		fmt.Fprintf(stderr, "Bivrost could not open the configured authentication browser: %v.\n", err)
		fmt.Fprintln(stderr, "Open this address in the intended browser profile to continue, or press Ctrl+C and run bivrost login --device-code:")
		fmt.Fprintln(stderr, address)
		return
	}
	fmt.Fprintln(stderr, "Opened Azure sign-in in the configured authentication browser.")
}

func start(browser profile.AuthenticationBrowser, address string) error {
	executable, args := browser.Command(address)
	path, err := exec.LookPath(executable)
	if err != nil {
		return errors.New("the browser executable was not found")
	}
	cmd := exec.Command(path, args...)
	// The browser outlives this short-lived launcher. Detaching it also keeps
	// Azure CLI from waiting on a browser that was not already running.
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return errors.New("the browser could not be started")
	}
	_ = cmd.Process.Release()
	return nil
}

func validAddress(address string) bool {
	if !plain(address) {
		return false
	}
	parsed, err := url.Parse(address)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != ""
}

func plain(value string) bool {
	for _, r := range value {
		if r < 32 || r == 127 {
			return false
		}
	}
	return value != ""
}
