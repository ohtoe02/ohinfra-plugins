package apt

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/resultbuilder"
)

const (
	Name        = "apt-base"
	Description = "Read-only local APT and dpkg diagnostics for ohtools."
)

type Options struct {
	Version   string
	Commit    string
	BuildDate string
	Root      string
	Runner    execx.Runner
	Host      string
	Now       func() time.Time
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

	manifest := protocol.Manifest{
		ProtocolVersion: protocol.ProtocolVersion,
		Name:            Name,
		Version:         options.Version,
		Description:     Description,
		Commands: []protocol.Command{
			diagnostic([]string{"apt", "status"}, "status", "Inspect local dpkg database state"),
			diagnostic([]string{"apt", "updates"}, "updates", "Simulate available package upgrades"),
			diagnostic([]string{"apt", "sources"}, "sources", "Inspect configured APT sources"),
			diagnostic([]string{"apt", "history"}, "history", "Inspect local APT transaction history"),
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

func diagnostic(path []string, use, short string) protocol.Command {
	return protocol.Command{
		Path:      path,
		Use:       use,
		Short:     short,
		Category:  protocol.CategoryDiagnostic,
		Arguments: []protocol.Argument{},
		Flags:     []protocol.Flag{},
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
	started := options.Now()
	local := probe.Local{Root: options.Root, Runner: options.Runner}

	switch {
	case slices.Equal(invocation.CommandPath, []string{"apt", "status"}):
		data, checks, structuredErrors, err := collectStatus(local)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return protocol.Result{}, dependencyError("dpkg status database is unavailable")
			}
			return protocol.Result{}, generalError("inspect dpkg status", err)
		}
		return buildResult(options, started, "apt status", data, checks, structuredErrors), nil
	case slices.Equal(invocation.CommandPath, []string{"apt", "updates"}):
		data, err := collectUpdates(ctx, local)
		if err != nil {
			if errors.Is(err, execx.ErrNotFound) {
				return protocol.Result{}, dependencyError("apt-get executable is unavailable")
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return protocol.Result{}, err
			}
			return protocol.Result{}, generalError("simulate APT upgrades", err)
		}
		return buildResult(options, started, "apt updates", data, []protocol.Check{{
			ID: "apt:updates", Status: protocol.StatusPass, Summary: "APT upgrade simulation completed",
		}}, nil), nil
	case slices.Equal(invocation.CommandPath, []string{"apt", "sources"}):
		data, checks, structuredErrors := collectSources(local)
		return buildResult(options, started, "apt sources", data, checks, structuredErrors), nil
	case slices.Equal(invocation.CommandPath, []string{"apt", "history"}):
		data, checks, structuredErrors := collectHistory(local)
		return buildResult(options, started, "apt history", data, checks, structuredErrors), nil
	default:
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("unsupported apt-base command"),
		}
	}
}

func buildResult(
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
		Checks: checks, Data: data, Errors: structuredErrors,
	})
}

func dependencyError(message string) error {
	return protocol.ExitError{Code: protocol.ExitDependency, Err: errors.New(message)}
}

func generalError(operation string, err error) error {
	return protocol.ExitError{
		Code: protocol.ExitGeneral,
		Err:  fmt.Errorf("%s: %w", operation, err),
	}
}
