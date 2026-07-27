package java

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/resultbuilder"
)

const (
	Name        = "java-base"
	Description = "Local Java runtime and process diagnostics for ohtools."
	ConfigPath  = "/etc/ohtools/plugins/java-base.yaml"
	maxPID      = 4_194_304
)

var decimalPID = regexp.MustCompile(`^[0-9]+$`)

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
		Commands: []protocol.Command{
			diagnosticCommand([]string{"java", "runtime"}, "runtime",
				"Show the local Java runtime version", nil),
			diagnosticCommand([]string{"java", "processes"}, "processes",
				"Show local Java processes", nil),
			diagnosticCommand([]string{"java", "inspect"}, "inspect <pid>",
				"Inspect a local Java process",
				[]protocol.Argument{{
					Name: "pid", Description: "Positive decimal process ID", Required: true,
				}}),
		},
	}
	return protocol.Definition{
		Manifest: manifest,
		Plan: func(ctx context.Context, invocation protocol.Invocation) (protocol.Plan, error) {
			return validateInvocation(ctx, invocation, manifest)
		},
		Execute: func(ctx context.Context, invocation protocol.Invocation) (protocol.Result, error) {
			if _, err := validateInvocation(ctx, invocation, manifest); err != nil {
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
	local := probe.Local{Root: options.Root, Runner: options.Runner}
	switch {
	case slices.Equal(invocation.CommandPath, []string{"java", "runtime"}):
		info, err := collectRuntime(ctx, local)
		if err != nil {
			if fatal := contextFailure(err); fatal != nil {
				return protocol.Result{}, fatal
			}
			return protocol.Result{}, dependencyFailure("java")
		}
		return buildResult(options, "java runtime",
			map[string]any{"runtime": info},
			[]protocol.Check{{
				ID: "java.runtime", Status: protocol.StatusPass,
				Summary: "Local Java runtime metadata is available",
			}}, nil), nil
	case slices.Equal(invocation.CommandPath, []string{"java", "processes"}):
		processes, issues, err := collectJavaProcesses(ctx, local)
		if err != nil {
			if fatal := contextFailure(err); fatal != nil {
				return protocol.Result{}, fatal
			}
			return protocol.Result{}, dependencyFailure("procfs")
		}
		status := protocol.StatusPass
		summary := fmt.Sprintf("Found %d local Java processes", len(processes))
		if len(processes) == 0 {
			status = protocol.StatusInfo
			summary = "No local Java processes were found"
		}
		return buildResult(options, "java processes",
			map[string]any{"processes": processes},
			[]protocol.Check{{
				ID: "java.processes", Status: status, Summary: summary,
			}}, issues), nil
	case slices.Equal(invocation.CommandPath, []string{"java", "inspect"}):
		pid, _ := parsePID(invocation.Arguments[0])
		info, err := inspectProcess(ctx, local, pid)
		if err != nil {
			if fatal := contextFailure(err); fatal != nil {
				return protocol.Result{}, fatal
			}
			if errors.Is(err, execx.ErrNotFound) {
				return protocol.Result{}, dependencyFailure("jcmd")
			}
			if errors.Is(err, errAttachDenied) {
				return protocol.Result{}, protocol.ExitError{
					Code: protocol.ExitPrivilege,
					Err:  errors.New("insufficient privilege to inspect the local Java process"),
				}
			}
			return protocol.Result{}, protocol.ExitError{
				Code: protocol.ExitGeneral,
				Err:  errors.New("local Java process inspection failed"),
			}
		}
		return buildResult(options, "java inspect",
			map[string]any{"process": info},
			[]protocol.Check{{
				ID: "java.inspect", Status: protocol.StatusPass,
				Summary: "Local Java process metadata is available",
			}}, nil), nil
	default:
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("unsupported java-base command"),
		}
	}
}

func validateInvocation(
	ctx context.Context,
	invocation protocol.Invocation,
	manifest protocol.Manifest,
) (protocol.Plan, error) {
	plan, err := protocol.ReadOnlyPlan(ctx, invocation, manifest)
	if err != nil {
		return protocol.Plan{}, err
	}
	if slices.Equal(invocation.CommandPath, []string{"java", "inspect"}) {
		if _, err := parsePID(invocation.Arguments[0]); err != nil {
			return protocol.Plan{}, protocol.ExitError{
				Code: protocol.ExitArguments, Err: err,
			}
		}
	}
	return plan, nil
}

func parsePID(value string) (int, error) {
	if !decimalPID.MatchString(value) {
		return 0, fmt.Errorf("pid must be a positive decimal integer")
	}
	pid, err := strconv.ParseUint(value, 10, 32)
	if err != nil || pid == 0 || pid > maxPID {
		return 0, fmt.Errorf("pid must be within 1..%d", maxPID)
	}
	return int(pid), nil
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

func dependencyFailure(dependency string) error {
	return protocol.ExitError{
		Code: protocol.ExitDependency,
		Err:  fmt.Errorf("%s local probe is unavailable", dependency),
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

func diagnosticCommand(
	path []string,
	use string,
	short string,
	arguments []protocol.Argument,
) protocol.Command {
	if arguments == nil {
		arguments = []protocol.Argument{}
	}
	return protocol.Command{
		Path: path, Use: use, Short: short,
		Category:  protocol.CategoryDiagnostic,
		Arguments: arguments, Flags: []protocol.Flag{},
	}
}
