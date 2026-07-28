package kafka

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	dpkgStatusLimit  int64 = 4 << 20
	processNameLimit int64 = 256
	processArgsLimit int64 = 64 << 10
	maxProcesses           = 4096
)

var unitStatePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

type Package struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type guardedRunner struct {
	runner execx.Runner
}

func (runner guardedRunner) Run(ctx context.Context, spec execx.Spec) (execx.Output, error) {
	if err := validateLocalCommand(spec); err != nil {
		return execx.Output{}, err
	}
	delegate := runner.runner
	if delegate == nil {
		delegate = execx.OSRunner{Resolver: execx.SystemResolver()}
	}
	return delegate.Run(ctx, spec)
}

func validateLocalCommand(spec execx.Spec) error {
	if len(spec.Environment) != 0 || len(spec.Stdin) != 0 {
		return errors.New("kafka local runner rejected environment or stdin overrides")
	}
	if spec.Program == "systemctl" && equalStrings(spec.Arguments, []string{
		"show", "--no-pager", "--property=LoadState,ActiveState,SubState",
		"--", "kafka.service",
	}) {
		return nil
	}
	if (spec.Program == "kafka-storage.sh" || spec.Program == "kafka-storage") &&
		len(spec.Arguments) == 3 &&
		spec.Arguments[0] == "info" &&
		spec.Arguments[1] == "--config" &&
		isFixedConfigPath(spec.Arguments[2]) {
		return nil
	}
	return fmt.Errorf("kafka local runner rejected command %q", spec.Program)
}

func isFixedConfigPath(value string) bool {
	for _, candidate := range configCandidates {
		if candidate.display == value {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func executeStatus(
	ctx context.Context,
	options Options,
	local probe.Local,
) (protocol.Result, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	packages, err := collectPackages(local)
	if err != nil {
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitDependency,
			Err:  errors.New("local Kafka package metadata is unavailable"),
		}
	}
	processIDs, err := collectProcesses(ctx, local, options.Root)
	if fatal := contextFailure(err); fatal != nil {
		return protocol.Result{}, fatal
	}
	if err != nil {
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitDependency,
			Err:  errors.New("local Kafka process metadata is unavailable"),
		}
	}

	unit, unitErr := collectUnit(ctx, local)
	if fatal := contextFailure(unitErr); fatal != nil {
		return protocol.Result{}, fatal
	}
	installed := len(packages) > 0 || len(processIDs) > 0 ||
		(unitErr == nil && unit["LoadState"] == "loaded")
	data := map[string]any{
		"installed":     installed,
		"packages":      packages,
		"package_count": len(packages),
		"process_ids":   processIDs,
		"process_count": len(processIDs),
	}
	checks := []protocol.Check{}
	failures := []protocol.StructuredError{}
	if installed {
		checks = append(checks, protocol.Check{
			ID: "kafka.installation", Status: protocol.StatusPass,
			Summary: "Local Kafka installation metadata is available",
		})
	} else {
		checks = append(checks, protocol.Check{
			ID: "kafka.installation", Status: protocol.StatusInfo,
			Summary: "No local Kafka installation or processes were found",
		})
	}
	if unitErr == nil {
		data["systemd_load_state"] = unit["LoadState"]
		data["systemd_active_state"] = unit["ActiveState"]
		data["systemd_sub_state"] = unit["SubState"]
		checks = append(checks, protocol.Check{
			ID: "kafka.systemd", Status: protocol.StatusPass,
			Summary: "Local Kafka unit state is available",
		})
	} else {
		checks = append(checks, protocol.Check{
			ID: "kafka.systemd", Status: protocol.StatusSkipped,
			Summary: "Local Kafka unit state is unavailable",
		})
		failures = append(failures, protocol.StructuredError{
			Kind: protocol.ErrorDependency, Code: "kafka_unit_unavailable",
			Message:    "Optional local Kafka unit metadata is unavailable",
			Dependency: "systemctl",
		})
	}
	return buildResult(options, "kafka status", data, checks, failures), nil
}

func collectPackages(local probe.Local) ([]Package, error) {
	data, err := local.Read("var/lib/dpkg/status", dpkgStatusLimit)
	if errors.Is(err, os.ErrNotExist) {
		return []Package{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(data) {
		return nil, errors.New("Kafka package metadata is not UTF-8")
	}
	packages := []Package{}
	for _, paragraph := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n\n") {
		values := map[string]string{}
		for _, line := range strings.Split(paragraph, "\n") {
			key, value, ok := strings.Cut(line, ": ")
			if ok {
				values[key] = strings.TrimSpace(value)
			}
		}
		if values["Status"] != "install ok installed" || !isKafkaPackage(values["Package"]) ||
			!safePackageVersion(values["Version"]) {
			continue
		}
		packages = append(packages, Package{Name: values["Package"], Version: values["Version"]})
	}
	sort.Slice(packages, func(left, right int) bool {
		return packages[left].Name < packages[right].Name
	})
	return packages, nil
}

func isKafkaPackage(name string) bool {
	switch name {
	case "kafka", "confluent-kafka", "confluent-server":
		return true
	default:
		return false
	}
}

func safePackageVersion(version string) bool {
	if version == "" || len(version) > 128 || sensitiveValue(version) {
		return false
	}
	for _, character := range version {
		if !strings.ContainsRune("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz.+:~_-", character) {
			return false
		}
	}
	return true
}

func collectProcesses(
	ctx context.Context,
	local probe.Local,
	root string,
) ([]int, error) {
	procPath := filepath.Join(root, "proc")
	info, err := os.Lstat(procPath)
	if errors.Is(err, os.ErrNotExist) {
		return []int{}, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("proc root is unsafe")
	}
	entries, err := os.ReadDir(procPath)
	if err != nil {
		return nil, err
	}
	if len(entries) > maxProcesses {
		return nil, errors.New("process inventory exceeds limit")
	}
	processIDs := []int{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 || !entry.IsDir() {
			continue
		}
		name, err := local.Read(filepath.ToSlash(filepath.Join(
			"proc", entry.Name(), "comm",
		)), processNameLimit)
		if err != nil || strings.TrimSpace(string(name)) != "java" {
			continue
		}
		arguments, err := local.Read(filepath.ToSlash(filepath.Join(
			"proc", entry.Name(), "cmdline",
		)), processArgsLimit)
		if err != nil {
			continue
		}
		if isKafkaCommand(arguments) {
			processIDs = append(processIDs, pid)
		}
	}
	sort.Ints(processIDs)
	return processIDs, nil
}

func isKafkaCommand(arguments []byte) bool {
	for _, field := range strings.Split(string(arguments), "\x00") {
		if field == "kafka.Kafka" || field == "kafka.server.KafkaServer" {
			return true
		}
	}
	return false
}

func collectUnit(ctx context.Context, local probe.Local) (map[string]string, error) {
	output, err := local.Run(ctx, probe.Command{
		Program: "systemctl",
		Arguments: []string{
			"show", "--no-pager", "--property=LoadState,ActiveState,SubState",
			"--", "kafka.service",
		},
		StdoutLimit: 32 << 10, StderrLimit: 16 << 10,
	})
	if err != nil {
		return nil, err
	}
	if output.ExitCode != 0 || output.StdoutTruncated || output.StderrTruncated {
		return nil, errors.New("local Kafka unit probe failed")
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(output.Stdout)), "\n") {
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\r"), "=")
		if !ok || !unitStatePattern.MatchString(value) {
			return nil, errors.New("local Kafka unit probe returned malformed state")
		}
		switch key {
		case "LoadState", "ActiveState", "SubState":
			if _, duplicate := values[key]; duplicate {
				return nil, errors.New("local Kafka unit probe returned duplicate state")
			}
			values[key] = value
		default:
			return nil, errors.New("local Kafka unit probe returned unknown state")
		}
	}
	for _, key := range []string{"LoadState", "ActiveState", "SubState"} {
		if values[key] == "" {
			return nil, errors.New("local Kafka unit probe omitted state")
		}
	}
	return values, nil
}
