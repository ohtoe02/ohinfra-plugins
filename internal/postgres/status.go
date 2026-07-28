package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	dpkgStatusLimit = 16 << 20
	procFileLimit   = 64 << 10
	maxProcEntries  = 32768
)

type Package struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type Process struct {
	PID int    `json:"pid"`
	UID string `json:"uid,omitempty"`
}

type systemdState struct {
	Load   string
	Active string
	Sub    string
}

var safeSystemdSubState = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

func executeStatus(
	ctx context.Context,
	options Options,
	local probe.Local,
) (protocol.Result, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	clusters, clusterErr := collectClusters(ctx, local)
	if fatal := contextFailure(clusterErr); fatal != nil {
		return protocol.Result{}, fatal
	}
	if clusters == nil {
		clusters = []Cluster{}
	}
	service, serviceErr := collectSystemdState(ctx, local)
	if fatal := contextFailure(serviceErr); fatal != nil {
		return protocol.Result{}, fatal
	}
	packages := collectPackages(local)
	processes := collectProcesses(local)
	configPresent, _ := local.Exists(filepath.FromSlash("etc/postgresql"))

	installed := len(clusters) > 0 || len(packages) > 0 || len(processes) > 0 ||
		configPresent || service.Load == "loaded"
	data := map[string]any{
		"installed": installed, "cluster_count": len(clusters),
		"package_count": len(packages), "process_count": len(processes),
		"config_present": configPresent,
		"clusters":       clusters, "packages": packages, "processes": processes,
	}
	if service.Load != "" {
		data["systemd_load_state"] = service.Load
	}
	if service.Active != "" {
		data["systemd_active_state"] = service.Active
	}
	if service.Sub != "" {
		data["systemd_sub_state"] = service.Sub
	}

	check := protocol.Check{
		ID: "postgres.status", Status: protocol.StatusInfo,
		Summary: "PostgreSQL is not installed on this host",
	}
	if installed {
		check.Status = protocol.StatusPass
		check.Summary = "Local PostgreSQL installation was detected"
		if service.Active != "" && service.Active != "active" && len(processes) == 0 {
			check.Status = protocol.StatusWarning
			check.Summary = "PostgreSQL is installed but no active local service was detected"
		}
	}
	return buildResult(options, "postgres status", data, []protocol.Check{check}, nil), nil
}

func collectSystemdState(ctx context.Context, local probe.Local) (systemdState, error) {
	output, err := local.Run(ctx, probe.Command{
		Program: "systemctl",
		Arguments: []string{
			"show", "postgresql.service",
			"--property=LoadState,ActiveState,SubState", "--no-pager",
		},
		StdoutLimit: 32 << 10, StderrLimit: 16 << 10,
	})
	if err != nil {
		return systemdState{}, err
	}
	if output.ExitCode != 0 || output.StdoutTruncated || output.StderrTruncated {
		return systemdState{}, errors.New("local PostgreSQL service state is unavailable")
	}
	state := systemdState{}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "LoadState":
			if allowedValue(value, "loaded", "error", "not-found", "bad-setting", "masked",
				"transient", "merged", "stub") {
				state.Load = value
			}
		case "ActiveState":
			if allowedValue(value, "active", "reloading", "inactive", "failed",
				"activating", "deactivating", "maintenance", "refreshing") {
				state.Active = value
			}
		case "SubState":
			if safeSystemdSubState.MatchString(value) {
				state.Sub = value
			}
		}
	}
	return state, nil
}

func allowedValue(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func collectPackages(local probe.Local) []Package {
	encoded, err := local.Read(filepath.FromSlash("var/lib/dpkg/status"), dpkgStatusLimit)
	if err != nil {
		return []Package{}
	}
	packages := []Package{}
	for _, stanza := range strings.Split(strings.ReplaceAll(string(encoded), "\r\n", "\n"), "\n\n") {
		fields := map[string]string{}
		for _, line := range strings.Split(stanza, "\n") {
			key, value, ok := strings.Cut(line, ":")
			if ok {
				fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
			}
		}
		name := fields["Package"]
		if fields["Status"] != "install ok installed" ||
			(name != "postgresql" && name != "postgresql-common" &&
				!strings.HasPrefix(name, "postgresql-")) {
			continue
		}
		packages = append(packages, Package{Name: name, Version: fields["Version"]})
	}
	sort.Slice(packages, func(left, right int) bool {
		return packages[left].Name < packages[right].Name
	})
	return packages
}

func collectProcesses(local probe.Local) []Process {
	procPath := filepath.Join(local.Root, "proc")
	entries, err := os.ReadDir(procPath)
	if err != nil || len(entries) > maxProcEntries {
		return []Process{}
	}
	processes := []Process{}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		pid, parseErr := strconv.Atoi(entry.Name())
		if parseErr != nil || pid <= 0 {
			continue
		}
		prefix := filepath.Join("proc", entry.Name())
		comm, readErr := local.Read(filepath.Join(prefix, "comm"), procFileLimit)
		if readErr != nil {
			continue
		}
		name := strings.TrimSpace(string(comm))
		if name != "postgres" && !strings.HasPrefix(name, "postgres:") {
			continue
		}
		process := Process{PID: pid}
		if status, statusErr := local.Lines(filepath.Join(prefix, "status"), procFileLimit); statusErr == nil {
			for _, line := range status {
				if strings.HasPrefix(line, "Uid:") {
					fields := strings.Fields(line)
					if len(fields) > 1 {
						process.UID = fields[1]
					}
					break
				}
			}
		}
		processes = append(processes, process)
	}
	sort.Slice(processes, func(left, right int) bool {
		return processes[left].PID < processes[right].PID
	})
	return processes
}
