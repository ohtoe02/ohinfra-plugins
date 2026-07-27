package kafka

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
	Name        = "kafka-base"
	Description = "Read-only local Kafka diagnostics for ohtools."
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
			diagnostic([]string{"kafka", "status"}, "status", "Show local Kafka status"),
			diagnostic([]string{"kafka", "config"}, "config", "Inspect local Kafka configuration"),
			diagnostic([]string{"kafka", "storage"}, "storage", "Inspect local Kafka storage"),
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
			local := probe.Local{
				Root:   options.Root,
				Runner: guardedRunner{runner: options.Runner},
			}
			switch {
			case slices.Equal(invocation.CommandPath, []string{"kafka", "status"}):
				return executeStatus(ctx, options, local)
			case slices.Equal(invocation.CommandPath, []string{"kafka", "config"}):
				return executeConfig(ctx, options, local)
			case slices.Equal(invocation.CommandPath, []string{"kafka", "storage"}):
				return executeStorage(ctx, options, local)
			default:
				return protocol.Result{}, protocol.ExitError{
					Code: protocol.ExitDependency,
					Err:  errors.New("local Kafka diagnostics are unavailable"),
				}
			}
		},
	}
}

func diagnostic(path []string, use, short string) protocol.Command {
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
