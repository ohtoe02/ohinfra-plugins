package serversetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

type Options struct {
	Version    string
	Commit     string
	BuildDate  string
	ConfigPath string
	Root       string
	Runner     execx.Runner
	Backend    Backend
	Platform   *Platform
	Host       string
	Now        func() time.Time
}

func NewDefinition(options Options) protocol.Definition {
	if options.Version == "" {
		options.Version = "dev"
	}
	return protocol.NewDefinition(
		protocol.DefinitionSpec{
			Name: Name, Version: options.Version, Description: Description,
		},
		protocol.Diagnostic(
			protocol.CommandSpec{
				Path: []string{"setup", "check"}, Use: "check",
				Short:     "Check the selected server setup profile for drift",
				Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
			},
			func(ctx context.Context, _ protocol.Invocation) (protocol.Result, error) {
				manager, err := managerFor(options, false)
				if err != nil {
					return protocol.Result{}, err
				}
				return manager.Check(ctx, nil), nil
			},
		),
		protocol.Mutation(
			protocol.CommandSpec{
				Path: []string{"setup", "apply"}, Use: "apply [item...]",
				Short: "Converge all or selected server setup items",
				Arguments: []protocol.Argument{{
					Name: "item", Description: "Stable server setup item ID", Variadic: true,
				}},
				Flags: []protocol.Flag{},
			},
			protocol.CategoryRunbook,
			protocol.Risk{RequiresRoot: true, RequiresConfirmation: true},
			func(ctx context.Context, invocation protocol.Invocation) (protocol.Plan, error) {
				manager, err := managerFor(options, true)
				if err != nil {
					return protocol.Plan{}, err
				}
				return manager.Plan(ctx, invocation.Arguments)
			},
			func(
				ctx context.Context,
				_ protocol.Invocation,
				approved protocol.Plan,
			) (protocol.Result, error) {
				manager, err := managerFor(options, true)
				if err != nil {
					return protocol.Result{}, err
				}
				return manager.Apply(ctx, approved)
			},
		),
		protocol.Mutation(
			protocol.CommandSpec{
				Path: []string{"setup", "upgrade"}, Use: "upgrade",
				Short:     "Install ordinary package upgrades without rebooting",
				Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
			},
			protocol.CategoryRunbook,
			protocol.Risk{RequiresRoot: true, RequiresConfirmation: true},
			func(ctx context.Context, _ protocol.Invocation) (protocol.Plan, error) {
				manager, err := managerFor(options, true)
				if err != nil {
					return protocol.Plan{}, err
				}
				return manager.UpgradePlan(ctx)
			},
			func(
				ctx context.Context,
				_ protocol.Invocation,
				approved protocol.Plan,
			) (protocol.Result, error) {
				manager, err := managerFor(options, true)
				if err != nil {
					return protocol.Result{}, err
				}
				return manager.Upgrade(ctx, approved)
			},
		),
	)
}

func managerFor(options Options, requireConfig bool) (Manager, error) {
	configPath := options.ConfigPath
	if configPath == "" {
		configPath = ConfigPath
	}
	_, configStatErr := os.Lstat(configPath)
	configMissing := errors.Is(configStatErr, os.ErrNotExist)
	config, err := LoadConfig(configPath, requireConfig)
	if err != nil {
		return Manager{}, protocol.ExitError{
			Code: protocol.ExitConfiguration, Err: err,
		}
	}
	platform, err := platformFor(options)
	if err != nil {
		return Manager{}, protocol.ExitError{
			Code: protocol.ExitDependency, Err: err,
		}
	}
	runner := options.Runner
	if runner == nil {
		runner = execx.OSRunner{Resolver: execx.SystemResolver()}
	}
	backend := options.Backend
	if backend == nil {
		backend = SystemBackend{Root: options.Root, Runner: runner}
	}
	host := options.Host
	if host == "" {
		host, _ = os.Hostname()
	}
	return Manager{
		Backend: backend, Config: config, Platform: platform, Host: host,
		ConfigPath: configPath, ConfigMissing: configMissing,
		Tool: protocol.Tool{
			Name: Name, Version: options.Version, Commit: options.Commit,
			BuildDate: options.BuildDate, GoVersion: runtime.Version(),
			Architecture: runtime.GOARCH,
		},
		Now: options.Now,
	}, nil
}

func platformFor(options Options) (Platform, error) {
	if options.Platform != nil {
		return *options.Platform, nil
	}
	root := options.Root
	if root == "" {
		root = string(filepath.Separator)
	}
	path := filepath.Join(root, filepath.FromSlash("etc/os-release"))
	content, err := os.ReadFile(path) // #nosec G304 -- path is fixed below an injected test root.
	if err != nil {
		return Platform{}, fmt.Errorf("read /etc/os-release: %w", err)
	}
	platform, err := ResolvePlatform(content)
	if err != nil {
		return Platform{}, err
	}
	if strings.TrimSpace(platform.ID) == "" {
		return Platform{}, fmt.Errorf("operating system identity is empty")
	}
	return platform, nil
}
