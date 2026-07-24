package redact

import (
	"regexp"
	"strings"

	"github.com/ohtoe02/ohinfra-plugins/internal/protocol"
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
	switch typed := value.(type) {
	case string:
		return String(typed)
	case map[string]any:
		output := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveKey(key) {
				output[key] = Mask
			} else {
				output[key] = Value(item)
			}
		}
		return output
	case map[string]string:
		output := make(map[string]string, len(typed))
		for key, item := range typed {
			if sensitiveKey(key) {
				output[key] = Mask
			} else {
				output[key] = String(item)
			}
		}
		return output
	case []any:
		output := make([]any, len(typed))
		for index, item := range typed {
			output[index] = Value(item)
		}
		return output
	case []string:
		output := make([]string, len(typed))
		for index, item := range typed {
			output[index] = String(item)
		}
		return output
	default:
		return value
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
