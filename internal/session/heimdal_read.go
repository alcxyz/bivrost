package session

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/alcxyz/bivrost/internal/azure"
	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/heimdal"
)

func fetchHeimdalRoutes(ctx context.Context, c profile.Profile) ([]string, error) {
	return fetchHeimdalRoutesWith(ctx, c, azure.TerraformBlobEndpoint, azure.ReadHeimdalBlob)
}

func fetchHeimdalRoutesWith(ctx context.Context, c profile.Profile,
	endpointFor func(context.Context, string, []string) (string, error),
	readBlob func(context.Context, azure.TerraformBackend, string, string, string, []string, int) ([]byte, error),
) ([]string, error) {
	if c.Heimdal == nil {
		return nil, nil
	}
	source := *c.Heimdal
	if err := source.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	// Preserve the normal Azure CLI identity/configuration while pinning its
	// network path to the already established session.
	environment := proxyEnvironment(os.Environ(), c.ProxyURL())
	endpoint, err := endpointFor(ctx, source.Account, environment)
	if err != nil {
		return nil, err
	}
	target := azure.TerraformBackend{Subscription: source.Subscription, Account: source.Account, Container: source.ContainerName()}
	prefix := source.BlobPrefix()
	data, err := readBlob(ctx, target, endpoint, prefix+"/current.json", c.ProxyURL(), environment, heimdal.MaxPointerSize)
	if err != nil {
		return nil, err
	}
	pointer, err := heimdal.DecodePointer(data, source.Environment)
	if err != nil {
		return nil, errors.New("Heimdal pointer validation failed")
	}
	data, err = readBlob(ctx, target, endpoint, prefix+"/revisions/"+pointer.Revision+".json", c.ProxyURL(), environment, heimdal.MaxDocumentSize)
	if err != nil {
		return nil, err
	}
	document, err := heimdal.Decode(data, source.Environment, pointer.Revision, time.Now())
	if err != nil {
		return nil, errors.New("Heimdal revision validation failed (schema, digest, environment, routes or validity)")
	}
	return append([]string(nil), document.PrivateHosts...), nil
}
