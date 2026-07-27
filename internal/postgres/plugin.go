package postgres

import (
	"context"
	"errors"
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
	Name        = "postgres-base"
	Description = "Read-only local PostgreSQL diagnostics for ohtools."
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
			diagnostic([]string{"postgres", "status"}, "status", "Show local PostgreSQL status"),
			diagnostic([]string{"postgres", "clusters"}, "clusters", "List local PostgreSQL clusters"),
			diagnostic([]string{"postgres", "config"}, "config", "Inspect local PostgreSQL configuration"),
		},
	}
	return protocol.Definition{
		Manifest: manifest,
		Plan: func(ctx context.Context, invocation protocol.Invocation) (protocol.Plan, error) {
			return protocol.ReadOnlyPlan(ctx, invocation, manifest)
		},
		Execute: func(ctx context.Context, invocation protocol.Invocation) (protocol.Result, error) {
			if _, err := protocol.ReadOnlyPlan(ctx, invocation, manifest); err != nil {
				return protocol.Result{}, err
			}
			return execute(ctx, invocation, options)
		},
	}
}

func diagnostic(path []string, use, short string) protocol.Command {
	return protocol.Command{
		Path: path, Use: use, Short: short,
		Category: protocol.CategoryDiagnostic, Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
	}
}

func execute(ctx context.Context, invocation protocol.Invocation, options Options) (protocol.Result, error) {
	local := probe.Local{Root: options.Root, Runner: options.Runner}
	switch {
	case slices.Equal(invocation.CommandPath, []string{"postgres", "status"}):
		return executeStatus(ctx, options, local)
	case slices.Equal(invocation.CommandPath, []string{"postgres", "clusters"}):
		return executeClusters(ctx, options, local)
	case slices.Equal(invocation.CommandPath, []string{"postgres", "config"}):
		return executeConfig(ctx, options, local)
	default:
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("unsupported postgres-base command"),
		}
	}
}

func buildResult(
	options Options,
	command string,
	data map[string]any,
	checks []protocol.Check,
	failures []protocol.StructuredError,
) protocol.Result {
	now := options.Now()
	return resultbuilder.Build(resultbuilder.Input{
		Command: command,
		Tool: protocol.Tool{
			Name: Name, Version: options.Version, Commit: options.Commit,
			BuildDate: options.BuildDate, GoVersion: runtime.Version(), Architecture: runtime.GOARCH,
		},
		Host: options.Host, Started: now, Now: now,
		Data: data, Checks: checks, Errors: failures,
	})
}

func dependencyFailure(message string) error {
	return protocol.ExitError{Code: protocol.ExitDependency, Err: errors.New(message)}
}

func contextFailure(err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, context.Canceled):
		return context.Canceled
	default:
		return nil
	}
}
