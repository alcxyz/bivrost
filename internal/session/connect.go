package session

import (
	"context"
	"errors"

	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
)

func connect(ctx context.Context, c profile.Profile, noLogin bool) (resultErr error) {
	finish := diagnostics.Step(ctx, diagnostics.EventACRConnect)
	defer func() { finish(resultErr) }()
	if err := c.ValidateACR(); err != nil {
		return err
	}
	if !noLogin && !profile.ValidResourceName(c.RegistrySubscription) {
		return errors.New("set registry_subscription to the subscription containing ACR")
	}
	if err := c.ValidatePlatform(); err != nil {
		return err
	}
	if err := requirePodmanForACR(ctx, c); err != nil {
		return err
	}
	c.ACRSession = true
	c.SkipRegistryLogin = noLogin
	return platformConnect(ctx, c)
}
