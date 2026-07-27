package gitlabrunner

import (
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/resultbuilder"
)

const (
	Name        = "gitlab-runner-base"
	Description = "Local GitLab Runner service, configuration, and executor diagnostics for ohtools."
)

type Options struct {
	Version   string
	Commit    string
	BuildDate string
	Host      string
	Root      string
	Runner    execx.Runner
	Now       func() time.Time
}

func NewDefinition(options Options) protocol.Definition {
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.Host == "" {
		options.Host, _ = os.Hostname()
	}
	if options.Root == "" {
		options.Root = string(os.PathSeparator)
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
			diagnosticCommand([]string{"gitlab-runner", "status"}, "status",
				"Show local GitLab Runner service and process state"),
			diagnosticCommand([]string{"gitlab-runner", "config"}, "config",
				"Show safe local GitLab Runner configuration metadata"),
			diagnosticCommand([]string{"gitlab-runner", "executors"}, "executors",
				"Show configured local GitLab Runner executors"),
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

func execute(
	ctx context.Context,
	invocation protocol.Invocation,
	options Options,
) (protocol.Result, error) {
	switch {
	case slices.Equal(invocation.CommandPath, []string{"gitlab-runner", "status"}):
		return executeStatus(ctx, options)
	case slices.Equal(invocation.CommandPath, []string{"gitlab-runner", "config"}):
		return executeConfig(ctx, options)
	case slices.Equal(invocation.CommandPath, []string{"gitlab-runner", "executors"}):
		return executeExecutors(ctx, options)
	default:
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments,
			Err:  errors.New("unsupported gitlab-runner-base command"),
		}
	}
}

func diagnosticCommand(path []string, use, short string) protocol.Command {
	return protocol.Command{
		Path: path, Use: use, Short: short,
		Category:  protocol.CategoryDiagnostic,
		Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
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
			BuildDate: options.BuildDate, GoVersion: runtime.Version(),
			Architecture: runtime.GOARCH,
		},
		Host: options.Host, Started: now, Now: now,
		Data: data, Checks: checks, Errors: failures,
	})
}

func contextFailure(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func dependencyFailure(message string) error {
	return protocol.ExitError{
		Code: protocol.ExitDependency,
		Err:  errors.New(message),
	}
}

func configurationFailure(message string) error {
	return protocol.ExitError{
		Code: protocol.ExitConfiguration,
		Err:  errors.New(message),
	}
}
