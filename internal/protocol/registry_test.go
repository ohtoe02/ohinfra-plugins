package protocol

import (
	"context"
	"errors"
	"testing"
)

func TestDefinitionRegistryDispatchesExactPathsAndValidatesInvocationShape(t *testing.T) {
	t.Parallel()

	var calls int
	definition := NewDefinition(
		DefinitionSpec{Name: "fixture-base", Version: "1.0.0", Description: "Protocol fixture"},
		Diagnostic(CommandSpec{
			Path:      []string{"fixture", "show"},
			Use:       "show <name>",
			Short:     "Show a fixture",
			Arguments: []Argument{{Name: "name", Required: true}},
			Flags:     []Flag{{Name: "verbose", Type: "bool", Default: false}},
		}, func(_ context.Context, invocation Invocation) (Result, error) {
			calls++
			return Result{Command: "fixture show", Status: StatusPass}, nil
		}),
	)

	valid := Invocation{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "request-1",
		CommandPath:     []string{"fixture", "show"},
		Arguments:       []string{"one"},
		Options:         map[string]any{"verbose": true},
	}
	result, err := definition.Execute(context.Background(), valid)
	if err != nil {
		t.Fatalf("valid invocation failed: %v", err)
	}
	if result.SchemaVersion != SchemaVersion || result.Checks == nil ||
		result.Data == nil || result.Changes == nil || result.Errors == nil {
		t.Fatalf("result was not normalized: %#v", result)
	}
	if calls != 1 {
		t.Fatalf("handler calls = %d", calls)
	}

	for _, invalid := range []Invocation{
		{ProtocolVersion: ProtocolVersion, RequestID: "request-2", CommandPath: []string{"fixture", "other"}, Arguments: []string{"one"}, Options: map[string]any{}},
		{ProtocolVersion: ProtocolVersion, RequestID: "request-3", CommandPath: []string{"fixture", "show"}, Arguments: []string{}, Options: map[string]any{}},
		{ProtocolVersion: ProtocolVersion, RequestID: "request-4", CommandPath: []string{"fixture", "show"}, Arguments: []string{"one"}, Options: map[string]any{"unknown": true}},
		{ProtocolVersion: ProtocolVersion, RequestID: "request-5", CommandPath: []string{"fixture", "show"}, Arguments: []string{"one"}, Options: map[string]any{"verbose": "yes"}},
	} {
		if _, err := definition.Execute(context.Background(), invalid); err == nil {
			t.Fatalf("invalid invocation accepted: %#v", invalid)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid invocations reached handler: calls=%d", calls)
	}
}

func TestMutationUsesOneRiskSourceAndReplansBeforeApply(t *testing.T) {
	t.Parallel()

	plans := []Plan{
		{
			CommandID: "fixture.apply",
			Summary:   "Apply fixture",
			Changes:   []Change{{Object: "fixture", Action: "write", Status: "planned"}},
		},
		{
			CommandID: "fixture.apply",
			Summary:   "Apply fixture",
			Changes:   []Change{{Object: "fixture", Action: "write", Status: "planned"}},
		},
	}
	planCalls := 0
	applyCalls := 0
	definition := NewDefinition(
		DefinitionSpec{Name: "fixture-base", Version: "1.0.0"},
		Mutation(
			CommandSpec{Path: []string{"fixture", "apply"}, Use: "apply", Short: "Apply fixture"},
			CategoryOperational,
			Risk{RequiresRoot: true, RequiresForce: true, RequiresConfirmation: true},
			func(context.Context, Invocation) (Plan, error) {
				plan := plans[planCalls]
				planCalls++
				return plan, nil
			},
			func(_ context.Context, _ Invocation, approved Plan) (Result, error) {
				applyCalls++
				if !approved.RequiresRoot || !approved.RequiresForce || !approved.RequiresConfirmation {
					return Result{}, errors.New("risk flags were not normalized")
				}
				return Normalize(Result{Command: "fixture apply", Status: StatusPass}), nil
			},
		),
	)

	command := definition.Manifest.Commands[0]
	if command.Category != CategoryOperational || !command.SupportsDryRun ||
		!command.RequiresRoot || !command.RequiresForce || !command.RequiresConfirmation {
		t.Fatalf("manifest risk flags = %#v", command)
	}

	invocation := Invocation{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "request-1",
		CommandPath:     []string{"fixture", "apply"},
		Arguments:       []string{},
		Options:         map[string]any{},
	}
	plan, err := definition.Plan(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RequiresRoot || !plan.RequiresForce || !plan.RequiresConfirmation {
		t.Fatalf("plan risk flags = %#v", plan)
	}
	invocation.PlanDigest, err = PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := definition.Execute(context.Background(), invocation); err != nil {
		t.Fatalf("execute failed: %v", err)
	}
	if planCalls != 2 || applyCalls != 1 {
		t.Fatalf("plan calls=%d apply calls=%d", planCalls, applyCalls)
	}
}

func TestMutationRejectsPlanThatChangedAfterApproval(t *testing.T) {
	t.Parallel()

	plans := []Plan{
		{CommandID: "fixture.apply", Summary: "first"},
		{CommandID: "fixture.apply", Summary: "changed"},
	}
	planCalls := 0
	applyCalls := 0
	definition := NewDefinition(
		DefinitionSpec{Name: "fixture-base", Version: "1.0.0"},
		Mutation(
			CommandSpec{Path: []string{"fixture", "apply"}, Use: "apply", Short: "Apply fixture"},
			CategoryRunbook,
			Risk{RequiresConfirmation: true},
			func(context.Context, Invocation) (Plan, error) {
				plan := plans[planCalls]
				planCalls++
				return plan, nil
			},
			func(context.Context, Invocation, Plan) (Result, error) {
				applyCalls++
				return Result{}, nil
			},
		),
	)
	invocation := Invocation{
		ProtocolVersion: ProtocolVersion,
		RequestID:       "request-1",
		CommandPath:     []string{"fixture", "apply"},
		Arguments:       []string{},
		Options:         map[string]any{},
	}
	approved, err := definition.Plan(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	invocation.PlanDigest, err = PlanDigest(approved)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := definition.Execute(context.Background(), invocation); err == nil {
		t.Fatal("execute accepted a stale plan")
	}
	if planCalls != 2 || applyCalls != 0 {
		t.Fatalf("plan calls=%d apply calls=%d", planCalls, applyCalls)
	}
}

func TestMutationRejectsDiagnosticCategory(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(
		DefinitionSpec{Name: "fixture-base", Version: "1.0.0"},
		Mutation(
			CommandSpec{Path: []string{"fixture", "apply"}, Use: "apply", Short: "Apply fixture"},
			CategoryDiagnostic,
			Risk{},
			func(context.Context, Invocation) (Plan, error) { return Plan{CommandID: "fixture.apply"}, nil },
			func(context.Context, Invocation, Plan) (Result, error) { return Result{}, nil },
		),
	)
	if err := ValidateManifest(definition.Manifest); err == nil {
		t.Fatal("mutation accepted the diagnostic category")
	}
}

func TestValidateManifestRejectsUseMismatchUnsafeHelpAndReservedFlags(t *testing.T) {
	t.Parallel()

	valid := Manifest{
		ProtocolVersion: ProtocolVersion,
		Name:            "fixture-base",
		Version:         "1.0.0",
		Commands: []Command{{
			Path: []string{"fixture", "show"}, Use: "show [name]", Short: "Show a fixture",
			Category: CategoryDiagnostic, Arguments: []Argument{}, Flags: []Flag{},
		}},
	}
	cases := []struct {
		name   string
		mutate func(*Command)
	}{
		{name: "use mismatch", mutate: func(command *Command) { command.Use = "other" }},
		{name: "unsafe use", mutate: func(command *Command) { command.Use = "show\x1b[31m" }},
		{name: "unsafe short", mutate: func(command *Command) { command.Short = "show\nfixture" }},
		{name: "unsafe argument description", mutate: func(command *Command) {
			command.Arguments = []Argument{{Name: "name", Description: "bad\x00description"}}
		}},
		{name: "duplicate argument", mutate: func(command *Command) {
			command.Arguments = []Argument{{Name: "name"}, {Name: "name"}}
		}},
		{name: "unsafe flag description", mutate: func(command *Command) {
			command.Flags = []Flag{{Name: "verbose", Type: "bool", Description: " bad"}}
		}},
		{name: "reserved retry flag", mutate: func(command *Command) {
			command.Flags = []Flag{{Name: "retry-request-id", Type: "string"}}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			manifest := valid
			manifest.Commands = append([]Command(nil), valid.Commands...)
			test.mutate(&manifest.Commands[0])
			if err := ValidateManifest(manifest); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}
