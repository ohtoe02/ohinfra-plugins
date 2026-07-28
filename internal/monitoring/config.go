package monitoring

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const configFileLimit = 256 << 10

type ConfigMetadata struct {
	ProductID string `json:"product_id"`
	Path      string `json:"path"`
	Format    string `json:"format"`
	SizeBytes int    `json:"size_bytes"`
	Valid     bool   `json:"valid"`
}

type configIssue struct {
	productID string
	code      string
	message   string
}

func executeConfig(
	ctx context.Context,
	options Options,
	local probe.Local,
) (protocol.Result, error) {
	configs, issues, err := collectConfigs(ctx, local)
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return protocol.Result{}, fatal
		}
		return protocol.Result{}, dependencyFailure("local monitoring config metadata is unavailable")
	}
	checks := []protocol.Check{{
		ID: "monitoring.config", Status: protocol.StatusPass,
		Summary: "Local monitoring config metadata is valid",
	}}
	if len(configs) == 0 && len(issues) == 0 {
		checks[0].Status = protocol.StatusInfo
		checks[0].Summary = "No local monitoring config files were found"
	}
	failures := configFailures(issues)
	if len(issues) > 0 {
		checks[0].Status = protocol.StatusPartial
		checks[0].Summary = "Some local monitoring config metadata is invalid"
	}
	return buildResult(options, "monitoring config",
		map[string]any{"configs": configs}, checks, failures), nil
}

func collectConfigs(
	ctx context.Context,
	local probe.Local,
) ([]ConfigMetadata, []configIssue, error) {
	configs := []ConfigMetadata{}
	issues := []configIssue{}
	issueKeys := map[string]bool{}
	addIssue := func(issue configIssue) {
		key := issue.productID + "\x00" + issue.code
		if issueKeys[key] {
			return
		}
		issueKeys[key] = true
		issues = append(issues, issue)
	}
	for _, product := range products {
		for _, relative := range product.ConfigPaths {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			path := filepath.FromSlash(relative)
			data, err := local.Read(path, configFileLimit)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				addIssue(configIssue{
					productID: product.ID, code: "unsafe_config",
					message: "A local monitoring config file could not be safely inspected",
				})
				continue
			}
			valid := validConfig(product.ConfigFormat, data)
			configs = append(configs, ConfigMetadata{
				ProductID: product.ID,
				Path:      "/" + filepath.ToSlash(relative),
				Format:    product.ConfigFormat, SizeBytes: len(data), Valid: valid,
			})
			if !valid {
				addIssue(configIssue{
					productID: product.ID, code: "malformed_config",
					message: "A local monitoring config file is malformed",
				})
			}
		}
	}
	return configs, issues, nil
}

func configFailures(issues []configIssue) []protocol.StructuredError {
	failures := make([]protocol.StructuredError, 0, len(issues))
	for _, issue := range issues {
		failures = append(failures, protocol.StructuredError{
			Kind: protocol.ErrorConfiguration, Code: issue.code,
			Message: issue.message, Retryable: false,
			Details: map[string]any{"product": issue.productID},
		})
	}
	return failures
}

func validConfig(format string, data []byte) bool {
	if len(data) == 0 || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return false
	}
	switch format {
	case "yaml":
		return validYAMLLike(data)
	case "toml":
		return validAssignments(data, true)
	case "properties", "environment":
		return validAssignments(data, false)
	case "alloy":
		return validAlloyLike(data)
	default:
		return false
	}
}

func validYAMLLike(data []byte) bool {
	found := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") ||
			line == "---" || line == "..." {
			continue
		}
		found = true
		if strings.HasPrefix(line, "- ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
			if line == "" {
				continue
			}
		}
		if !strings.Contains(line, ":") &&
			line != "[]" && line != "{}" {
			return false
		}
	}
	return found
}

func validAssignments(data []byte, sections bool) bool {
	found := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		found = true
		if sections && strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			continue
		}
		if !strings.Contains(line, "=") {
			return false
		}
	}
	return found
}

func validAlloyLike(data []byte) bool {
	found := false
	depth := 0
	quoted := false
	escaped := false
	for _, character := range string(data) {
		if escaped {
			escaped = false
			continue
		}
		if character == '\\' && quoted {
			escaped = true
			continue
		}
		if character == '"' {
			quoted = !quoted
			continue
		}
		if quoted {
			continue
		}
		switch character {
		case '{', '[', '(':
			depth++
			found = true
		case '}', ']', ')':
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return found && !quoted && depth == 0
}
