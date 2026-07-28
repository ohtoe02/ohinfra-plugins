package gitlabrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	configRelativePath = "etc/gitlab-runner/config.toml"
	configDisplayPath  = "/etc/gitlab-runner/config.toml"
	configFileLimit    = 1 << 20
	maxConfigLines     = 16384
	maxRunners         = 256
	maxMetadataText    = 512
)

var (
	bareTOMLKey  = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	tomlInteger  = regexp.MustCompile(`^[+-]?[0-9](?:_?[0-9])*$`)
	tomlFloat    = regexp.MustCompile(`^[+-]?(?:[0-9](?:_?[0-9])*)\.[0-9](?:_?[0-9])*(?:[eE][+-]?[0-9](?:_?[0-9])*)?$`)
	tomlDateTime = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}(?:[Tt ][0-9:.+-]+[Zz]?)?$`)
	safeText     = regexp.MustCompile(`^[^\x00-\x1f\x7f]{0,512}$`)
)

type SafeSettings struct {
	Concurrent      int    `json:"concurrent,omitempty"`
	CheckInterval   int    `json:"check_interval,omitempty"`
	LogLevel        string `json:"log_level,omitempty"`
	ShutdownTimeout int    `json:"shutdown_timeout,omitempty"`
}

type RunnerMetadata struct {
	Executor           string `json:"executor"`
	Limit              int    `json:"limit,omitempty"`
	RequestConcurrency int    `json:"request_concurrency,omitempty"`
	Shell              string `json:"shell,omitempty"`
}

type ConfigMetadata struct {
	Path        string           `json:"path"`
	Size        int              `json:"size"`
	RunnerCount int              `json:"runner_count"`
	Settings    SafeSettings     `json:"settings"`
	Runners     []RunnerMetadata `json:"runners"`
}

func executeConfig(ctx context.Context, options Options) (protocol.Result, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	metadata, err := readConfig(ctx, options)
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return protocol.Result{}, fatal
		}
		if errors.Is(err, os.ErrNotExist) {
			return protocol.Result{}, dependencyFailure(
				"local GitLab Runner configuration is unavailable")
		}
		return protocol.Result{}, configurationFailure(
			"local GitLab Runner configuration is not safe strict TOML")
	}
	return buildResult(options, "gitlab-runner config",
		map[string]any{"config": metadata},
		[]protocol.Check{{
			ID: "gitlab-runner.config", Status: protocol.StatusPass,
			Summary: "Safe local GitLab Runner configuration metadata is available",
		}}, nil), nil
}

func readConfig(ctx context.Context, options Options) (ConfigMetadata, error) {
	local := probe.Local{Root: options.Root}
	content, err := local.Read(configRelativePath, configFileLimit)
	if err != nil {
		return ConfigMetadata{}, err
	}
	if err := ctx.Err(); err != nil {
		return ConfigMetadata{}, err
	}
	metadata, err := parseConfig(content)
	if err != nil {
		return ConfigMetadata{}, err
	}
	metadata.Path = configDisplayPath
	metadata.Size = len(content)
	metadata.RunnerCount = len(metadata.Runners)
	return metadata, nil
}

func parseConfig(content []byte) (ConfigMetadata, error) {
	if !utf8.Valid(content) || strings.IndexByte(string(content), 0) >= 0 {
		return ConfigMetadata{}, errors.New("configuration is not valid UTF-8")
	}
	lines := strings.Split(strings.ReplaceAll(string(content), "\r\n", "\n"), "\n")
	if len(lines) > maxConfigLines {
		return ConfigMetadata{}, errors.New("configuration line limit exceeded")
	}

	metadata := ConfigMetadata{Runners: []RunnerMetadata{}}
	table := ""
	runnerIndex := -1
	seen := map[string]struct{}{}
	for lineNumber, raw := range lines {
		line, err := stripTOMLComment(raw)
		if err != nil {
			return ConfigMetadata{}, lineError(lineNumber, err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[[") {
			name, err := parseTableHeader(line, true)
			if err != nil || name != "runners" {
				if err == nil {
					err = errors.New("unsupported array table")
				}
				return ConfigMetadata{}, lineError(lineNumber, err)
			}
			if len(metadata.Runners) >= maxRunners {
				return ConfigMetadata{}, errors.New("runner limit exceeded")
			}
			metadata.Runners = append(metadata.Runners, RunnerMetadata{})
			runnerIndex = len(metadata.Runners) - 1
			table = "runners"
			continue
		}
		if strings.HasPrefix(line, "[") {
			name, err := parseTableHeader(line, false)
			if err != nil {
				return ConfigMetadata{}, lineError(lineNumber, err)
			}
			if strings.HasPrefix(name, "runners.") && runnerIndex < 0 {
				return ConfigMetadata{}, lineError(lineNumber,
					errors.New("runner table precedes runner declaration"))
			}
			table = name
			continue
		}

		key, rawValue, err := splitTOMLAssignment(line)
		if err != nil {
			return ConfigMetadata{}, lineError(lineNumber, err)
		}
		value, err := parseTOMLValue(rawValue)
		if err != nil {
			return ConfigMetadata{}, lineError(lineNumber, err)
		}
		scope := table
		if strings.HasPrefix(table, "runners") {
			scope = fmt.Sprintf("%s#%d", table, runnerIndex)
		}
		identity := scope + "\x00" + key
		if _, duplicate := seen[identity]; duplicate {
			return ConfigMetadata{}, lineError(lineNumber,
				errors.New("duplicate key"))
		}
		seen[identity] = struct{}{}

		switch {
		case table == "":
			if err := applyGlobalSetting(&metadata.Settings, key, value); err != nil {
				return ConfigMetadata{}, lineError(lineNumber, err)
			}
		case table == "runners":
			if err := applyRunnerSetting(&metadata.Runners[runnerIndex], key, value); err != nil {
				return ConfigMetadata{}, lineError(lineNumber, err)
			}
		}
	}
	for _, runner := range metadata.Runners {
		if runner.Executor == "" {
			return ConfigMetadata{}, errors.New("runner is missing a supported executor")
		}
	}
	return metadata, nil
}

func stripTOMLComment(line string) (string, error) {
	quote := byte(0)
	escaped := false
	for index := 0; index < len(line); index++ {
		character := line[index]
		if quote == '"' && escaped {
			escaped = false
			continue
		}
		if quote == '"' && character == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		switch character {
		case '"', '\'':
			quote = character
		case '#':
			return line[:index], nil
		}
	}
	if quote != 0 || escaped {
		return "", errors.New("unterminated string")
	}
	return line, nil
}

func parseTableHeader(line string, array bool) (string, error) {
	open, close := "[", "]"
	if array {
		open, close = "[[", "]]"
	}
	if !strings.HasPrefix(line, open) || !strings.HasSuffix(line, close) {
		return "", errors.New("malformed table header")
	}
	name := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, open), close))
	if name == "" {
		return "", errors.New("empty table header")
	}
	for _, component := range strings.Split(name, ".") {
		if !bareTOMLKey.MatchString(component) {
			return "", errors.New("invalid table name")
		}
	}
	return name, nil
}

func splitTOMLAssignment(line string) (string, string, error) {
	quote := byte(0)
	escaped := false
	depth := 0
	for index := 0; index < len(line); index++ {
		character := line[index]
		if quote == '"' && escaped {
			escaped = false
			continue
		}
		if quote == '"' && character == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		switch character {
		case '"', '\'':
			quote = character
		case '[', '{':
			depth++
		case ']', '}':
			depth--
			if depth < 0 {
				return "", "", errors.New("unbalanced value")
			}
		case '=':
			if depth == 0 {
				key := strings.TrimSpace(line[:index])
				value := strings.TrimSpace(line[index+1:])
				if !bareTOMLKey.MatchString(key) || value == "" {
					return "", "", errors.New("invalid assignment")
				}
				return key, value, nil
			}
		}
	}
	return "", "", errors.New("missing assignment")
}

func parseTOMLValue(raw string) (any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty value")
	}
	if raw[0] == '"' {
		if !validTOMLBasicEscapes(raw) {
			return nil, errors.New("invalid TOML string escape")
		}
		value, err := strconv.Unquote(raw)
		if err != nil || !safeText.MatchString(value) || len(value) > maxMetadataText {
			return nil, errors.New("invalid basic string")
		}
		return value, nil
	}
	if raw[0] == '\'' {
		if len(raw) < 2 || raw[len(raw)-1] != '\'' {
			return nil, errors.New("invalid literal string")
		}
		value := raw[1 : len(raw)-1]
		if strings.Contains(value, "'") || !safeText.MatchString(value) ||
			len(value) > maxMetadataText {
			return nil, errors.New("invalid literal string")
		}
		return value, nil
	}
	switch raw {
	case "true":
		return true, nil
	case "false":
		return false, nil
	}
	if tomlInteger.MatchString(raw) {
		value, err := strconv.ParseInt(strings.ReplaceAll(raw, "_", ""), 10, 64)
		if err != nil {
			return nil, errors.New("invalid integer")
		}
		return value, nil
	}
	if tomlFloat.MatchString(raw) || tomlDateTime.MatchString(raw) {
		return raw, nil
	}
	if raw[0] == '[' && raw[len(raw)-1] == ']' {
		for _, item := range splitTOMLCollection(raw[1 : len(raw)-1]) {
			if item == "" {
				continue
			}
			if _, err := parseTOMLValue(item); err != nil {
				return nil, err
			}
		}
		return raw, nil
	}
	if raw[0] == '{' && raw[len(raw)-1] == '}' {
		seen := map[string]struct{}{}
		for _, item := range splitTOMLCollection(raw[1 : len(raw)-1]) {
			if item == "" {
				continue
			}
			key, value, err := splitTOMLAssignment(item)
			if err != nil {
				return nil, err
			}
			if _, duplicate := seen[key]; duplicate {
				return nil, errors.New("duplicate inline table key")
			}
			seen[key] = struct{}{}
			if _, err := parseTOMLValue(value); err != nil {
				return nil, err
			}
		}
		return raw, nil
	}
	return nil, errors.New("unsupported or malformed TOML value")
}

func validTOMLBasicEscapes(raw string) bool {
	for index := 1; index < len(raw)-1; index++ {
		if raw[index] != '\\' {
			continue
		}
		index++
		if index >= len(raw)-1 ||
			!strings.ContainsRune(`btnfr"\uU`, rune(raw[index])) {
			return false
		}
	}
	return true
}

func splitTOMLCollection(raw string) []string {
	items := []string{}
	start := 0
	quote := byte(0)
	escaped := false
	depth := 0
	for index := 0; index < len(raw); index++ {
		character := raw[index]
		if quote == '"' && escaped {
			escaped = false
			continue
		}
		if quote == '"' && character == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			}
			continue
		}
		switch character {
		case '"', '\'':
			quote = character
		case '[', '{':
			depth++
		case ']', '}':
			depth--
		case ',':
			if depth == 0 {
				items = append(items, strings.TrimSpace(raw[start:index]))
				start = index + 1
			}
		}
	}
	items = append(items, strings.TrimSpace(raw[start:]))
	return items
}

func applyGlobalSetting(settings *SafeSettings, key string, raw any) error {
	switch key {
	case "concurrent":
		value, err := safeTOMLInt(raw)
		if err != nil {
			return err
		}
		settings.Concurrent = value
	case "check_interval":
		value, err := safeTOMLInt(raw)
		if err != nil {
			return err
		}
		settings.CheckInterval = value
	case "shutdown_timeout":
		value, err := safeTOMLInt(raw)
		if err != nil {
			return err
		}
		settings.ShutdownTimeout = value
	case "log_level":
		value, ok := raw.(string)
		if !ok || !allowed(value, "debug", "info", "warn", "error", "fatal", "panic") {
			return errors.New("invalid log level")
		}
		settings.LogLevel = value
	}
	return nil
}

func applyRunnerSetting(runner *RunnerMetadata, key string, raw any) error {
	switch key {
	case "name":
		value, ok := raw.(string)
		if !ok || value == "" || !safeText.MatchString(value) ||
			strings.Contains(value, "://") {
			return errors.New("invalid runner name")
		}
	case "executor":
		value, ok := raw.(string)
		if !ok || !allowed(value,
			"shell", "docker", "docker-windows", "kubernetes", "custom",
			"parallels", "virtualbox", "ssh", "docker+machine",
			"instance", "docker-autoscaler") {
			return errors.New("unsupported runner executor")
		}
		runner.Executor = value
	case "limit":
		value, err := safeTOMLInt(raw)
		if err != nil {
			return err
		}
		runner.Limit = value
	case "request_concurrency":
		value, err := safeTOMLInt(raw)
		if err != nil {
			return err
		}
		runner.RequestConcurrency = value
	case "shell":
		value, ok := raw.(string)
		if !ok || !allowed(value, "bash", "sh", "powershell", "pwsh") {
			return errors.New("unsupported runner shell")
		}
		runner.Shell = value
	}
	return nil
}

func safeTOMLInt(raw any) (int, error) {
	value, ok := raw.(int64)
	if !ok || value < 0 || value > 1_000_000 {
		return 0, errors.New("integer setting is out of range")
	}
	return int(value), nil
}

func allowed(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if value == candidate {
			return true
		}
	}
	return false
}

func lineError(index int, err error) error {
	return fmt.Errorf("line %d: %w", index+1, err)
}
