package incident

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	fileInputLimit     int64 = 256 << 10
	commandOutputLimit int64 = 256 << 10
	journalOutputLimit int64 = 512 << 10
	commandErrorLimit  int64 = 16 << 10
)

var (
	serviceUnitName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@:-]{0,254}\.service$`)
	osVersion       = regexp.MustCompile(`^[0-9]+(?:\.[0-9]+){0,2}$`)
)

type OSInfo struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Name    string `json:"name"`
}

type MemoryInfo struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	SwapTotalBytes uint64 `json:"swap_total_bytes"`
	SwapFreeBytes  uint64 `json:"swap_free_bytes"`
}

type DiskUsage struct {
	TotalBytes  uint64 `json:"total_bytes"`
	UsedBytes   uint64 `json:"used_bytes"`
	FreeBytes   uint64 `json:"free_bytes"`
	UsedPercent int    `json:"used_percent"`
	MountPoint  string `json:"mount_point"`
}

type ServiceEvidence struct {
	Unit   string `json:"unit"`
	Load   string `json:"load"`
	Active string `json:"active"`
	Sub    string `json:"sub"`
}

type Snapshot struct {
	OS             OSInfo            `json:"os"`
	Load           []float64         `json:"load"`
	Memory         MemoryInfo        `json:"memory"`
	Disk           DiskUsage         `json:"disk"`
	ListenerCount  int               `json:"listener_count"`
	FailedServices []ServiceEvidence `json:"failed_services"`
	OOMEvents      int               `json:"oom_events"`
	RecentErrors   int               `json:"recent_errors"`
}

func executeSnapshot(
	ctx context.Context,
	options Options,
) (protocol.Result, error) {
	local := probe.Local{Root: options.Root, Runner: options.Runner}
	snapshot := Snapshot{
		Load: []float64{}, FailedServices: []ServiceEvidence{},
	}
	var checks []protocol.Check
	var failures []protocol.StructuredError

	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	if data, err := local.Read("etc/os-release", fileInputLimit); err == nil {
		snapshot.OS, err = parseOSRelease(string(data))
		if err != nil {
			failures = append(failures, evidenceFailure("os", "Operating system evidence is malformed"))
		}
	} else {
		failures = append(failures, evidenceFailure("os", "Operating system evidence is unavailable"))
	}
	if data, err := local.Read("proc/loadavg", fileInputLimit); err == nil {
		snapshot.Load, err = parseLoad(string(data))
		if err != nil {
			failures = append(failures, evidenceFailure("load", "Load evidence is malformed"))
		}
	} else {
		failures = append(failures, evidenceFailure("load", "Load evidence is unavailable"))
	}
	if data, err := local.Read("proc/meminfo", fileInputLimit); err == nil {
		snapshot.Memory, err = parseMemory(string(data))
		if err != nil {
			failures = append(failures, evidenceFailure("memory", "Memory evidence is malformed"))
		}
	} else {
		failures = append(failures, evidenceFailure("memory", "Memory evidence is unavailable"))
	}

	output, err := runBounded(ctx, local, "df", []string{"-P", "-B1", "--", "/"}, commandOutputLimit)
	if err != nil {
		if contextFailure(err) != nil {
			return protocol.Result{}, err
		}
		failures = append(failures, evidenceFailure("disk", "Disk evidence is unavailable"))
	} else if snapshot.Disk, err = parseDisk(string(output)); err != nil {
		failures = append(failures, evidenceFailure("disk", "Disk evidence is malformed"))
	}

	output, err = runBounded(ctx, local, "ss", []string{"-H", "-lntu"}, commandOutputLimit)
	if err != nil {
		if contextFailure(err) != nil {
			return protocol.Result{}, err
		}
		failures = append(failures, evidenceFailure("listeners", "Listener evidence is unavailable"))
	} else if snapshot.ListenerCount, err = countListeners(string(output)); err != nil {
		failures = append(failures, evidenceFailure("listeners", "Listener evidence is malformed"))
	}

	output, err = runBounded(ctx, local, "systemctl", failedUnitArguments(), commandOutputLimit)
	if err != nil {
		if contextFailure(err) != nil {
			return protocol.Result{}, err
		}
		failures = append(failures, evidenceFailure("services", "Failed service evidence is unavailable"))
	} else if snapshot.FailedServices, err = parseServices(string(output)); err != nil {
		failures = append(failures, evidenceFailure("services", "Failed service evidence is malformed"))
	}

	output, err = runBounded(ctx, local, "journalctl", journalArguments("1h"), journalOutputLimit)
	if err != nil {
		if contextFailure(err) != nil {
			return protocol.Result{}, err
		}
		failures = append(failures, evidenceFailure("journal", "Journal evidence is unavailable"))
	} else {
		snapshot.RecentErrors, snapshot.OOMEvents = countJournalEvidence(string(output))
	}

	checks = append(checks, protocol.Check{
		ID: "incident.snapshot", Status: protocol.StatusPass,
		Summary: "Bounded local incident evidence collected",
		Details: map[string]any{"available_probes": 7 - len(failures), "total_probes": 7},
	})
	return buildResult(options, "incident snapshot",
		map[string]any{"snapshot": snapshot}, checks, failures), nil
}

func runBounded(
	ctx context.Context,
	local probe.Local,
	program string,
	arguments []string,
	stdoutLimit int64,
) ([]byte, error) {
	output, err := local.Run(ctx, probe.Command{
		Program: program, Arguments: arguments,
		StdoutLimit: stdoutLimit, StderrLimit: commandErrorLimit,
	})
	if err != nil {
		return nil, err
	}
	if output.ExitCode != 0 || output.StdoutTruncated || output.StderrTruncated {
		return nil, errors.New("local probe failed")
	}
	return output.Stdout, nil
}

func parseOSRelease(value string) (OSInfo, error) {
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		key, raw, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return OSInfo{}, errors.New("invalid os-release")
		}
		raw = strings.TrimSpace(raw)
		if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
			raw = raw[1 : len(raw)-1]
		}
		switch key {
		case "ID", "VERSION_ID", "PRETTY_NAME":
			if strings.ContainsAny(raw, "\r\n\x00") {
				return OSInfo{}, errors.New("invalid os-release value")
			}
			fields[key] = raw
		}
	}
	if fields["ID"] == "" || fields["VERSION_ID"] == "" {
		return OSInfo{}, errors.New("incomplete os-release")
	}
	name := ""
	switch fields["ID"] {
	case "debian":
		name = "Debian"
	case "ubuntu":
		name = "Ubuntu"
	default:
		return OSInfo{}, errors.New("unsupported operating system evidence")
	}
	if !osVersion.MatchString(fields["VERSION_ID"]) {
		return OSInfo{}, errors.New("invalid operating system version")
	}
	return OSInfo{ID: fields["ID"], Version: fields["VERSION_ID"], Name: name}, nil
}

func parseLoad(value string) ([]float64, error) {
	fields := strings.Fields(value)
	if len(fields) < 3 {
		return nil, errors.New("invalid loadavg")
	}
	values := make([]float64, 3)
	for index := range values {
		number, err := strconv.ParseFloat(fields[index], 64)
		if err != nil || number < 0 || math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, errors.New("invalid loadavg")
		}
		values[index] = number
	}
	return values, nil
}

func parseMemory(value string) (MemoryInfo, error) {
	values := map[string]uint64{}
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[2] != "kB" {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		switch key {
		case "MemTotal", "MemAvailable", "SwapTotal", "SwapFree":
			number, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil || number > math.MaxUint64/1024 {
				return MemoryInfo{}, errors.New("invalid meminfo")
			}
			values[key] = number * 1024
		}
	}
	if values["MemTotal"] == 0 {
		return MemoryInfo{}, errors.New("incomplete meminfo")
	}
	return MemoryInfo{
		TotalBytes: values["MemTotal"], AvailableBytes: values["MemAvailable"],
		SwapTotalBytes: values["SwapTotal"], SwapFreeBytes: values["SwapFree"],
	}, nil
}

func parseDisk(value string) (DiskUsage, error) {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) != 2 {
		return DiskUsage{}, errors.New("invalid df output")
	}
	fields := strings.Fields(lines[1])
	if len(fields) != 6 || fields[5] != "/" || !strings.HasSuffix(fields[4], "%") {
		return DiskUsage{}, errors.New("invalid df output")
	}
	total, err1 := strconv.ParseUint(fields[1], 10, 64)
	used, err2 := strconv.ParseUint(fields[2], 10, 64)
	free, err3 := strconv.ParseUint(fields[3], 10, 64)
	percent, err4 := strconv.Atoi(strings.TrimSuffix(fields[4], "%"))
	if err1 != nil || err2 != nil || err3 != nil || err4 != nil ||
		percent < 0 || percent > 100 {
		return DiskUsage{}, errors.New("invalid df output")
	}
	return DiskUsage{
		TotalBytes: total, UsedBytes: used, FreeBytes: free,
		UsedPercent: percent, MountPoint: "/",
	}, nil
}

func countListeners(value string) (int, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	count := 0
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 6 ||
			(fields[0] != "tcp" && fields[0] != "udp") ||
			(fields[1] != "LISTEN" && fields[1] != "UNCONN") {
			return 0, errors.New("invalid listener output")
		}
		count++
	}
	return count, nil
}

func failedUnitArguments() []string {
	return []string{
		"list-units", "--type=service", "--state=failed", "--all",
		"--no-legend", "--plain",
	}
}

func parseServices(value string) ([]ServiceEvidence, error) {
	if strings.TrimSpace(value) == "" {
		return []ServiceEvidence{}, nil
	}
	services := make([]ServiceEvidence, 0)
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || !serviceUnitName.MatchString(fields[0]) {
			return nil, errors.New("invalid systemctl output")
		}
		for _, state := range fields[1:4] {
			if !safeState(state) {
				return nil, errors.New("invalid systemctl state")
			}
		}
		services = append(services, ServiceEvidence{
			Unit: fields[0], Load: fields[1], Active: fields[2], Sub: fields[3],
		})
	}
	return services, nil
}

func safeState(value string) bool {
	if value == "" || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && character != '-' {
			return false
		}
	}
	return true
}

func countJournalEvidence(value string) (total, oom int) {
	for _, line := range strings.Split(strings.TrimSpace(value), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		total++
		normalized := strings.ToLower(line)
		if strings.Contains(normalized, "out of memory") ||
			strings.Contains(normalized, "oom-kill") {
			oom++
		}
	}
	return total, oom
}

func evidenceFailure(code, message string) protocol.StructuredError {
	return protocol.StructuredError{
		Kind: protocol.ErrorDependency, Code: code, Message: message, Retryable: false,
	}
}

func contextFailure(err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func journalArguments(since string) []string {
	return []string{
		"--no-pager", "--output=short-iso", "--priority=0..3",
		fmt.Sprintf("--since=-%s", since),
	}
}
