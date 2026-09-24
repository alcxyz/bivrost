// Package cli parses commands and renders help without starting sessions.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	profile "github.com/alcxyz/bivrost/internal/config"
)

type Kind int

const (
	Help Kind = iota
	Environments
	Subscriptions
	ConfigInit
	Connect
	Switch
	Version
	SSH
	ACRProxy
	ACRConnect
	ACRLogin
	ACREnable
	ACRDoctor
	Login
	Doctor
)

type Command struct {
	Kind         Kind
	Environment  string
	ConfigPath   string
	PrivateHosts []string
	ACR          bool
	NoPull       bool
	NoLogin      bool
	Tenant       string
	Debug        bool
	Refresh      bool
	HelpTopic    string
}

func Parse(args []string) (Command, error) {
	if len(args) > 1 && args[0] == "help" {
		topic := strings.Join(args[1:], " ")
		topic = canonicalHelpTopic(topic)
		if HelpText(topic) == "" {
			return Command{}, errors.New("unknown help topic; use bivrost --help")
		}
		return Command{Kind: Help, HelpTopic: topic}, nil
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		topic := canonicalHelpTopic(args[0])
		if HelpText(topic) != "" {
			return Command{Kind: Help, HelpTopic: topic}, nil
		}
	}
	if len(args) == 3 && args[0] == "config" && args[1] == "init" && (args[2] == "--help" || args[2] == "-h") {
		return Command{Kind: Help, HelpTopic: "config init"}, nil
	}

	if len(args) == 0 || (len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h")) {
		return Command{Kind: Help}, nil
	}

	switch args[0] {
	case "list":
		if len(args) >= 2 && args[1] == "subscriptions" {
			return parseSubscriptionsCommand(args[2:])
		}
		if len(args) != 1 {
			return Command{}, errors.New("list does not accept arguments; use bivrost list subscriptions to discover Azure subscriptions")
		}
		return Command{Kind: Environments}, nil
	case "environments", "env", "envs":
		if len(args) != 1 {
			return Command{}, fmt.Errorf("%s does not accept arguments", args[0])
		}
		return Command{Kind: Environments}, nil
	case "config":
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
			return Command{Kind: Help}, nil
		}
		if len(args) != 2 || args[1] != "init" {
			return Command{}, errors.New("use bivrost config init without additional arguments")
		}
		return Command{Kind: ConfigInit}, nil
	case "version", "v", "-v", "--version":
		if len(args) != 1 {
			return Command{}, errors.New("version does not accept arguments")
		}
		return Command{Kind: Version}, nil
	case "doctor":
		return parseConnectionCommand(Doctor, "doctor", args[1:], false)
	case "switch":
		return parseConnectionCommand(Switch, "switch", args[1:], false)
	case "connect":
		return parseConnectionCommand(Connect, "connect", args[1:], true)
	case "ssh":
		return parseConnectionCommand(SSH, "ssh", args[1:], false)
	case "acr":
		if len(args) == 1 {
			return Command{}, errors.New("missing ACR command; use bivrost help")
		}
		if len(args) == 2 && (args[1] == "--help" || args[1] == "-h" || args[1] == "help") {
			return Command{Kind: Help, HelpTopic: "acr"}, nil
		}
		if args[1] == "enable" {
			if len(args) == 3 && (args[2] == "--help" || args[2] == "-h") {
				return Command{Kind: Help, HelpTopic: "acr enable"}, nil
			}
			if len(args) != 2 {
				return Command{}, errors.New("acr enable uses the active session and does not accept options")
			}
			return Command{Kind: ACREnable}, nil
		}
		kind, ok := map[string]Kind{
			"proxy":   ACRProxy,
			"connect": ACRConnect,
			"login":   ACRLogin,
			"doctor":  ACRDoctor,
		}[args[1]]
		if !ok {
			return Command{}, fmt.Errorf("unknown ACR command %q; use bivrost help", args[1])
		}
		return parseConnectionCommand(kind, "acr "+args[1], args[2:], kind == ACRConnect)
	case "login":
		return parseLoginCommand(args[1:])
	case "help":
		return Command{}, errors.New("help does not accept arguments")
	default:
		return Command{}, fmt.Errorf("unknown command %q; use bivrost help", args[0])
	}
}

func parseSubscriptionsCommand(args []string) (Command, error) {
	flags := flag.NewFlagSet("list subscriptions", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	refresh := flags.Bool("refresh", false, "retrieve an up-to-date subscription list from Azure")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Command{Kind: Help, HelpTopic: "list subscriptions"}, nil
		}
		return Command{}, fmt.Errorf("invalid list subscriptions options: %w", err)
	}
	if flags.NArg() != 0 {
		return Command{}, errors.New("list subscriptions does not accept positional arguments")
	}
	return Command{Kind: Subscriptions, Refresh: *refresh}, nil
}

func parseConnectionCommand(kind Kind, name string, args []string, allowNoLogin bool) (Command, error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	debug := flags.Bool("debug", false, "write a bounded local diagnostic log")
	flags.BoolVar(debug, "d", false, "write a bounded local diagnostic log")
	environment := flags.String("env", "", "environment profile name")
	flags.StringVar(environment, "e", "", "environment profile name")
	configPath := flags.String("config", "", "path to a connection configuration")
	flags.StringVar(configPath, "c", "", "path to a connection configuration")
	var privateHosts []string
	if kind == Connect || kind == ACRConnect || kind == Switch {
		flags.Func("private-host", "exact private DNS host to route through this session (repeatable)", func(host string) error {
			privateHosts = append(privateHosts, host)
			return nil
		})
	}
	var noPull bool
	if kind == Doctor {
		flags.BoolVar(&noPull, "no-pull", false, "skip the diagnostic image pull")
	}
	var acr bool
	if kind == Connect || kind == Switch {
		flags.BoolVar(&acr, "acr", false, "enable Podman registry access")
	}
	var noLogin *bool
	if allowNoLogin {
		noLogin = flags.Bool("no-login", false, "connect without logging Podman into ACR")
		flags.BoolVar(noLogin, "n", false, "connect without logging Podman into ACR")
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Command{Kind: Help, HelpTopic: name}, nil
		}
		return Command{}, fmt.Errorf("invalid %s options: %w", name, err)
	}
	if flags.NArg() != 0 {
		return Command{}, fmt.Errorf("%s does not accept positional arguments", name)
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
		return Command{}, errors.New("--env and --config are mutually exclusive")
	}
	if !seenEnvironment && !seenConfig && kind != Doctor {
		return Command{}, fmt.Errorf("%s requires --env NAME or --config PATH", name)
	}
	if seenEnvironment && !profile.ValidEnvironmentName(*environment) {
		return Command{}, errors.New("--env must start with a lowercase letter, contain only lowercase letters, digits, or hyphens, and be at most 63 characters")
	}
	if seenConfig && strings.TrimSpace(*configPath) == "" {
		return Command{}, errors.New("--config requires a non-empty path")
	}
	if err := (profile.Profile{PrivateHosts: privateHosts}).ValidatePrivateHosts(); err != nil {
		return Command{}, fmt.Errorf("invalid --private-host: %w", err)
	}

	command := Command{NoPull: noPull, ACR: acr, Kind: kind, Environment: *environment, ConfigPath: *configPath, PrivateHosts: privateHosts, Debug: *debug}
	if noLogin != nil {
		command.NoLogin = *noLogin
	}
	if kind == Connect && command.NoLogin && !acr {
		return Command{}, errors.New("--no-login requires --acr")
	}
	return command, nil
}

func parseLoginCommand(args []string) (Command, error) {
	flags := flag.NewFlagSet("login", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	debug := flags.Bool("debug", false, "write a bounded local diagnostic log")
	flags.BoolVar(debug, "d", false, "write a bounded local diagnostic log")
	tenant := flags.String("tenant", "", "Azure tenant to authenticate against")
	flags.StringVar(tenant, "t", "", "Azure tenant to authenticate against")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return Command{Kind: Help, HelpTopic: "login"}, nil
		}
		return Command{}, fmt.Errorf("invalid login options: %w", err)
	}
	if flags.NArg() != 0 {
		return Command{}, errors.New("login does not accept positional arguments")
	}
	seenTenant := false
	flags.Visit(func(f *flag.Flag) { seenTenant = seenTenant || f.Name == "tenant" || f.Name == "t" })
	if seenTenant && strings.TrimSpace(*tenant) == "" {
		return Command{}, errors.New("--tenant requires a non-empty value")
	}
	return Command{Kind: Login, Tenant: *tenant, Debug: *debug}, nil
}
