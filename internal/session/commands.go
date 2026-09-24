// Package session coordinates command execution and owns connection lifecycles.
package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/alcxyz/bivrost/internal/azure"
	"github.com/alcxyz/bivrost/internal/cli"
	profile "github.com/alcxyz/bivrost/internal/config"
	"github.com/alcxyz/bivrost/internal/diagnostics"
)

func Run(args []string, version string) (resultErr error) {
	command, err := cli.Parse(args)
	if err != nil {
		return err
	}
	if command.Kind == cli.Environments {
		return profile.ListEnvironments(os.Stdout)
	}
	if command.Kind == cli.ConfigInit {
		path, err := profile.InitSettings()
		if err != nil {
			return err
		}
		fmt.Printf("Created default settings at %q\n", path)
		return nil
	}
	if command.Kind == cli.Version {
		fmt.Println(version)
		return nil
	}
	if command.Kind == cli.Help {
		fmt.Print(cli.HelpText(command.HelpTopic))
		return nil
	}

	ctx := context.Background()
	stop := func() {}
	signals := terminationSignals(true)
	if command.Kind == cli.Connect || command.Kind == cli.ACRConnect {
		// Platform sessions manage Ctrl+C themselves: cancel setup, but let the
		// foreground local shell handle interrupts once it is running.
		signals = terminationSignals(false)
	}
	if len(signals) > 0 {
		ctx, stop = signal.NotifyContext(ctx, signals...)
	}
	defer stop()
	if command.Kind == cli.Subscriptions {
		subscriptions, err := azure.DiscoverSubscriptions(ctx, command.Refresh)
		if err != nil {
			return err
		}
		return cli.WriteSubscriptions(os.Stdout, subscriptions)
	}

	if command.Debug {
		debugCtx, logger, err := diagnostics.Start(ctx)
		if err != nil {
			return err
		}
		ctx = debugCtx
		fmt.Fprintf(os.Stderr, "Diagnostic log: %q\n", logger.Path())
		finish := diagnostics.Step(ctx, diagnostics.EventCommand)
		defer func() {
			if errors.Is(resultErr, ErrSwitchAccepted) {
				finish(nil)
			} else {
				finish(resultErr)
			}
			if err := logger.Close(); err != nil {
				fmt.Fprintln(os.Stderr, "Diagnostic log could not be completed.")
				if resultErr == nil {
					resultErr = err
				}
			}
		}()
	}

	if command.Kind == cli.Switch {
		return runSwitch(ctx, command)
	}
	if command.Kind == cli.ACREnable {
		return enableSessionACR(ctx)
	}
	if command.Kind == cli.Login {
		return localAzureLogin(ctx, command.Tenant)
	}

	if command.Kind == cli.Doctor {
		return runDoctor(ctx, command, os.Stdout)
	}

	finishConfig := diagnostics.Step(ctx, diagnostics.EventConfiguration)
	c, err := profile.LoadEnvironment(command.Environment, command.ConfigPath)
	finishConfig(err)
	if err != nil {
		return err
	}
	c, err = withPrivateHosts(c, command.PrivateHosts)
	if err != nil {
		return err
	}
	if command.Kind == cli.ACRProxy || command.Kind == cli.ACRConnect || command.Kind == cli.ACRLogin || command.Kind == cli.ACRDoctor {
		_, err := profile.LoadUserSettings()
		if err != nil {
			return err
		}
	}
	if c.RequiresPIM && (command.Kind == cli.Connect || command.Kind == cli.SSH || command.Kind == cli.ACRConnect) {
		fmt.Println("This environment requires PIM activation. Activate your eligible access before connecting; this tool does not grant or activate permissions.")
	}

	switch command.Kind {
	case cli.Connect:
		c.Environment = command.Environment
		if c.Environment == "" {
			c.Environment = "custom-profile"
		}
		if err := c.ValidatePlatform(); err != nil {
			return err
		}
		if command.ACR {
			return connect(ctx, c, command.NoLogin)
		}
		return platformConnect(ctx, c)
	case cli.SSH:
		if err := c.ValidateConnection(); err != nil {
			return err
		}
		return interactiveSSH(ctx, c)
	case cli.ACRProxy, cli.ACRConnect, cli.ACRLogin, cli.ACRDoctor:
		if err := c.ValidateACR(); err != nil {
			return err
		}
	}

	switch command.Kind {
	case cli.ACRProxy:
		return serveProxy(ctx, c)
	case cli.ACRConnect:
		return connect(ctx, c, command.NoLogin)
	case cli.ACRLogin:
		return acrLogin(ctx, c)
	case cli.ACRDoctor:
		return doctor(ctx, c)
	default:
		return errors.New("internal error: unsupported command")
	}
}

func withPrivateHosts(c profile.Profile, additional []string) (profile.Profile, error) {
	if len(additional) == 0 {
		return c, nil
	}
	hosts := append([]string(nil), c.PrivateHosts...)
	seen := make(map[string]struct{}, len(c.PrivateHosts)+len(additional))
	for _, host := range c.PrivateHosts {
		seen[host] = struct{}{}
	}
	for _, host := range additional {
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}
	c.PrivateHosts = hosts
	if err := c.ValidatePrivateHosts(); err != nil {
		return profile.Profile{}, err
	}
	return c, nil
}

func localAzureLogin(ctx context.Context, tenant string) (resultErr error) {
	finish := diagnostics.Step(ctx, diagnostics.EventAzureLogin)
	defer func() { finish(resultErr) }()
	cmd, err := azure.Command(ctx, azure.LoginArguments(tenant)...)
	if err != nil {
		return err
	}
	// This logs in the local Azure CLI. It does not authenticate an Azure CLI
	// inside the VM reached by `bivrost ssh`.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	prepareInteractive(cmd)
	if err := cmd.Run(); err != nil {
		return errors.New("Azure login failed")
	}
	return nil
}
