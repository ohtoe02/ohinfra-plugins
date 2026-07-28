package kafka

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	storageOutputLimit int64 = 256 << 10
	maxLogDirectories        = 16
	maxLogDirEntries         = 4096
)

type LogDirectory struct {
	Path       string `json:"path"`
	EntryCount int    `json:"entry_count"`
}

type storageSummary struct {
	MetadataRecords int
}

type directoryReader interface {
	ReadDir(int) ([]os.DirEntry, error)
	Stat() (os.FileInfo, error)
	Close() error
}

func executeStorage(
	ctx context.Context,
	options Options,
	local probe.Local,
) (protocol.Result, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	config, properties, err := selectStorageConfig(ctx, local)
	if err != nil {
		return protocol.Result{}, storageFailure()
	}
	directories, err := inspectLogDirectories(ctx, options.Root, properties)
	if fatal := contextFailure(err); fatal != nil {
		return protocol.Result{}, fatal
	}
	if err != nil {
		return protocol.Result{}, storageFailure()
	}

	summary, toolAvailable, toolErr := runStorageInfo(ctx, local, config.display)
	if fatal := contextFailure(toolErr); fatal != nil {
		return protocol.Result{}, fatal
	}
	data := map[string]any{
		"config_path":            config.display,
		"log_directories":        directories,
		"log_directory_count":    len(directories),
		"storage_tool_available": toolAvailable,
		"metadata_records":       summary.MetadataRecords,
	}
	checks := []protocol.Check{{
		ID: "kafka.storage.directories", Status: protocol.StatusPass,
		Summary: "Local Kafka log directories are available",
	}}
	failures := []protocol.StructuredError{}
	if toolAvailable {
		checks = append(checks, protocol.Check{
			ID: "kafka.storage.info", Status: protocol.StatusPass,
			Summary: "Local Kafka storage metadata is available",
		})
	} else {
		checks = append(checks, protocol.Check{
			ID: "kafka.storage.info", Status: protocol.StatusSkipped,
			Summary: "Local Kafka storage tool metadata is unavailable",
		})
		failures = append(failures, protocol.StructuredError{
			Kind: protocol.ErrorDependency, Code: "kafka_storage_info_unavailable",
			Message:    "Optional local Kafka storage metadata is unavailable",
			Dependency: "kafka-storage",
		})
	}
	return buildResult(options, "kafka storage", data, checks, failures), nil
}

func storageFailure() error {
	return protocol.ExitError{
		Code: protocol.ExitDependency,
		Err:  errors.New("local Kafka storage metadata is unavailable or unsafe"),
	}
}

func selectStorageConfig(
	ctx context.Context,
	local probe.Local,
) (struct{ relative, display string }, map[string]string, error) {
	for _, candidate := range configCandidates {
		if err := ctx.Err(); err != nil {
			return struct{ relative, display string }{}, nil, err
		}
		exists, err := local.Exists(candidate.relative)
		if err != nil {
			return struct{ relative, display string }{}, nil, err
		}
		if !exists {
			continue
		}
		data, err := local.Read(candidate.relative, configFileLimit)
		if err != nil {
			return struct{ relative, display string }{}, nil, err
		}
		properties, err := parseRawProperties(data)
		return candidate, properties, err
	}
	return struct{ relative, display string }{}, nil, os.ErrNotExist
}

func inspectLogDirectories(
	ctx context.Context,
	root string,
	properties map[string]string,
) ([]LogDirectory, error) {
	if properties["log.dirs"] != "" && properties["log.dir"] != "" {
		return nil, errors.New("Kafka log directory properties conflict")
	}
	value := properties["log.dirs"]
	if value == "" {
		value = properties["log.dir"]
	}
	if value == "" {
		return nil, errors.New("Kafka log directories are not configured")
	}
	parts := strings.Split(value, ",")
	if len(parts) == 0 || len(parts) > maxLogDirectories {
		return nil, errors.New("Kafka log directory count is invalid")
	}
	seen := map[string]bool{}
	directories := make([]LogDirectory, 0, len(parts))
	for _, raw := range parts {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		unixPath := strings.TrimSpace(raw)
		if !validAbsoluteUnixPath(unixPath) || seen[unixPath] {
			return nil, errors.New("Kafka log directory path is invalid")
		}
		seen[unixPath] = true
		count, err := inspectDirectory(ctx, root, unixPath)
		if err != nil {
			return nil, err
		}
		directories = append(directories, LogDirectory{
			Path: unixPath, EntryCount: count,
		})
	}
	sort.Slice(directories, func(left, right int) bool {
		return directories[left].Path < directories[right].Path
	})
	return directories, nil
}

func validAbsoluteUnixPath(value string) bool {
	if value == "" || len(value) > 4096 || !strings.HasPrefix(value, "/") ||
		value == "/" || path.Clean(value) != value || sensitiveValue(value) {
		return false
	}
	for _, component := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func sensitiveValue(value string) bool {
	normalized := strings.ToLower(value)
	for _, marker := range []string{
		"password", "passwd", "token", "secret", "jaas", "sasl",
		"keystore", "truststore",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func inspectDirectory(ctx context.Context, root, unixPath string) (int, error) {
	return inspectDirectoryWithOpen(ctx, root, unixPath, func(target string) (directoryReader, error) {
		return os.Open(target) // #nosec G304 -- target is confined below the validated root.
	})
}

func inspectDirectoryWithOpen(
	ctx context.Context,
	root string,
	unixPath string,
	openDirectory func(string) (directoryReader, error),
) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return 0, errors.New("Kafka probe root is unsafe")
	}
	current := root
	var expected os.FileInfo
	for _, component := range strings.Split(strings.TrimPrefix(unixPath, "/"), "/") {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		current = filepath.Join(current, filepath.FromSlash(component))
		expected, err = os.Lstat(current)
		if err != nil {
			return 0, err
		}
		if expected.Mode()&os.ModeSymlink != 0 {
			return 0, errors.New("Kafka log directory contains a symlink")
		}
	}
	if expected == nil || !expected.IsDir() {
		return 0, errors.New("Kafka log directory is unavailable")
	}
	directory, err := openDirectory(current)
	if err != nil {
		return 0, err
	}
	defer func() { _ = directory.Close() }()
	opened, err := directory.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(expected, opened) {
		return 0, errors.New("Kafka log directory changed during inspection")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	entries, err := directory.ReadDir(maxLogDirEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(entries) > maxLogDirEntries {
		return 0, errors.New("Kafka log directory exceeds entry limit")
	}
	return len(entries), nil
}

func runStorageInfo(
	ctx context.Context,
	local probe.Local,
	configPath string,
) (storageSummary, bool, error) {
	var lastErr error
	for _, program := range []string{"kafka-storage.sh", "kafka-storage"} {
		output, err := local.Run(ctx, probe.Command{
			Program:     program,
			Arguments:   []string{"info", "--config", configPath},
			StdoutLimit: storageOutputLimit, StderrLimit: 32 << 10,
		})
		if err != nil {
			if fatal := contextFailure(err); fatal != nil {
				return storageSummary{}, false, fatal
			}
			if errors.Is(err, execx.ErrNotFound) {
				lastErr = err
				continue
			}
			return storageSummary{}, false, err
		}
		if output.ExitCode != 0 || output.StdoutTruncated || output.StderrTruncated {
			return storageSummary{}, false, errors.New("local Kafka storage info probe failed")
		}
		summary, err := parseStorageInfo(output.Stdout)
		if err != nil {
			return storageSummary{}, false, err
		}
		return summary, true, nil
	}
	return storageSummary{}, false, lastErr
}

func parseStorageInfo(data []byte) (storageSummary, error) {
	if !utf8.Valid(data) {
		return storageSummary{}, errors.New("Kafka storage info output is not UTF-8")
	}
	summary := storageSummary{}
	markers := 0
	for _, raw := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		switch {
		case line == "":
		case line == "Found log directory:":
			markers++
		case strings.HasPrefix(line, "Found metadata:"):
			markers++
			summary.MetadataRecords++
		case strings.HasPrefix(line, "  "), strings.HasPrefix(line, "\t"):
		default:
			return storageSummary{}, errors.New("Kafka storage info output is malformed")
		}
	}
	if markers == 0 {
		return storageSummary{}, errors.New("Kafka storage info output omitted markers")
	}
	return summary, nil
}
