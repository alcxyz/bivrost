package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
)

type commandKind int

const (
	commandHelp commandKind = iota
	commandEnvironments
	commandConfigInit
	commandConnect
	commandVersion
	commandSSH
	commandACRProxy
	commandACRConnect
	commandACRLogin
	commandACREnable
	commandACRDoctor
	commandLogin
	commandDoctor
)

type parsedCommand struct {
	kind        commandKind
	environment string
	configPath  string
	acr         bool
	noPull      bool
	noLogin     bool
	tenant      string
	debug       bool
	helpTopic   string
}

var environmentPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func run(args []string) (resultErr error) {
	command, err := parseCommand(args)
	if err != nil {
		return err
	}
	if command.kind == commandEnvironments {
		return listEnvironments(os.Stdout)
	}
	if command.kind == commandConfigInit {
		path, err := initSettings()
		if err != nil {
			return err
		}
		fmt.Printf("Created default settings at %q\n", path)
		return nil
	}
	if command.kind == commandVersion {
		fmt.Println(buildVersion())
		return nil
	}
	if command.kind == commandHelp {
		fmt.Print(helpText(command.helpTopic))
		return nil
	}

	ctx := context.Background()
	stop := func() {}
	signals := terminationSignals(true)
	if command.kind == commandConnect || command.kind == commandACRConnect {
		// Platform sessions manage Ctrl+C themselves: cancel setup, but let the
		// foreground local shell handle interrupts once it is running.
		signals = terminationSignals(false)
	}
	if len(signals) > 0 {
		ctx, stop = signal.NotifyContext(ctx, signals...)
	}
	defer stop()

	if command.debug {
		debugCtx, logger, err := startDiagnostics(ctx)
		if err != nil {
			return err
		}
		ctx = debugCtx
		fmt.Fprintf(os.Stderr, "Diagnostic log: %q\n", logger.path)
		finish := diagnosticStep(ctx, eventCommand)
		defer func() {
			finish(resultErr)
			if err := logger.close(); err != nil {
				fmt.Fprintln(os.Stderr, "Diagnostic log could not be completed.")
				if resultErr == nil {
					resultErr = err
				}
			}
		}()
	}

	if command.kind == commandACREnable {
		return enableSessionACR(ctx)
	}
	if command.kind == commandLogin {
		return localAzureLogin(ctx, command.tenant)
	}

	if command.kind == commandDoctor {
		return runDoctor(ctx, command, os.Stdout)
	}

	finishConfig := diagnosticStep(ctx, eventConfiguration)
	c, err := loadEnvironment(command.environment, command.configPath)
	finishConfig(err)
	if err != nil {
		return err
	}
	if command.kind == commandACRProxy || command.kind == commandACRConnect || command.kind == commandACRLogin || command.kind == commandACRDoctor {
		_, err := loadUserSettings()
		if err != nil {
			return err
		}
	}
	if c.RequiresPIM && (command.kind == commandConnect || command.kind == commandSSH || command.kind == commandACRConnect) {
		fmt.Println("This environment requires PIM activation. Activate your eligible access before connecting; this tool does not grant or activate permissions.")
	}

	switch command.kind {
	case commandConnect:
		c.Environment = command.environment
		if c.Environment == "" {
			c.Environment = "custom-profile"
		}
		if err := c.validatePlatform(); err != nil {
			return err
		}
		if command.acr {
			return connect(ctx, c, command.noLogin)
		}
		return platformConnect(ctx, c)
	case commandSSH:
		if err := c.validateConnection(); err != nil {
			return err
		}
		return interactiveSSH(ctx, c)
	case commandACRProxy, commandACRConnect, commandACRLogin, commandACRDoctor:
		if err := c.validateACR(); err != nil {
			return err
		}
	}

	switch command.kind {
	case commandACRProxy:
		return serveProxy(ctx, c)
	case commandACRConnect:
		return connect(ctx, c, command.noLogin)
	case commandACRLogin:
		return acrLogin(ctx, c)
	case commandACRDoctor:
		return doctor(ctx, c)
	default:
		return errors.New("internal error: unsupported command")
	}
}

func parseCommand(args []string) (parsedCommand, error) {
	if len(args) > 1 && args[0] == "help" {
		topic := strings.Join(args[1:], " ")
		topic = canonicalHelpTopic(topic)
		if helpText(topic) == "" {
			return parsedCommand{}, errors.New("unknown help topic; use bivrost --help")
		}
		return parsedCommand{kind: commandHelp, helpTopic: topic}, nil
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		topic := canonicalHelpTopic(args[0])
		if helpText(topic) != "" {
			return parsedCommand{kind: commandHelp, helpTopic: topic}, nil
		}
	}
	if len(args) == 3 && args[0] == "config" && args[1] == "init" && (args[2] == "--help" || args[2] == "-h") {
		return parsedCommand{kind: commandHelp, helpTopic: "config init"}, nil
	}

	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		return parsedCommand{kind: commandHelp}, nil
	}

	switch args[0] {
	case "environments", "env", "envs":
		if len(args) != 1 {
			return parsedCommand{}, errors.New("environments does not accept arguments")
		}
		return parsedCommand{kind: commandEnvironments}, nil
	case "config":
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			return parsedCommand{kind: commandHelp}, nil
		}
		if len(args) != 2 || args[1] != "init" {
			return parsedCommand{}, errors.New("use bivrost config init without additional arguments")
		}
		return parsedCommand{kind: commandConfigInit}, nil
	case "version", "v", "-v", "--version":
		if len(args) != 1 {
			return parsedCommand{}, errors.New("version does not accept arguments")
		}
		return parsedCommand{kind: commandVersion}, nil
	case "doctor":
		return parseConnectionCommand(commandDoctor, "doctor", args[1:], false)
	case "connect":
		return parseConnectionCommand(commandConnect, "connect", args[1:], true)
	case "ssh":
		return parseConnectionCommand(commandSSH, "ssh", args[1:], false)
	case "acr":
		if len(args) == 1 {
			return parsedCommand{}, errors.New("missing ACR command; use bivrost help")
		}
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h" || args[1] == "help") {
			return parsedCommand{kind: commandHelp, helpTopic: "acr"}, nil
		}
		if args[1] == "enable" {
			if len(args) == 3 && (args[2] == "--help" || args[2] == "-h") {
				return parsedCommand{kind: commandHelp, helpTopic: "acr enable"}, nil
			}
			if len(args) != 2 {
				return parsedCommand{}, errors.New("acr enable uses the active session and does not accept options")
			}
			return parsedCommand{kind: commandACREnable}, nil
		}
		kind, ok := map[string]commandKind{
			"proxy":   commandACRProxy,
			"connect": commandACRConnect,
			"login":   commandACRLogin,
			"doctor":  commandACRDoctor,
		}[args[1]]
		if !ok {
			return parsedCommand{}, fmt.Errorf("unknown ACR command %q; use bivrost help", args[1])
		}
		return parseConnectionCommand(kind, "acr "+args[1], args[2:], kind == commandACRConnect)
	case "login":
		return parseLoginCommand(args[1:])
	case "help":
		return parsedCommand{}, errors.New("help does not accept arguments")
	default:
		return parsedCommand{}, fmt.Errorf("unknown command %q; use bivrost help", args[0])
	}
}

func parseConnectionCommand(kind commandKind, name string, args []string, allowNoLogin bool) (parsedCommand, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	debug := flags.Bool("debug", false, "write a bounded local diagnostic log")
	flags.BoolVar(debug, "d", false, "write a bounded local diagnostic log")
	environment := flags.String("env", "", "environment profile name")
	flags.StringVar(environment, "e", "", "environment profile name")
	configPath := flags.String("config", "", "path to a connection configuration")
	flags.StringVar(configPath, "c", "", "path to a connection configuration")
	var noPull bool
	if kind == commandDoctor {
		flags.BoolVar(&noPull, "no-pull", false, "skip the diagnostic image pull")
	}
	var acr bool
	if kind == commandConnect {
		flags.BoolVar(&acr, "acr", false, "enable Podman registry access")
	}
	var noLogin *bool
	if allowNoLogin {
		noLogin = flags.Bool("no-login", false, "connect without logging Podman into ACR")
		flags.BoolVar(noLogin, "n", false, "connect without logging Podman into ACR")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parsedCommand{kind: commandHelp, helpTopic: name}, nil
		}
		return parsedCommand{}, fmt.Errorf("invalid %s options: %w", name, err)
	}
	if flags.NArg() != 0 {
		return parsedCommand{}, fmt.Errorf("%s does not accept positional arguments", name)
	}

	seenEnvironment := false
	seenConfig := false
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "env", "e":
			seenEnvironment = true
		case "config", "c":
			seenConfig = true
		}
	})
	if seenEnvironment && seenConfig {
		return parsedCommand{}, errors.New("--env and --config are mutually exclusive")
	}
	if !seenEnvironment && !seenConfig && kind != commandDoctor {
		return parsedCommand{}, fmt.Errorf("%s requires --env NAME or --config PATH", name)
	}
	if seenEnvironment && !environmentPattern.MatchString(*environment) {
		return parsedCommand{}, errors.New("--env must start with a lowercase letter, contain only lowercase letters, digits, or hyphens, and be at most 63 characters")
	}
	if seenConfig && strings.TrimSpace(*configPath) == "" {
		return parsedCommand{}, errors.New("--config requires a non-empty path")
	}

	command := parsedCommand{noPull: noPull, acr: acr, kind: kind, environment: *environment, configPath: *configPath, debug: *debug}
	if noLogin != nil {
		command.noLogin = *noLogin
	}
	if kind == commandConnect && command.noLogin && !acr {
		return parsedCommand{}, errors.New("--no-login requires --acr")
	}
	return command, nil
}

func parseLoginCommand(args []string) (parsedCommand, error) {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	debug := flags.Bool("debug", false, "write a bounded local diagnostic log")
	flags.BoolVar(debug, "d", false, "write a bounded local diagnostic log")
	tenant := flags.String("tenant", "", "Azure tenant to authenticate against")
	flags.StringVar(tenant, "t", "", "Azure tenant to authenticate against")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return parsedCommand{kind: commandHelp, helpTopic: "login"}, nil
		}
		return parsedCommand{}, fmt.Errorf("invalid login options: %w", err)
	}
	if flags.NArg() != 0 {
		return parsedCommand{}, errors.New("login does not accept positional arguments")
	}
	seenTenant := false
	flags.Visit(func(f *flag.Flag) { seenTenant = seenTenant || f.Name == "tenant" || f.Name == "t" })
	if seenTenant && strings.TrimSpace(*tenant) == "" {
		return parsedCommand{}, errors.New("--tenant requires a non-empty value")
	}
	return parsedCommand{kind: commandLogin, tenant: *tenant, debug: *debug}, nil
}

func resolveConfigPath(environment, explicitPath string) (string, error) {
	if environment != "" && explicitPath != "" {
		return "", errors.New("--env and --config are mutually exclusive")
	}
	if explicitPath != "" {
		return explicitPath, nil
	}
	if !environmentPattern.MatchString(environment) {
		return "", errors.New("invalid environment name")
	}
	root, err := userConfigRoot()
	if err != nil {
		return "", fmt.Errorf("locate user configuration directory: %w", err)
	}
	return filepath.Join(root, "bivrost", "environments", environment+".json"), nil
}

func userConfigRoot() (string, error) {
	if root := os.Getenv("XDG_CONFIG_HOME"); root != "" {
		if !filepath.IsAbs(root) {
			return "", errors.New("XDG_CONFIG_HOME must be an absolute path")
		}
		return root, nil
	}
	return os.UserConfigDir()
}

func azureLoginArguments(tenant string) []string {
	args := []string{"login", "--output", "none"}
	if tenant != "" {
		args = append(args, "--tenant", tenant)
	}
	return args
}

func localAzureLogin(ctx context.Context, tenant string) (resultErr error) {
	finish := diagnosticStep(ctx, eventAzureLogin)
	defer func() { finish(resultErr) }()
	cmd, err := azureCommand(ctx, azureLoginArguments(tenant)...)
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
