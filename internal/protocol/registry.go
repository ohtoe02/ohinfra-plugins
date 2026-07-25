package protocol

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

type DefinitionSpec struct {
	Name        string
	Version     string
	Description string
}

type CommandSpec struct {
	Path      []string
	Use       string
	Short     string
	Arguments []Argument
	Flags     []Flag
}

type Risk struct {
	RequiresRoot         bool
	RequiresForce        bool
	RequiresConfirmation bool
}

type Handler func(context.Context, Invocation) (Result, error)
type Planner func(context.Context, Invocation) (Plan, error)
type Applier func(context.Context, Invocation, Plan) (Result, error)

type Binding struct {
	command Command
	plan    Planner
	handle  Handler
	apply   Applier
}

func Diagnostic(spec CommandSpec, handler Handler) Binding {
	return Binding{
		command: commandFromSpec(spec, CategoryDiagnostic, Risk{}),
		handle:  handler,
	}
}

func Mutation(
	spec CommandSpec,
	category Category,
	risk Risk,
	planner Planner,
	applier Applier,
) Binding {
	if category != CategoryOperational && category != CategoryRunbook {
		category = ""
	}
	command := commandFromSpec(spec, category, risk)
	command.SupportsDryRun = true
	return Binding{command: command, plan: planner, apply: applier}
}

func NewDefinition(spec DefinitionSpec, bindings ...Binding) Definition {
	commands := make([]Command, 0, len(bindings))
	byPath := make(map[string]Binding, len(bindings))
	for _, binding := range bindings {
		binding.command.Path = slices.Clone(binding.command.Path)
		binding.command.Arguments = slices.Clone(binding.command.Arguments)
		binding.command.Flags = slices.Clone(binding.command.Flags)
		commands = append(commands, binding.command)
		byPath[pathKey(binding.command.Path)] = binding
	}

	definition := Definition{
		Manifest: Manifest{
			ProtocolVersion: ProtocolVersion,
			Name:            spec.Name,
			Version:         spec.Version,
			Description:     spec.Description,
			Commands:        commands,
		},
	}
	definition.Plan = func(ctx context.Context, invocation Invocation) (Plan, error) {
		binding, err := lookupBinding(byPath, invocation)
		if err != nil {
			return Plan{}, err
		}
		if binding.plan == nil {
			return Plan{}, argumentFailure("command does not support planning")
		}
		if err := validateInvocation(binding.command, invocation, false); err != nil {
			return Plan{}, err
		}
		plan, err := binding.plan(ctx, invocation)
		if err != nil {
			return Plan{}, err
		}
		return normalizeBindingPlan(plan, binding.command), nil
	}
	definition.Execute = func(ctx context.Context, invocation Invocation) (Result, error) {
		binding, err := lookupBinding(byPath, invocation)
		if err != nil {
			return Result{}, err
		}
		if err := validateInvocation(binding.command, invocation, binding.apply != nil); err != nil {
			return Result{}, err
		}
		if binding.apply == nil {
			if binding.handle == nil {
				return Result{}, argumentFailure("command does not support execution")
			}
			result, err := binding.handle(ctx, invocation)
			if err != nil {
				return Result{}, err
			}
			return Normalize(result), nil
		}
		current, err := binding.plan(ctx, invocation)
		if err != nil {
			return Result{}, err
		}
		current = normalizeBindingPlan(current, binding.command)
		digest, err := PlanDigest(current)
		if err != nil {
			return Result{}, err
		}
		if invocation.PlanDigest != digest {
			return Result{}, argumentFailure("approved plan digest does not match current plan")
		}
		result, err := binding.apply(ctx, invocation, current)
		if err != nil {
			return Result{}, err
		}
		return Normalize(result), nil
	}
	return definition
}

func commandFromSpec(spec CommandSpec, category Category, risk Risk) Command {
	arguments := slices.Clone(spec.Arguments)
	if arguments == nil {
		arguments = []Argument{}
	}
	flags := slices.Clone(spec.Flags)
	if flags == nil {
		flags = []Flag{}
	}
	return Command{
		Path:                 slices.Clone(spec.Path),
		Use:                  spec.Use,
		Short:                spec.Short,
		Category:             category,
		Arguments:            arguments,
		Flags:                flags,
		RequiresRoot:         risk.RequiresRoot,
		RequiresForce:        risk.RequiresForce,
		RequiresConfirmation: risk.RequiresConfirmation,
	}
}

func lookupBinding(bindings map[string]Binding, invocation Invocation) (Binding, error) {
	binding, found := bindings[pathKey(invocation.CommandPath)]
	if !found || !slices.Equal(binding.command.Path, invocation.CommandPath) {
		return Binding{}, argumentFailure("unsupported plugin command")
	}
	return binding, nil
}

func validateInvocation(command Command, invocation Invocation, mutation bool) error {
	if !slices.Equal(command.Path, invocation.CommandPath) {
		return argumentFailure("invocation command path does not match the registered command")
	}

	minimum := 0
	maximum := len(command.Arguments)
	variadic := false
	for _, argument := range command.Arguments {
		if argument.Required {
			minimum++
		}
		if argument.Variadic {
			variadic = true
		}
	}
	if len(invocation.Arguments) < minimum || (!variadic && len(invocation.Arguments) > maximum) {
		return argumentFailure("invocation argument count does not match the command manifest")
	}

	knownFlags := make(map[string]Flag, len(command.Flags))
	for _, flag := range command.Flags {
		knownFlags[flag.Name] = flag
	}
	for name, value := range invocation.Options {
		flag, found := knownFlags[name]
		if !found {
			return argumentFailure(fmt.Sprintf("unknown option %q", name))
		}
		if !validOptionValue(flag.Type, value) {
			return argumentFailure(fmt.Sprintf("option %q has the wrong type", name))
		}
	}
	if mutation && invocation.PlanDigest == "" {
		return argumentFailure("mutation execution requires an approved plan digest")
	}
	if !mutation && invocation.PlanDigest != "" {
		return argumentFailure("diagnostic execution must not include a plan digest")
	}
	return nil
}

func validOptionValue(flagType string, value any) bool {
	switch flagType {
	case "string", "duration":
		_, ok := value.(string)
		return ok
	case "bool":
		_, ok := value.(bool)
		return ok
	case "int":
		switch typed := value.(type) {
		case int:
			return true
		case float64:
			return !math.IsNaN(typed) && !math.IsInf(typed, 0) &&
				typed == math.Trunc(typed) && typed >= math.MinInt && typed <= math.MaxInt
		default:
			return false
		}
	default:
		return false
	}
}

func normalizeBindingPlan(plan Plan, command Command) Plan {
	plan = NormalizePlan(plan)
	plan.RequiresRoot = command.RequiresRoot
	plan.RequiresForce = command.RequiresForce
	plan.RequiresConfirmation = command.RequiresConfirmation
	return plan
}

func pathKey(path []string) string {
	return strings.Join(path, "\x00")
}

func argumentFailure(message string) error {
	return ExitError{Code: ExitArguments, Err: errors.New(message)}
}
