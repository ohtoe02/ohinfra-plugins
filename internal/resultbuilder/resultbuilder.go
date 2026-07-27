package resultbuilder

import (
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/redact"
)

type Input struct {
	Command string
	Tool    protocol.Tool
	Host    string
	Started time.Time
	Now     time.Time
	Checks  []protocol.Check
	Data    map[string]any
	Changes []protocol.Change
	Errors  []protocol.StructuredError
}

func Build(input Input) protocol.Result {
	checks := cloneChecks(input.Checks)
	changes := cloneChanges(input.Changes)
	errors := cloneErrors(input.Errors)

	sort.SliceStable(checks, func(left, right int) bool {
		return checks[left].ID < checks[right].ID
	})
	sort.SliceStable(changes, func(left, right int) bool {
		if changes[left].Object == changes[right].Object {
			return changes[left].Action < changes[right].Action
		}
		return changes[left].Object < changes[right].Object
	})

	duration := int64(0)
	if !input.Started.IsZero() && input.Now.After(input.Started) {
		duration = input.Now.Sub(input.Started).Milliseconds()
	}

	result := protocol.Result{
		Command:    input.Command,
		Status:     classify(checks, errors),
		Timestamp:  input.Now.Format(time.RFC3339Nano),
		DurationMS: duration,
		Host:       input.Host,
		Tool:       input.Tool,
		Checks:     checks,
		Data:       cloneMap(input.Data),
		Changes:    changes,
		Errors:     errors,
	}
	removeStderr(result.Data)
	for index := range result.Checks {
		removeStderr(result.Checks[index].Details)
	}
	for index := range result.Changes {
		removeStderr(result.Changes[index].Details)
	}
	for index := range result.Errors {
		removeStderr(result.Errors[index].Details)
	}
	return protocol.Normalize(redact.Result(result))
}

func classify(checks []protocol.Check, errors []protocol.StructuredError) protocol.Status {
	hasWarning := false
	hasPartial := len(errors) > 0
	for _, check := range checks {
		switch check.Status {
		case protocol.StatusCritical:
			return protocol.StatusCritical
		case protocol.StatusWarning:
			hasWarning = true
		case protocol.StatusPass, protocol.StatusInfo:
		default:
			hasPartial = true
		}
	}
	if hasWarning {
		return protocol.StatusWarning
	}
	if hasPartial {
		return protocol.StatusPartial
	}
	return protocol.StatusPass
}

func cloneChecks(input []protocol.Check) []protocol.Check {
	if input == nil {
		return nil
	}
	output := make([]protocol.Check, len(input))
	for index, check := range input {
		output[index] = check
		output[index].Details = cloneMap(check.Details)
	}
	return output
}

func cloneChanges(input []protocol.Change) []protocol.Change {
	if input == nil {
		return nil
	}
	output := make([]protocol.Change, len(input))
	for index, change := range input {
		output[index] = change
		output[index].Details = cloneMap(change.Details)
	}
	return output
}

func cloneErrors(input []protocol.StructuredError) []protocol.StructuredError {
	if input == nil {
		return nil
	}
	output := make([]protocol.StructuredError, len(input))
	for index, structuredError := range input {
		output[index] = structuredError
		output[index].Details = cloneMap(structuredError.Details)
	}
	return output
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = cloneValue(value)
	}
	return output
}

func cloneValue(value any) any {
	cloned := cloneJSONValue(reflect.ValueOf(value))
	if !cloned.IsValid() {
		return nil
	}
	return cloned.Interface()
}

func cloneJSONValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return reflect.Value{}
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneJSONValue(value.Elem())
		output := reflect.New(value.Type()).Elem()
		output.Set(cloned)
		return output
	case reflect.Map:
		if value.IsNil() || value.Type().Key().Kind() != reflect.String {
			return value
		}
		output := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			output.SetMapIndex(iterator.Key(), cloneJSONValue(iterator.Value()))
		}
		return output
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		output := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := range value.Len() {
			output.Index(index).Set(cloneJSONValue(value.Index(index)))
		}
		return output
	default:
		return value
	}
}

func removeStderr(input map[string]any) {
	removeStderrValue(reflect.ValueOf(input))
}

func removeStderrValue(value reflect.Value) {
	if !value.IsValid() {
		return
	}
	switch value.Kind() {
	case reflect.Interface:
		if !value.IsNil() {
			removeStderrValue(value.Elem())
		}
	case reflect.Map:
		if value.IsNil() || value.Type().Key().Kind() != reflect.String {
			return
		}
		for _, key := range value.MapKeys() {
			normalized := strings.NewReplacer("_", "", "-", "", ".", "").
				Replace(strings.ToLower(key.String()))
			if strings.HasPrefix(normalized, "stderr") {
				value.SetMapIndex(key, reflect.Value{})
				continue
			}
			removeStderrValue(value.MapIndex(key))
		}
	case reflect.Slice, reflect.Array:
		for index := range value.Len() {
			removeStderrValue(value.Index(index))
		}
	}
}
