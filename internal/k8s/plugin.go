package k8s

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
	Name        = "k8s-base"
	Description = "Local Kubernetes client, kubelet, context, and manifest diagnostics for ohtools."
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
		Commands:        manifestCommands(),
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

func manifestCommands() []protocol.Command {
	diagnostic := func(path []string, use, short string) protocol.Command {
		return protocol.Command{
			Path: path, Use: use, Short: short,
			Category:  protocol.CategoryDiagnostic,
			Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
		}
	}
	return []protocol.Command{
		diagnostic([]string{"k8s", "status"}, "status",
			"Show local Kubernetes client and kubelet state"),
		diagnostic([]string{"k8s", "contexts"}, "contexts",
			"Show safe local kubeconfig metadata"),
		diagnostic([]string{"k8s", "manifests"}, "manifests",
			"Inspect local static Kubernetes manifests"),
	}
}

func execute(
	ctx context.Context,
	invocation protocol.Invocation,
	options Options,
) (protocol.Result, error) {
	switch {
	case slices.Equal(invocation.CommandPath, []string{"k8s", "status"}):
		return executeStatus(ctx, options)
	case slices.Equal(invocation.CommandPath, []string{"k8s", "contexts"}):
		return executeContexts(ctx, options)
	case slices.Equal(invocation.CommandPath, []string{"k8s", "manifests"}):
		return executeManifests(ctx, options)
	default:
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("unsupported k8s-base command"),
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
			BuildDate: options.BuildDate, GoVersion: runtime.Version(),
			Architecture: runtime.GOARCH,
		},
		Host: options.Host, Started: now, Now: now,
		Data: data, Checks: checks, Errors: failures,
	})
}
