package protocol

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func ValidateDescription(value string) error {
	if value == "" {
		return nil
	}
	if !utf8.ValidString(value) {
		return errors.New("description must be valid UTF-8")
	}
	if len(value) > 512 {
		return errors.New("description exceeds 512 bytes")
	}
	if strings.TrimSpace(value) != value {
		return errors.New("description must not have surrounding whitespace")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("description must be a single line without control characters")
		}
	}
	return nil
}

func ValidateManifest(manifest Manifest) error {
	if manifest.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf("protocol_version must be %d", ProtocolVersion)
	}
	if !identifier.MatchString(manifest.Name) {
		return fmt.Errorf("invalid plugin name %q", manifest.Name)
	}
	if strings.TrimSpace(manifest.Version) == "" {
		return errors.New("plugin version must not be empty")
	}
	if err := ValidateDescription(manifest.Description); err != nil {
		return err
	}
	if len(manifest.Commands) == 0 || len(manifest.Commands) > 128 {
		return errors.New("plugin command count must be within 1..128")
	}
	seen := map[string]struct{}{}
	for _, command := range manifest.Commands {
		if err := validateCommand(command); err != nil {
			return err
		}
		key := strings.Join(command.Path, " ")
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate command %q", key)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateCommand(command Command) error {
	if len(command.Path) < 2 {
		return errors.New("plugin command path must contain at least two segments")
	}
	for _, segment := range command.Path {
		if !identifier.MatchString(segment) {
			return fmt.Errorf("invalid command segment %q", segment)
		}
	}
	switch command.Category {
	case CategoryDiagnostic:
	case CategoryOperational, CategoryRunbook:
		if !command.SupportsDryRun {
			return fmt.Errorf("mutating command %q must support dry-run", strings.Join(command.Path, " "))
		}
	default:
		return fmt.Errorf("invalid command category %q", command.Category)
	}
	if len(command.Arguments) > 64 || len(command.Flags) > 64 {
		return errors.New("command argument or flag count exceeds 64")
	}
	optionalSeen := false
	for index, argument := range command.Arguments {
		if !identifier.MatchString(argument.Name) {
			return fmt.Errorf("invalid argument name %q", argument.Name)
		}
		if argument.Required && optionalSeen {
			return fmt.Errorf("required argument %q follows an optional argument", argument.Name)
		}
		if !argument.Required {
			optionalSeen = true
		}
		if argument.Variadic && index != len(command.Arguments)-1 {
			return fmt.Errorf("variadic argument %q must be last", argument.Name)
		}
	}
	seenFlags := map[string]struct{}{}
	for _, flag := range command.Flags {
		if !identifier.MatchString(flag.Name) {
			return fmt.Errorf("invalid flag name %q", flag.Name)
		}
		if _, duplicate := seenFlags[flag.Name]; duplicate {
			return fmt.Errorf("duplicate flag %q", flag.Name)
		}
		seenFlags[flag.Name] = struct{}{}
		if err := validateFlag(flag); err != nil {
			return err
		}
	}
	return nil
}

func validateFlag(flag Flag) error {
	if flag.Default == nil {
		switch flag.Type {
		case "string", "bool", "int", "duration":
			return nil
		default:
			return fmt.Errorf("flag %q has invalid type %q", flag.Name, flag.Type)
		}
	}
	switch flag.Type {
	case "string":
		if _, ok := flag.Default.(string); !ok {
			return fmt.Errorf("flag %q default must be a string", flag.Name)
		}
	case "bool":
		if _, ok := flag.Default.(bool); !ok {
			return fmt.Errorf("flag %q default must be a boolean", flag.Name)
		}
	case "int":
		switch value := flag.Default.(type) {
		case int:
		case float64:
			if float64(int(value)) != value {
				return fmt.Errorf("flag %q default must be an integer", flag.Name)
			}
		default:
			return fmt.Errorf("flag %q default must be an integer", flag.Name)
		}
	case "duration":
		value, ok := flag.Default.(string)
		if !ok {
			return fmt.Errorf("flag %q default must be a duration string", flag.Name)
		}
		if _, err := time.ParseDuration(value); err != nil {
			return fmt.Errorf("flag %q default is invalid: %w", flag.Name, err)
		}
	default:
		return fmt.Errorf("flag %q has invalid type %q", flag.Name, flag.Type)
	}
	return nil
}
