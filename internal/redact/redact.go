package redact

import (
	"reflect"
	"regexp"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const Mask = "********"

var patterns = []struct {
	expression  *regexp.Regexp
	replacement string
}{
	{regexp.MustCompile(`(?i)(authorization\s*:\s*bearer\s+)([^\s,;]+)`), `${1}` + Mask},
	{regexp.MustCompile(`(?i)\b(password|passwd|pwd)(\s*[:=]\s*)([^\s&;,]+)`), `${1}${2}` + Mask},
	{regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]+\b`), `glpat-` + Mask},
	{regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token)(\s*[:=]\s*)([^\s&;,]+)`), `${1}${2}` + Mask},
}

func String(value string) string {
	for _, pattern := range patterns {
		value = pattern.expression.ReplaceAllString(value, pattern.replacement)
	}
	return value
}

func Value(value any) any {
	output := redactValue(reflect.ValueOf(value))
	if !output.IsValid() {
		return nil
	}
	return output.Interface()
}

func redactValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return reflect.Value{}
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		output := reflect.New(value.Type()).Elem()
		output.Set(redactValue(value.Elem()))
		return output
	case reflect.String:
		output := reflect.New(value.Type()).Elem()
		output.SetString(String(value.String()))
		return output
	case reflect.Map:
		if value.IsNil() || value.Type().Key().Kind() != reflect.String {
			return value
		}
		output := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			item := iterator.Value()
			if sensitiveKey(iterator.Key().String()) {
				item = maskedValue(item)
			} else {
				item = redactValue(item)
			}
			output.SetMapIndex(iterator.Key(), item)
		}
		return output
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		output := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := range value.Len() {
			output.Index(index).Set(redactValue(value.Index(index)))
		}
		return output
	case reflect.Array:
		output := reflect.New(value.Type()).Elem()
		for index := range value.Len() {
			output.Index(index).Set(redactValue(value.Index(index)))
		}
		return output
	default:
		return value
	}
}

func maskedValue(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		output := reflect.New(value.Type()).Elem()
		output.Set(reflect.ValueOf(Mask))
		return output
	case reflect.String:
		output := reflect.New(value.Type()).Elem()
		output.SetString(Mask)
		return output
	default:
		return reflect.Zero(value.Type())
	}
}

func Result(input protocol.Result) protocol.Result {
	input.Data = redactMap(input.Data)
	for index := range input.Checks {
		input.Checks[index].Summary = String(input.Checks[index].Summary)
		input.Checks[index].Details = redactMap(input.Checks[index].Details)
	}
	for index := range input.Changes {
		input.Changes[index].Object = String(input.Changes[index].Object)
		input.Changes[index].Details = redactMap(input.Changes[index].Details)
	}
	for index := range input.Errors {
		input.Errors[index].Message = String(input.Errors[index].Message)
		input.Errors[index].Dependency = String(input.Errors[index].Dependency)
		input.Errors[index].Details = redactMap(input.Errors[index].Details)
	}
	return input
}

func redactMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	return Value(input).(map[string]any)
}

func sensitiveKey(key string) bool {
	normalized := strings.NewReplacer("_", "", "-", "", ".", "").Replace(strings.ToLower(key))
	for _, marker := range []string{
		"password", "passwd", "token", "secret", "privatekey", "authorization", "cookie", "apikey",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
