package runbook

import (
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/resultbuilder"
)

const (
	Name        = "runbook-base"
	Description = "Read-only local runbook discovery and validation for ohtools."
)

type Options struct {
	Version   string
	Commit    string
	BuildDate string
	Root      string
	Host      string
	Now       func() time.Time
}

func execute(
	ctx context.Context,
	invocation protocol.Invocation,
	options Options,
) (protocol.Result, error) {
	switch {
	case slices.Equal(invocation.CommandPath, []string{"runbook", "list"}):
		documents, err := loadDocuments(ctx, options.Root)
		if err != nil {
			return protocol.Result{}, configurationFailure(err)
		}
		summaries := make([]RunbookSummary, 0, len(documents))
		for _, document := range documents {
			summaries = append(summaries, RunbookSummary{
				Name: document.Name, Title: document.Title,
				Description: document.Description, StepCount: len(document.Steps),
			})
		}
		return buildResult(options, "runbook list", map[string]any{
			"runbooks": summaries,
		}), nil
	case slices.Equal(invocation.CommandPath, []string{"runbook", "inspect"}):
		name := invocation.Arguments[0]
		if !runbookName.MatchString(name) {
			return protocol.Result{}, invalidNameFailure()
		}
		documents, err := loadDocuments(ctx, options.Root)
		if err != nil {
			return protocol.Result{}, configurationFailure(err)
		}
		for _, document := range documents {
			if document.Name == name {
				return buildResult(options, "runbook inspect", map[string]any{
					"runbook": renderDocument(document),
				}), nil
			}
		}
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitDependency, Err: errors.New("runbook not found"),
		}
	case slices.Equal(invocation.CommandPath, []string{"runbook", "validate"}):
		selected := ""
		if len(invocation.Arguments) == 1 {
			selected = invocation.Arguments[0]
			if !runbookName.MatchString(selected) {
				return protocol.Result{}, invalidNameFailure()
			}
		}
		documents, err := loadDocuments(ctx, options.Root)
		if err != nil {
			return protocol.Result{}, configurationFailure(err)
		}
		validations := validationViews(documents, selected)
		if selected != "" && len(validations) == 0 {
			return protocol.Result{}, protocol.ExitError{
				Code: protocol.ExitDependency, Err: errors.New("runbook not found"),
			}
		}
		return buildResult(options, "runbook validate", map[string]any{
			"validations": validations,
		}), nil
	}
	return protocol.Result{}, protocol.ExitError{
		Code: protocol.ExitArguments, Err: errors.New("unsupported runbook-base command"),
	}
}

func invalidNameFailure() error {
	return protocol.ExitError{
		Code: protocol.ExitArguments, Err: errors.New("invalid runbook name"),
	}
}

func buildResult(options Options, command string, data map[string]any) protocol.Result {
	now := options.Now()
	return resultbuilder.Build(resultbuilder.Input{
		Command: command,
		Tool: protocol.Tool{
			Name: Name, Version: options.Version, Commit: options.Commit,
			BuildDate: options.BuildDate, GoVersion: runtime.Version(),
			Architecture: runtime.GOARCH,
		},
		Host: options.Host, Started: now, Now: now, Data: data,
	})
}

func configurationFailure(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return protocol.ExitError{
		Code: protocol.ExitConfiguration, Err: errors.New("invalid local runbook collection"),
	}
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
			diagnostic(
				[]string{"runbook", "list"},
				"list",
				"List local runbook documents",
				[]protocol.Argument{},
			),
			diagnostic(
				[]string{"runbook", "inspect"},
				"inspect <name>",
				"Inspect a local runbook document",
				[]protocol.Argument{{
					Name: "name", Description: "Runbook name", Required: true,
				}},
			),
			diagnostic(
				[]string{"runbook", "validate"},
				"validate [name]",
				"Validate local runbook documents",
				[]protocol.Argument{{
					Name: "name", Description: "Optional runbook name",
				}},
			),
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

func diagnostic(
	path []string,
	use string,
	short string,
	arguments []protocol.Argument,
) protocol.Command {
	return protocol.Command{
		Path: path, Use: use, Short: short,
		Category: protocol.CategoryDiagnostic, Arguments: arguments, Flags: []protocol.Flag{},
	}
}
