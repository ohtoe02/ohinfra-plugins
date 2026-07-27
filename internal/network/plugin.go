package network

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
	Name        = "network-base"
	Description = "Local network state and TLS certificate diagnostics for ohtools."
	ConfigPath  = "/etc/ohtools/plugins/network-base.yaml"
)

type Options struct {
	Version    string
	Commit     string
	BuildDate  string
	ConfigPath string
	Host       string
	Root       string
	Runner     execx.Runner
	Now        func() time.Time
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
			plan, err := protocol.ReadOnlyPlan(ctx, invocation, manifest)
			if err != nil {
				return protocol.Plan{}, err
			}
			if slices.Equal(invocation.CommandPath, []string{"tls", "inspect"}) {
				if err := validateTLSPathArgument(invocation.Arguments[0]); err != nil {
					return protocol.Plan{}, protocol.ExitError{
						Code: protocol.ExitArguments, Err: err,
					}
				}
			}
			return plan, nil
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
	diagnostic := func(path []string, use, short string, arguments []protocol.Argument) protocol.Command {
		if arguments == nil {
			arguments = []protocol.Argument{}
		}
		return protocol.Command{
			Path: path, Use: use, Short: short,
			Category:  protocol.CategoryDiagnostic,
			Arguments: arguments, Flags: []protocol.Flag{},
		}
	}
	return []protocol.Command{
		diagnostic([]string{"network", "overview"}, "overview",
			"Show a local network overview", nil),
		diagnostic([]string{"network", "interfaces"}, "interfaces",
			"Show local network interfaces", nil),
		diagnostic([]string{"network", "routes"}, "routes",
			"Show local network routes", nil),
		diagnostic([]string{"network", "listeners"}, "listeners",
			"Show local listening sockets", nil),
		diagnostic([]string{"tls", "inspect"}, "inspect <path>",
			"Inspect a local TLS certificate file",
			[]protocol.Argument{{
				Name: "path", Description: "PEM or DER certificate path", Required: true,
			}}),
	}
}

func execute(
	ctx context.Context,
	invocation protocol.Invocation,
	options Options,
) (protocol.Result, error) {
	local := probe.Local{Root: options.Root, Runner: options.Runner}
	switch {
	case slices.Equal(invocation.CommandPath, []string{"network", "overview"}):
		return executeOverview(ctx, options, local)
	case slices.Equal(invocation.CommandPath, []string{"network", "interfaces"}):
		interfaces, err := collectInterfaces(ctx, local)
		if err != nil {
			if fatal := contextFailure(err); fatal != nil {
				return protocol.Result{}, fatal
			}
			return protocol.Result{}, dependencyFailure("ip", err)
		}
		return buildResult(options, "network interfaces",
			map[string]any{"interfaces": interfaces}, nil, nil), nil
	case slices.Equal(invocation.CommandPath, []string{"network", "routes"}):
		routes, err := collectRoutes(ctx, local)
		if err != nil {
			if fatal := contextFailure(err); fatal != nil {
				return protocol.Result{}, fatal
			}
			return protocol.Result{}, dependencyFailure("ip", err)
		}
		return buildResult(options, "network routes",
			map[string]any{"routes": routes}, nil, nil), nil
	case slices.Equal(invocation.CommandPath, []string{"network", "listeners"}):
		listeners, err := collectListeners(ctx, local)
		if err != nil {
			if fatal := contextFailure(err); fatal != nil {
				return protocol.Result{}, fatal
			}
			return protocol.Result{}, dependencyFailure("ss", err)
		}
		return buildResult(options, "network listeners",
			map[string]any{"listeners": listeners}, nil, nil), nil
	case slices.Equal(invocation.CommandPath, []string{"tls", "inspect"}):
		return executeTLS(options, invocation.Arguments[0])
	default:
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("unsupported network-base command"),
		}
	}
}

func executeOverview(
	ctx context.Context,
	options Options,
	local probe.Local,
) (protocol.Result, error) {
	data := map[string]any{}
	checks := []protocol.Check{}
	failures := []protocol.StructuredError{}

	interfaces, interfaceErr := collectInterfaces(ctx, local)
	if fatal := contextFailure(interfaceErr); fatal != nil {
		return protocol.Result{}, fatal
	}
	if interfaceErr == nil {
		data["interfaces"] = interfaces
		checks = append(checks, protocol.Check{
			ID: "network.interfaces", Status: protocol.StatusPass,
			Summary: fmt.Sprintf("Collected %d network interfaces", len(interfaces)),
		})
	} else {
		checks = append(checks, unavailableCheck("network.interfaces", "Network interfaces are unavailable"))
		failures = append(failures, dependencyError("ip", "interfaces_unavailable", interfaceErr))
	}

	routes, routeErr := collectRoutes(ctx, local)
	if fatal := contextFailure(routeErr); fatal != nil {
		return protocol.Result{}, fatal
	}
	if routeErr == nil {
		data["routes"] = routes
		checks = append(checks, protocol.Check{
			ID: "network.routes", Status: protocol.StatusPass,
			Summary: fmt.Sprintf("Collected %d network routes", len(routes)),
		})
	} else {
		checks = append(checks, unavailableCheck("network.routes", "Network routes are unavailable"))
		failures = append(failures, dependencyError("ip", "routes_unavailable", routeErr))
	}

	listeners, listenerErr := collectListeners(ctx, local)
	if fatal := contextFailure(listenerErr); fatal != nil {
		return protocol.Result{}, fatal
	}
	if listenerErr == nil {
		data["listeners"] = listeners
		checks = append(checks, protocol.Check{
			ID: "network.listeners", Status: protocol.StatusPass,
			Summary: fmt.Sprintf("Collected %d local listeners", len(listeners)),
		})
	} else {
		checks = append(checks, unavailableCheck("network.listeners", "Local listeners are unavailable"))
		failures = append(failures, dependencyError("ss", "listeners_unavailable", listenerErr))
	}

	return buildResult(options, "network overview", data, checks, failures), nil
}

func unavailableCheck(id, summary string) protocol.Check {
	return protocol.Check{ID: id, Status: protocol.StatusSkipped, Summary: summary}
}

func dependencyError(dependency, code string, _ error) protocol.StructuredError {
	return protocol.StructuredError{
		Kind: protocol.ErrorDependency, Code: code,
		Message:    fmt.Sprintf("%s local probe is unavailable", dependency),
		Dependency: dependency,
	}
}

func dependencyFailure(dependency string, _ error) error {
	return protocol.ExitError{
		Code: protocol.ExitDependency,
		Err:  fmt.Errorf("%s local probe is unavailable", dependency),
	}
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
