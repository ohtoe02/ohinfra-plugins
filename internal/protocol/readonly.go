package protocol

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const readOnlyPlanSummary = "Read local system state without making changes"

func ReadOnlyPlan(ctx context.Context, invocation Invocation, manifest Manifest) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if invocation.ProtocolVersion != ProtocolVersion {
		return Plan{}, invalidInvocation("unsupported protocol version")
	}
	if err := ValidateManifest(manifest); err != nil {
		return Plan{}, fmt.Errorf("validate manifest: %w", err)
	}

	command, ok := findCommand(manifest.Commands, invocation.CommandPath)
	if !ok {
		return Plan{}, invalidInvocation(
			fmt.Sprintf("unknown command %q", strings.Join(invocation.CommandPath, " ")),
		)
	}
	if err := validateInvocationArguments(command, invocation.Arguments); err != nil {
		return Plan{}, err
	}
	if err := validateInvocationOptions(command, invocation.Options); err != nil {
		return Plan{}, err
	}

	return Plan{
		CommandID: strings.Join(command.Path, "."),
		Summary:   readOnlyPlanSummary,
		Checks:    []Check{},
		Changes:   []Change{},
		Risks:     []string{},
	}, nil
}

func findCommand(commands []Command, path []string) (Command, bool) {
	for _, command := range commands {
		if len(command.Path) != len(path) {
			continue
		}
		matches := true
		for index := range path {
			if command.Path[index] != path[index] {
				matches = false
				break
			}
		}
		if matches {
			return command, true
		}
	}
	return Command{}, false
}

func validateInvocationArguments(command Command, values []string) error {
	required := 0
	for _, argument := range command.Arguments {
		if argument.Required {
			required++
		}
	}
	if len(values) < required {
		return invalidInvocation(
			fmt.Sprintf("command %q requires at least %d argument(s)", strings.Join(command.Path, " "), required),
		)
	}
	variadic := len(command.Arguments) > 0 && command.Arguments[len(command.Arguments)-1].Variadic
	if !variadic && len(values) > len(command.Arguments) {
		return invalidInvocation(
			fmt.Sprintf("command %q accepts at most %d argument(s)", strings.Join(command.Path, " "), len(command.Arguments)),
		)
	}
	return nil
}

func validateInvocationOptions(command Command, values map[string]any) error {
	flags := make(map[string]Flag, len(command.Flags))
	for _, flag := range command.Flags {
		flags[flag.Name] = flag
	}
	for name, value := range values {
		flag, ok := flags[name]
		if !ok {
			return invalidInvocation(fmt.Sprintf("unknown option %q", name))
		}
		if !validInvocationOption(flag.Type, value) {
			return invalidInvocation(fmt.Sprintf("option %q must be %s", name, flag.Type))
		}
	}
	return nil
}

func validInvocationOption(flagType string, value any) bool {
	switch flagType {
	case "string":
		_, ok := value.(string)
		return ok
	case "bool":
		_, ok := value.(bool)
		return ok
	case "int":
		switch number := value.(type) {
		case int:
			return true
		case float64:
			return float64(int(number)) == number
		default:
			return false
		}
	case "duration":
		duration, ok := value.(string)
		if !ok {
			return false
		}
		_, err := time.ParseDuration(duration)
		return err == nil
	default:
		return false
	}
}

func invalidInvocation(message string) error {
	return ExitError{Code: ExitArguments, Err: fmt.Errorf("%s", message)}
}
