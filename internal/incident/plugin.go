package incident

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
	Name        = "incident-base"
	Description = "Read-only local incident evidence collection for ohtools."
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
			diagnostic([]string{"incident", "snapshot"}, "snapshot",
				"Collect bounded local incident evidence"),
			diagnostic([]string{"incident", "services"}, "services",
				"Show local failed service evidence"),
			{
				Path: []string{"incident", "timeline"}, Use: "timeline",
				Short:     "Show a bounded local incident timeline",
				Category:  protocol.CategoryDiagnostic,
				Arguments: []protocol.Argument{},
				Flags: []protocol.Flag{{
					Name: "since", Type: "duration",
					Description: "Look back over local journal events", Default: "1h",
				}},
			},
		},
	}
	return protocol.Definition{
		Manifest: manifest,
		Plan: func(ctx context.Context, invocation protocol.Invocation) (protocol.Plan, error) {
			if slices.Equal(invocation.CommandPath, []string{"incident", "timeline"}) {
				if _, err := timelineSince(invocation); err != nil {
					return protocol.Plan{}, err
				}
			}
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
		Category:  protocol.CategoryDiagnostic,
		Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
	}
}

func execute(ctx context.Context, invocation protocol.Invocation, options Options) (protocol.Result, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	switch {
	case slices.Equal(invocation.CommandPath, []string{"incident", "snapshot"}):
		return executeSnapshot(ctx, options)
	case slices.Equal(invocation.CommandPath, []string{"incident", "services"}):
		return executeServices(ctx, options)
	case slices.Equal(invocation.CommandPath, []string{"incident", "timeline"}):
		return executeTimeline(ctx, invocation, options)
	default:
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("unsupported incident-base command"),
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
