package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	versionOutputLimit int64 = 4 << 10
	commandErrorLimit  int64 = 8 << 10
	configFileLimit    int64 = 256 << 10
)

type adapter struct {
	ID           string
	Name         string
	Version      probe.Command
	VersionMatch *regexp.Regexp
	ConfigPath   string
	Repository   repositoryParser
	Service      string
	Timer        string
	HistoryPath  string
}

type repositoryParser func(string) (string, bool)

var adapters = []adapter{
	{
		ID: "borg", Name: "BorgBackup",
		Version: probe.Command{
			Program: "borg", Arguments: []string{"--version"},
			StdoutLimit: versionOutputLimit, StderrLimit: commandErrorLimit,
		},
		VersionMatch: regexp.MustCompile(`\Aborg ([0-9]+(?:\.[0-9]+){1,3})\n?\z`),
		ConfigPath:   filepath.FromSlash("etc/borgmatic/config.yaml"),
		Repository:   parseBorgRepository,
		Service:      "borg-backup.service",
		Timer:        "borg-backup.timer",
		HistoryPath:  filepath.FromSlash("var/log/borg/backup.log"),
	},
	{
		ID: "restic", Name: "Restic",
		Version: probe.Command{
			Program: "restic", Arguments: []string{"version"},
			StdoutLimit: versionOutputLimit, StderrLimit: commandErrorLimit,
		},
		VersionMatch: regexp.MustCompile(
			`\Arestic ([0-9]+(?:\.[0-9]+){1,3})(?: compiled with [A-Za-z0-9.+_-]+ on [A-Za-z0-9/_-]+)?\n?\z`,
		),
		ConfigPath:  filepath.FromSlash("etc/restic/restic.conf"),
		Repository:  parseResticRepository,
		Service:     "restic-backup.service",
		Timer:       "restic-backup.timer",
		HistoryPath: filepath.FromSlash("var/log/restic/backup.log"),
	},
	{
		ID: "rsnapshot", Name: "rsnapshot",
		Version: probe.Command{
			Program: "rsnapshot", Arguments: []string{"-V"},
			StdoutLimit: versionOutputLimit, StderrLimit: commandErrorLimit,
		},
		VersionMatch: regexp.MustCompile(`\Arsnapshot ([0-9]+(?:\.[0-9]+){1,3})\n?\z`),
		ConfigPath:   filepath.FromSlash("etc/rsnapshot.conf"),
		Repository:   parseRSnapshotRepository,
		Service:      "rsnapshot.service",
		Timer:        "rsnapshot.timer",
		HistoryPath:  filepath.FromSlash("var/log/rsnapshot.log"),
	},
}

type ProductInventory struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Installed      bool   `json:"installed"`
	Version        string `json:"version,omitempty"`
	ConfigPresent  bool   `json:"config_present"`
	RepositoryType string `json:"repository_type,omitempty"`
}

func executeInventory(
	ctx context.Context,
	options Options,
	local probe.Local,
) (protocol.Result, error) {
	products, checks, failures, err := collectInventory(ctx, local)
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return protocol.Result{}, fatal
		}
		return protocol.Result{}, dependencyFailure("local backup inventory is unavailable")
	}
	return buildResult(options, "backup inventory",
		map[string]any{"products": products}, checks, failures), nil
}

func collectInventory(
	ctx context.Context,
	local probe.Local,
) ([]ProductInventory, []protocol.Check, []protocol.StructuredError, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	products := make([]ProductInventory, 0, len(adapters))
	checks := make([]protocol.Check, 0, len(adapters))
	failures := []protocol.StructuredError{}
	for _, current := range adapters {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		item := ProductInventory{ID: current.ID, Name: current.Name}
		version, found, issue, err := inspectVersion(ctx, local, current)
		if err != nil {
			return nil, nil, nil, err
		}
		item.Version = version
		item.Installed = found
		if issue != nil {
			failures = append(failures, backupFailure(
				protocol.ErrorDependency, "version_probe",
				"A local backup tool version could not be inspected", current.ID,
			))
		}

		repositoryType, present, configIssue := inspectConfig(local, current)
		item.ConfigPresent = present
		item.RepositoryType = repositoryType
		item.Installed = item.Installed || present
		if configIssue != nil {
			failures = append(failures, backupFailure(
				protocol.ErrorConfiguration, "config_metadata",
				"A local backup configuration is unsafe or malformed", current.ID,
			))
		}
		check := protocol.Check{
			ID: "backup." + current.ID + ".present", Status: protocol.StatusPass,
			Summary: current.Name + " is installed locally",
		}
		if !item.Installed {
			check.ID = "backup." + current.ID + ".missing"
			check.Status = protocol.StatusInfo
			check.Summary = current.Name + " is not installed"
		}
		products = append(products, item)
		checks = append(checks, check)
	}
	return products, checks, failures, nil
}

func inspectVersion(
	ctx context.Context,
	local probe.Local,
	current adapter,
) (string, bool, error, error) {
	output, err := local.Run(ctx, current.Version)
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return "", false, nil, fatal
		}
		if errors.Is(err, execx.ErrNotFound) {
			return "", false, nil, nil
		}
		return "", false, err, nil
	}
	if output.ExitCode != 0 || output.StdoutTruncated || output.StderrTruncated ||
		len(output.Stderr) != 0 || !utf8.Valid(output.Stdout) {
		return "", true, errors.New("untrusted version output"), nil
	}
	matches := current.VersionMatch.FindSubmatch(output.Stdout)
	if len(matches) != 2 {
		return "", true, errors.New("malformed version output"), nil
	}
	return string(matches[1]), true, nil, nil
}

func inspectConfig(local probe.Local, current adapter) (string, bool, error) {
	data, err := local.Read(current.ConfigPath, configFileLimit)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !utf8.Valid(data) {
		return "", true, errors.New("config is not UTF-8")
	}
	repositoryType, ok := current.Repository(string(data))
	if !ok {
		return "", true, errors.New("repository metadata is malformed")
	}
	return repositoryType, true, nil
}

func parseResticRepository(content string) (string, bool) {
	value, ok := exactAssignment(content, "RESTIC_REPOSITORY")
	if !ok {
		return "", false
	}
	return classifyRepository(value), true
}

func parseBorgRepository(content string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, prefix := range []string{"- path:", "path:"} {
			if strings.HasPrefix(trimmed, prefix) {
				value := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, prefix)), `"'`)
				if value == "" {
					return "", false
				}
				return classifyRepository(value), true
			}
		}
	}
	return "", false
}

func parseRSnapshotRepository(content string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "snapshot_root" {
			return classifyRepository(fields[1]), true
		}
	}
	return "", false
}

func exactAssignment(content, key string) (string, bool) {
	var value string
	found := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, candidate, ok := strings.Cut(trimmed, "=")
		if ok && strings.TrimSpace(name) == key {
			if found {
				return "", false
			}
			value = strings.Trim(strings.TrimSpace(candidate), `"'`)
			found = true
		}
	}
	return value, found && value != ""
}

func classifyRepository(value string) string {
	lower := strings.ToLower(strings.TrimSpace(value))
	switch {
	case strings.HasPrefix(value, "/"), filepath.IsAbs(value), strings.HasPrefix(lower, "file:"):
		return "local"
	case strings.HasPrefix(lower, "ssh:"), strings.Contains(lower, "@"):
		return "ssh"
	case strings.HasPrefix(lower, "s3:"), strings.HasPrefix(lower, "b2:"),
		strings.HasPrefix(lower, "azure:"), strings.HasPrefix(lower, "gs:"),
		strings.HasPrefix(lower, "rclone:"):
		return "object"
	case strings.Contains(lower, "://"):
		return "remote"
	default:
		return "unknown"
	}
}

func backupFailure(
	kind protocol.ErrorKind,
	code string,
	message string,
	product string,
) protocol.StructuredError {
	return protocol.StructuredError{
		Kind: kind, Code: code, Message: message, Retryable: false,
		Details: map[string]any{"product": product},
	}
}
