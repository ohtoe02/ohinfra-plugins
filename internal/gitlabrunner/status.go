package gitlabrunner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	procFileLimit  = 64 << 10
	maxProcEntries = 32768
)

var (
	safeVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?$`)
	decimalUID  = regexp.MustCompile(`^[0-9]{1,10}$`)
)

type UnitState struct {
	Load   string `json:"load,omitempty"`
	Active string `json:"active,omitempty"`
	Sub    string `json:"sub,omitempty"`
}

type ProcessInfo struct {
	PID int    `json:"pid"`
	UID string `json:"uid,omitempty"`
}

type StatusInfo struct {
	Version   string        `json:"version,omitempty"`
	Unit      UnitState     `json:"unit"`
	Processes []ProcessInfo `json:"processes"`
}

func executeStatus(ctx context.Context, options Options) (protocol.Result, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	local := probe.Local{Root: options.Root, Runner: options.Runner}
	failures := []protocol.StructuredError{}

	version, versionErr := collectVersion(ctx, local)
	if fatal := contextFailure(versionErr); fatal != nil {
		return protocol.Result{}, fatal
	}
	if versionErr != nil && !errors.Is(versionErr, execx.ErrNotFound) {
		failures = append(failures, unavailable("version_unavailable",
			"GitLab Runner version metadata is unavailable"))
	}

	unit, unitErr := collectUnit(ctx, local)
	if fatal := contextFailure(unitErr); fatal != nil {
		return protocol.Result{}, fatal
	}
	if unitErr != nil && !errors.Is(unitErr, execx.ErrNotFound) {
		failures = append(failures, unavailable("unit_unavailable",
			"GitLab Runner unit metadata is unavailable"))
	}

	processes, processErr := collectProcesses(ctx, local)
	if fatal := contextFailure(processErr); fatal != nil {
		return protocol.Result{}, fatal
	}
	if processErr != nil {
		processes = []ProcessInfo{}
		failures = append(failures, unavailable("processes_unavailable",
			"GitLab Runner process metadata is unavailable"))
	}

	info := StatusInfo{Version: version, Unit: unit, Processes: processes}
	installed := version != "" || unit.Load == "loaded" || len(processes) > 0
	check := protocol.Check{
		ID: "gitlab-runner.status", Status: protocol.StatusInfo,
		Summary: "GitLab Runner is not installed on this host",
	}
	if installed {
		check.Status = protocol.StatusPass
		check.Summary = "Local GitLab Runner installation was detected"
		if unit.Active != "" && unit.Active != "active" && len(processes) == 0 {
			check.Status = protocol.StatusWarning
			check.Summary = "GitLab Runner is installed but is not active"
		}
	}
	return buildResult(options, "gitlab-runner status",
		map[string]any{"status": info}, []protocol.Check{check}, failures), nil
}

func collectVersion(ctx context.Context, local probe.Local) (string, error) {
	output, err := local.Run(ctx, probe.Command{
		Program: "gitlab-runner", Arguments: []string{"--version"},
		StdoutLimit: 64 << 10, StderrLimit: 16 << 10,
	})
	if err != nil {
		return "", err
	}
	if output.ExitCode != 0 || output.StdoutTruncated || output.StderrTruncated {
		return "", errors.New("invalid local version probe")
	}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(key) != "Version" {
			continue
		}
		version := strings.TrimSpace(value)
		if safeVersion.MatchString(version) {
			return version, nil
		}
		return "", errors.New("invalid local version value")
	}
	return "", errors.New("missing local version value")
}

func collectUnit(ctx context.Context, local probe.Local) (UnitState, error) {
	output, err := local.Run(ctx, probe.Command{
		Program: "systemctl",
		Arguments: []string{
			"show", "gitlab-runner.service",
			"--property=LoadState,ActiveState,SubState", "--no-pager",
		},
		StdoutLimit: 32 << 10, StderrLimit: 16 << 10,
	})
	if err != nil {
		return UnitState{}, err
	}
	if output.ExitCode != 0 || output.StdoutTruncated || output.StderrTruncated {
		return UnitState{}, errors.New("invalid local unit probe")
	}
	state := UnitState{}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "LoadState":
			if allowed(value, "loaded", "error", "not-found", "bad-setting",
				"masked", "transient", "merged", "stub") {
				state.Load = value
			}
		case "ActiveState":
			if allowed(value, "active", "reloading", "inactive", "failed",
				"activating", "deactivating", "maintenance", "refreshing") {
				state.Active = value
			}
		case "SubState":
			if allowed(value, "running", "dead", "exited", "failed", "start",
				"stop", "auto-restart", "stop-sigterm", "stop-sigkill") {
				state.Sub = value
			}
		}
	}
	return state, nil
}

func collectProcesses(ctx context.Context, local probe.Local) ([]ProcessInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := local.Exists("proc"); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(local.Root, "proc"))
	if err != nil {
		return nil, err
	}
	if len(entries) > maxProcEntries {
		return nil, errors.New("procfs entry limit exceeded")
	}
	processes := []ProcessInfo{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		prefix := filepath.Join("proc", entry.Name())
		comm, err := local.Read(filepath.Join(prefix, "comm"), procFileLimit)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(comm)) != "gitlab-runner" {
			continue
		}
		process := ProcessInfo{PID: pid}
		if lines, err := local.Lines(filepath.Join(prefix, "status"), procFileLimit); err == nil {
			for _, line := range lines {
				if strings.HasPrefix(line, "Uid:") {
					fields := strings.Fields(line)
					if len(fields) > 1 && decimalUID.MatchString(fields[1]) {
						if _, err := strconv.ParseUint(fields[1], 10, 32); err != nil {
							break
						}
						process.UID = fields[1]
					}
					break
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		processes = append(processes, process)
	}
	sort.Slice(processes, func(left, right int) bool {
		return processes[left].PID < processes[right].PID
	})
	return processes, nil
}

func unavailable(code, message string) protocol.StructuredError {
	return protocol.StructuredError{
		Kind: protocol.ErrorDependency, Code: code,
		Message: message, Retryable: false,
	}
}
