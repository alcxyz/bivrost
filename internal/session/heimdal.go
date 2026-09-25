package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/alcxyz/bivrost/internal/azure"
	"github.com/alcxyz/bivrost/internal/cli"
	"github.com/alcxyz/bivrost/internal/heimdal"
)

func runHeimdalInit(ctx context.Context, command cli.Command, out io.Writer) error {
	// Construct and validate all data before performing a remote write.
	document, pointer, revision, err := heimdal.Create(command.Environment, command.PrivateHosts, time.Now(), command.MetadataValidity)
	if err != nil {
		return err
	}
	if !heimdal.ValidPrefix(command.MetadataPrefix) {
		return errors.New("invalid Heimdal blob prefix")
	}
	environment := os.Environ()
	if os.Getenv("BIVROST_SESSION") != "" || os.Getenv("BIVROST_CONTROL_FILE") != "" {
		if os.Getenv("BIVROST_SESSION") == "" || os.Getenv("BIVROST_CONTROL_FILE") == "" {
			return errors.New("incomplete active session; reconnect before publishing metadata")
		}
		status, err := currentDoctorSession(ctx)
		if err != nil {
			return errors.New("active session unavailable; reconnect before publishing metadata")
		}
		if err := checkProxy(ctx, status.Config, ""); err != nil {
			return errors.New("active proxy does not match the session; reconnect before publishing metadata")
		}
		environment = proxyEnvironment(environment, status.Config.ProxyURL())
	}
	endpoint, err := azure.TerraformBlobEndpoint(ctx, command.Account, environment)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "bivrost-heimdal-")
	if err != nil {
		return errors.New("cannot create private metadata staging directory")
	}
	defer os.RemoveAll(directory)
	revisionFile := filepath.Join(directory, "revision.json")
	pointerFile := filepath.Join(directory, "current.json")
	if err := os.WriteFile(revisionFile, document, 0600); err != nil {
		return errors.New("cannot stage metadata revision")
	}
	if err := os.WriteFile(pointerFile, pointer, 0600); err != nil {
		return errors.New("cannot stage metadata pointer")
	}
	target := azure.TerraformBackend{Subscription: command.Subscription, Account: command.Account, Container: command.Container}
	if err := azure.PublishInitialHeimdal(ctx, target, endpoint, command.MetadataPrefix, revision, revisionFile, pointerFile, environment); err != nil {
		return err
	}
	fmt.Fprintln(out, "Heimdal initial metadata published.")
	fmt.Fprintf(out, "Source: %s/%s/%s/current.json\n", endpoint, command.Container, command.MetadataPrefix)
	fmt.Fprintf(out, "Revision: %s\n", revision)
	fmt.Fprintln(out, "Configure the profile's heimdal source to fetch this metadata on connect; init does not change local profiles.")
	return nil
}
