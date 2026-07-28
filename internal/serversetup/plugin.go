package serversetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/platform"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/redact"
	"github.com/ohtoe02/ohtools-plugins/internal/resultbuilder"
)

const (
	Name        = "server-setup-base"
	Description = "Local server baseline checks and controlled setup operations for ohtools."
	ConfigPath  = "/etc/ohtools/plugins/server-setup-base.yaml"
)

type Options struct {
	Version          string
	Commit           string
	BuildDate        string
	ConfigPath       string
	Host             string
	Root             string
	Runner           execx.Runner
	Now              func() time.Time
	Identity         FileIdentityReader
	Transactions     SetupTransactionFactory
	MutationFiles    MutationFileAdapter
	MutationCommands MutationCommandAdapter
	UpgradeProbe     CachedUpgradeProbe
}

func NewDefinition(options Options) protocol.Definition {
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.ConfigPath == "" {
		options.ConfigPath = ConfigPath
	}
	if options.Host == "" {
		options.Host, _ = os.Hostname()
	}
	if options.Root == "" {
		options.Root = string(filepath.Separator)
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Transactions == nil {
		commands := options.MutationCommands
		if commands == nil {
			commands = localMutationCommands{Runner: options.Runner}
		}
		options.Transactions = localSetupTransactionFactory{
			files: options.MutationFiles, commands: commands,
		}
	}
	if options.UpgradeProbe == nil {
		options.UpgradeProbe = localCachedUpgradeProbe{Runner: options.Runner}
	}

	commands := []protocol.Command{
		{
			Path: []string{"setup", "check"}, Use: "check",
			Short:     "Check the compiled server setup baseline",
			Category:  protocol.CategoryDiagnostic,
			Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
		},
		{
			Path: []string{"setup", "apply"}, Use: "apply",
			Short:     "Apply the compiled server setup baseline",
			Category:  protocol.CategoryOperational,
			Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
			RequiresRoot: true, SupportsDryRun: true, RequiresConfirmation: true,
		},
		{
			Path: []string{"setup", "upgrade"}, Use: "upgrade",
			Short:     "Upgrade ohtools-owned setup state",
			Category:  protocol.CategoryOperational,
			Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
			RequiresRoot: true, SupportsDryRun: true, RequiresConfirmation: true,
		},
	}

	manifest := protocol.Manifest{
		ProtocolVersion: protocol.ProtocolVersion,
		Name:            Name,
		Version:         options.Version,
		Description:     Description,
		Commands:        commands,
	}
	return protocol.Definition{
		Manifest: manifest,
		Plan: func(ctx context.Context, invocation protocol.Invocation) (protocol.Plan, error) {
			if slices.Equal(invocation.CommandPath, []string{"setup", "check"}) {
				return protocol.ReadOnlyPlan(ctx, invocation, manifest)
			}
			return buildCurrentPlan(ctx, invocation, options, manifest)
		},
		Execute: func(ctx context.Context, invocation protocol.Invocation) (protocol.Result, error) {
			return execute(ctx, invocation, options, manifest)
		},
	}
}

func buildCurrentPlan(
	ctx context.Context,
	invocation protocol.Invocation,
	options Options,
	manifest protocol.Manifest,
) (protocol.Plan, error) {
	if _, err := protocol.ReadOnlyPlan(ctx, invocation, manifest); err != nil {
		return protocol.Plan{}, err
	}
	mode, err := planModeForPath(invocation.CommandPath)
	if err != nil {
		return protocol.Plan{}, err
	}
	settings, profile, err := loadSetupState(options)
	if err != nil {
		return protocol.Plan{}, err
	}
	current, err := Checker{
		Root: options.Root, Runner: options.Runner, Host: options.Host,
		Now: options.Now, Tool: tool(options), Identity: options.Identity,
	}.Run(ctx, profile, settings)
	if err != nil {
		return protocol.Plan{}, err
	}
	cachedUpgrades := []string{}
	if mode == PlanUpgrade {
		cachedUpgrades, err = options.UpgradeProbe.CachedUpgrades(ctx, profile.Packages)
		if err != nil {
			code := protocol.ExitGeneral
			if errors.Is(err, execx.ErrNotFound) {
				code = protocol.ExitDependency
			}
			return protocol.Plan{}, protocol.ExitError{
				Code: code,
				Err:  errors.New(redact.String("inspect cached upgrades: " + err.Error())),
			}
		}
	}
	return BuildSetupPlan(PlanInput{
		Mode: mode, Profile: profile, Checks: current.Checks,
		CachedUpgrades: cachedUpgrades,
	})
}

func loadSetupState(options Options) (Config, Profile, error) {
	settings, err := LoadConfig(options.ConfigPath)
	if err != nil {
		return Config{}, Profile{}, protocol.ExitError{
			Code: protocol.ExitConfiguration, Err: err,
		}
	}
	detected, err := platform.Detect(options.Root)
	if err != nil {
		return Config{}, Profile{}, protocol.ExitError{
			Code: protocol.ExitConfiguration,
			Err:  errors.New("detect setup platform: " + err.Error()),
		}
	}
	profile, err := ResolveProfile(settings, detected)
	if err != nil {
		return Config{}, Profile{}, protocol.ExitError{
			Code: protocol.ExitConfiguration, Err: err,
		}
	}
	return settings, profile, nil
}

func execute(
	ctx context.Context,
	invocation protocol.Invocation,
	options Options,
	manifest protocol.Manifest,
) (protocol.Result, error) {
	if !slices.Equal(invocation.CommandPath, []string{"setup", "check"}) {
		return executeMutation(ctx, invocation, options, manifest)
	}
	if _, err := protocol.ReadOnlyPlan(ctx, invocation, manifest); err != nil {
		return protocol.Result{}, err
	}
	settings, err := LoadConfig(options.ConfigPath)
	if err != nil {
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitConfiguration, Err: err,
		}
	}
	detected, err := platform.Detect(options.Root)
	if err != nil {
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitConfiguration,
			Err:  errors.New("detect setup platform: " + err.Error()),
		}
	}
	profile, err := ResolveProfile(settings, detected)
	if err != nil {
		now := options.Now().UTC()
		return resultbuilder.Build(resultbuilder.Input{
			Command: "setup check", Tool: tool(options), Host: options.Host,
			Started: now, Now: now,
			Checks: []protocol.Check{{
				ID: "platform", Status: protocol.StatusCritical,
				Summary: "no exact compiled setup profile is available",
			}},
			Data: map[string]any{
				"platform_id": detected.ID, "platform_version": detected.VersionID,
			},
			Errors: []protocol.StructuredError{{
				Kind: protocol.ErrorConfiguration, Code: "unsupported_platform",
				Message: err.Error(),
			}},
		}), nil
	}
	return Checker{
		Root: options.Root, Runner: options.Runner, Host: options.Host,
		Now: options.Now, Tool: tool(options), Identity: options.Identity,
	}.Run(ctx, profile, settings)
}

func tool(options Options) protocol.Tool {
	return protocol.Tool{
		Name: Name, Version: options.Version, Commit: options.Commit,
		BuildDate: options.BuildDate, GoVersion: runtime.Version(), Architecture: runtime.GOARCH,
	}
}

func argumentError(message string) error {
	return protocol.ExitError{Code: protocol.ExitArguments, Err: errors.New(message)}
}
