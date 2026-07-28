package security

import (
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/platform"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/resultbuilder"
)

const (
	Name        = "security-base"
	Description = "Local account, SSH, and firewall security diagnostics for ohtools."
)

type Options struct {
	Version   string
	Commit    string
	BuildDate string
	Host      string
	Root      string
	Local     probe.Local
	Now       func() time.Time
}

type collection struct {
	Data     map[string]any
	Checks   []protocol.Check
	Errors   []protocol.StructuredError
	Usable   bool
	ExitCode int
	Fatal    error
}

func NewDefinition(options Options) protocol.Definition {
	options = normalizeOptions(options)
	manifest := protocol.Manifest{
		ProtocolVersion: protocol.ProtocolVersion,
		Name:            Name,
		Version:         options.Version,
		Description:     Description,
		Commands: []protocol.Command{
			readOnlyCommand([]string{"security", "audit"}, "audit", "Audit local account, SSH, and firewall posture"),
			readOnlyCommand([]string{"security", "accounts"}, "accounts", "Inspect local account security metadata"),
			readOnlyCommand([]string{"security", "ssh"}, "ssh", "Inspect local SSH server security posture"),
			readOnlyCommand([]string{"security", "firewall"}, "firewall", "Inspect local firewall state"),
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

func normalizeOptions(options Options) Options {
	if options.Version == "" {
		options.Version = "dev"
	}
	switch {
	case options.Root == "" && options.Local.Root != "":
		options.Root = options.Local.Root
	case options.Root == "":
		options.Root = "/"
	}
	if options.Local.Root == "" || options.Local.Root != options.Root {
		options.Local.Root = options.Root
	}
	if options.Host == "" {
		options.Host, _ = os.Hostname()
		if options.Host == "" {
			options.Host = "unknown"
		}
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return options
}

func readOnlyCommand(path []string, use, short string) protocol.Command {
	return protocol.Command{
		Path: path, Use: use, Short: short, Category: protocol.CategoryDiagnostic,
		Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
	}
}

func execute(
	ctx context.Context,
	invocation protocol.Invocation,
	options Options,
) (protocol.Result, error) {
	switch {
	case slices.Equal(invocation.CommandPath, []string{"security", "audit"}):
		return executeAudit(ctx, options)
	case slices.Equal(invocation.CommandPath, []string{"security", "accounts"}):
		return executeNarrow("security accounts", options, collectAccounts(ctx, options))
	case slices.Equal(invocation.CommandPath, []string{"security", "ssh"}):
		return executeNarrow("security ssh", options, collectSSH(ctx, options))
	case slices.Equal(invocation.CommandPath, []string{"security", "firewall"}):
		return executeNarrow("security firewall", options, collectFirewall(ctx, options))
	default:
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("unsupported security-base command"),
		}
	}
}

func executeNarrow(command string, options Options, collected collection) (protocol.Result, error) {
	if collected.Fatal != nil {
		return protocol.Result{}, collected.Fatal
	}
	if !collected.Usable {
		code := collected.ExitCode
		if code == 0 {
			code = protocol.ExitDependency
		}
		message := "required local security source is unavailable"
		if code == protocol.ExitPrivilege {
			message = "local security source requires additional privileges"
		}
		return protocol.Result{}, protocol.ExitError{Code: code, Err: errors.New(message)}
	}
	return buildResult(command, options, collected), nil
}

func buildResult(command string, options Options, collected collection) protocol.Result {
	data := collected.Data
	if data == nil {
		data = map[string]any{}
	}
	info, err := platform.Detect(options.Root)
	if err != nil {
		collected.Errors = append(collected.Errors, structuredError(
			protocol.ErrorDependency, "platform_unavailable", "operating-system metadata is unavailable", "os-release",
		))
	} else {
		data["platform"] = map[string]any{
			"id": info.ID, "version_id": info.VersionID, "supported": info.Supported,
		}
		if !info.Supported {
			collected.Checks = append(collected.Checks, protocol.Check{
				ID: "platform.supported", Status: protocol.StatusWarning,
				Summary: "Operating system is outside the supported release matrix",
			})
		}
	}
	now := options.Now()
	return resultbuilder.Build(resultbuilder.Input{
		Command: command,
		Tool: protocol.Tool{
			Name: Name, Version: options.Version, Commit: options.Commit,
			BuildDate: options.BuildDate, GoVersion: runtime.Version(), Architecture: runtime.GOARCH,
		},
		Host: options.Host, Started: now, Now: now,
		Checks: collected.Checks, Data: data, Errors: collected.Errors,
	})
}

func structuredError(
	kind protocol.ErrorKind,
	code string,
	message string,
	dependency string,
) protocol.StructuredError {
	return protocol.StructuredError{
		Kind: kind, Code: code, Message: message, Dependency: dependency,
	}
}
