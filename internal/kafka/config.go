package kafka

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	configFileLimit  int64 = 512 << 10
	maxPropertyLines       = 8192
)

var (
	configCandidates = []struct {
		relative string
		display  string
	}{
		{"etc/kafka/server.properties", "/etc/kafka/server.properties"},
		{"etc/kafka/kraft/server.properties", "/etc/kafka/kraft/server.properties"},
		{"opt/kafka/config/server.properties", "/opt/kafka/config/server.properties"},
		{"opt/kafka/config/kraft/server.properties", "/opt/kafka/config/kraft/server.properties"},
	}
	propertyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	integerSettingKeys = map[string]int64{
		"node.id":                    2_147_483_647,
		"broker.id":                  2_147_483_647,
		"num.partitions":             1_000_000,
		"default.replication.factor": 100_000,
		"min.insync.replicas":        100_000,
	}
)

type ConfigFile struct {
	Path     string            `json:"path"`
	Settings map[string]string `json:"settings"`
}

func executeConfig(
	ctx context.Context,
	options Options,
	local probe.Local,
) (protocol.Result, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	files := []ConfigFile{}
	for _, candidate := range configCandidates {
		if err := ctx.Err(); err != nil {
			return protocol.Result{}, err
		}
		exists, err := local.Exists(candidate.relative)
		if err != nil {
			return protocol.Result{}, configFailure()
		}
		if !exists {
			continue
		}
		data, err := local.Read(candidate.relative, configFileLimit)
		if err != nil {
			return protocol.Result{}, configFailure()
		}
		settings, err := parseProperties(data)
		if err != nil {
			return protocol.Result{}, configFailure()
		}
		files = append(files, ConfigFile{Path: candidate.display, Settings: settings})
	}
	if len(files) == 0 {
		return protocol.Result{}, configFailure()
	}
	return buildResult(options, "kafka config",
		map[string]any{"files": files, "file_count": len(files)},
		[]protocol.Check{{
			ID: "kafka.config", Status: protocol.StatusPass,
			Summary: "Local Kafka configuration metadata is available",
		}}, nil), nil
}

func configFailure() error {
	return protocol.ExitError{
		Code: protocol.ExitDependency,
		Err:  errors.New("local Kafka configuration is unavailable or unsafe"),
	}
}

func parseProperties(data []byte) (map[string]string, error) {
	all, err := parseRawProperties(data)
	if err != nil {
		return nil, err
	}
	safe := map[string]string{}
	for key, value := range all {
		normalized, include := safeSetting(key, value)
		if include {
			safe[key] = normalized
		}
	}
	return safe, nil
}

func parseRawProperties(data []byte) (map[string]string, error) {
	if !utf8.Valid(data) {
		return nil, errors.New("Kafka properties are not UTF-8")
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) > maxPropertyLines {
		return nil, errors.New("Kafka properties exceed line limit")
	}
	all := map[string]string{}
	for _, raw := range lines {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		if strings.HasSuffix(line, `\`) {
			return nil, errors.New("Kafka property continuations are unsupported")
		}
		delimiter := strings.IndexAny(line, "=:")
		if delimiter <= 0 {
			return nil, errors.New("Kafka property is malformed")
		}
		key := strings.TrimSpace(line[:delimiter])
		value := strings.TrimSpace(line[delimiter+1:])
		if !propertyKeyPattern.MatchString(key) {
			return nil, errors.New("Kafka property key is invalid")
		}
		if _, duplicate := all[key]; duplicate {
			return nil, errors.New("Kafka property is duplicated")
		}
		all[key] = value
	}
	return all, nil
}

func safeSetting(key, value string) (string, bool) {
	if maximum, ok := integerSettingKeys[key]; ok {
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil || number < 0 || number > maximum {
			return "", false
		}
		return strconv.FormatInt(number, 10), true
	}
	switch key {
	case "auto.create.topics.enable":
		if value == "true" || value == "false" {
			return value, true
		}
	case "process.roles":
		roles := strings.Split(value, ",")
		seen := map[string]bool{}
		for _, role := range roles {
			role = strings.TrimSpace(role)
			if role != "broker" && role != "controller" {
				return "", false
			}
			seen[role] = true
		}
		if len(seen) == 0 {
			return "", false
		}
		normalized := make([]string, 0, len(seen))
		for role := range seen {
			normalized = append(normalized, role)
		}
		sort.Strings(normalized)
		return strings.Join(normalized, ","), true
	}
	return "", false
}
