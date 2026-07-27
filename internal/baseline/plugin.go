package baseline

import (
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"time"

	pluginconfig "github.com/ohtoe02/ohtools-plugins/internal/config"
	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/platform"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/resultbuilder"
)

const (
	Name        = "baseline-base"
	Description = "Compiled local operating system baseline evaluation for ohtools."
	ConfigPath  = "/etc/ohtools/plugins/baseline-base.yaml"
)

type Options struct {
	Version    string
	Commit     string
	BuildDate  string
	Root       string
	Runner     execx.Runner
	Host       string
	Now        func() time.Time
	ConfigPath string
}

func NewDefinition(options Options) protocol.Definition {
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.Root == "" {
		options.Root = string(os.PathSeparator)
	}
	if options.Runner == nil {
		options.Runner = execx.OSRunner{Resolver: execx.SystemResolver()}
	}
	if options.Host == "" {
		options.Host, _ = os.Hostname()
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ConfigPath == "" {
		options.ConfigPath = ConfigPath
	}
	manifest := protocol.Manifest{
		ProtocolVersion: protocol.ProtocolVersion,
		Name:            Name,
		Version:         options.Version,
		Description:     Description,
		Commands: []protocol.Command{
			baselineCommand("check", "Evaluate the compiled local baseline"),
			baselineCommand("profile", "Show the effective compiled baseline profile"),
		},
	}
	return protocol.Definition{
		Manifest: manifest,
		Plan: func(ctx context.Context, invocation protocol.Invocation) (protocol.Plan, error) {
			return protocol.ReadOnlyPlan(ctx, invocation, manifest)
		},
		Execute: func(ctx context.Context, invocation protocol.Invocation) (protocol.Result, error) {
			return execute(ctx, invocation, options, manifest)
		},
	}
}

func baselineCommand(use, short string) protocol.Command {
	return protocol.Command{
		Path: []string{"baseline", use}, Use: use, Short: short,
		Category:  protocol.CategoryDiagnostic,
		Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
	}
}

func execute(
	ctx context.Context,
	invocation protocol.Invocation,
	options Options,
	manifest protocol.Manifest,
) (protocol.Result, error) {
	if _, err := protocol.ReadOnlyPlan(ctx, invocation, manifest); err != nil {
		return protocol.Result{}, err
	}
	settings, err := pluginconfig.Load(options.ConfigPath, DefaultConfig())
	if err != nil {
		return protocol.Result{}, protocol.ExitError{Code: protocol.ExitConfiguration, Err: err}
	}
	checkIDs, err := selectedChecks(settings)
	if err != nil {
		return protocol.Result{}, protocol.ExitError{Code: protocol.ExitConfiguration, Err: err}
	}
	started := options.Now()
	info, err := platform.Detect(options.Root)
	if err != nil {
		return protocol.Result{}, protocol.ExitError{Code: protocol.ExitDependency, Err: err}
	}
	if !info.Supported {
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitDependency, Err: errors.New("no compiled baseline for this operating system"),
		}
	}

	switch {
	case slices.Equal(invocation.CommandPath, []string{"baseline", "profile"}):
		return baselineResult(options, started, "baseline profile", map[string]any{
			"profile":   settings.Profile,
			"platform":  map[string]any{"id": info.ID, "version_id": info.VersionID},
			"check_ids": checkIDs,
			"thresholds": map[string]any{
				"max_pending_package_records": settings.MaxPendingPackageRecords,
			},
		}, []protocol.Check{{
			ID: "baseline:profile", Status: protocol.StatusPass,
			Summary: "The compiled baseline profile is available",
		}}, nil), nil
	case slices.Equal(invocation.CommandPath, []string{"baseline", "check"}):
		checks, structuredErrors, err := runChecks(
			ctx,
			probe.Local{Root: options.Root, Runner: options.Runner},
			info,
			settings,
			checkIDs,
		)
		if err != nil {
			return protocol.Result{}, err
		}
		return baselineResult(options, started, "baseline check", map[string]any{
			"profile":             settings.Profile,
			"platform":            map[string]any{"id": info.ID, "version_id": info.VersionID},
			"enabled_check_count": len(checkIDs),
		}, checks, structuredErrors), nil
	default:
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("unsupported baseline-base command"),
		}
	}
}

func baselineResult(
	options Options,
	started time.Time,
	command string,
	data map[string]any,
	checks []protocol.Check,
	structuredErrors []protocol.StructuredError,
) protocol.Result {
	return resultbuilder.Build(resultbuilder.Input{
		Command: command,
		Tool: protocol.Tool{
			Name: Name, Version: options.Version, Commit: options.Commit,
			BuildDate: options.BuildDate, GoVersion: runtime.Version(), Architecture: runtime.GOARCH,
		},
		Host: options.Host, Started: started, Now: options.Now(),
		Data: data, Checks: checks, Errors: structuredErrors,
	})
}
