package system

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

type OSInfo struct {
	ID         string `json:"id"`
	Version    string `json:"version"`
	PrettyName string `json:"pretty_name"`
}

type CPUInfo struct {
	Model        string `json:"model"`
	LogicalCores int    `json:"logical_cores"`
}

type MemoryInfo struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	SwapTotalBytes uint64 `json:"swap_total_bytes"`
	SwapFreeBytes  uint64 `json:"swap_free_bytes"`
}

type LoadInfo struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

type Snapshot struct {
	OS               OSInfo     `json:"os"`
	Kernel           string     `json:"kernel"`
	Architecture     string     `json:"architecture"`
	Hostname         string     `json:"hostname"`
	UptimeSeconds    int64      `json:"uptime_seconds"`
	CPU              CPUInfo    `json:"cpu"`
	Memory           MemoryInfo `json:"memory"`
	Load             LoadInfo   `json:"load"`
	Virtualization   string     `json:"virtualization"`
	Timezone         string     `json:"timezone"`
	TimeSynchronized *bool      `json:"time_synchronized"`
	RebootRequired   bool       `json:"reboot_required"`
}

type Collector struct {
	Root     string
	Runner   execx.Runner
	Hostname func() (string, error)
	Now      func() time.Time
}

type FilesystemUsage struct {
	MountPoint  string `json:"mount_point"`
	UsedPercent int    `json:"used_percent"`
	Error       string `json:"error,omitempty"`
}

type HealthThresholds struct {
	DiskWarning    int
	DiskCritical   int
	MemoryWarning  int
	MemoryCritical int
	OOMWindow      time.Duration
}

type HealthInput struct {
	Tool        protocol.Tool
	Filesystems []FilesystemUsage
	Thresholds  HealthThresholds
}

func DefaultHealthThresholds() HealthThresholds {
	return HealthThresholds{
		DiskWarning: 80, DiskCritical: 90,
		MemoryWarning: 85, MemoryCritical: 95, OOMWindow: 24 * time.Hour,
	}
}

func DefaultCollector() Collector {
	return Collector{
		Root:     "/",
		Runner:   execx.OSRunner{Resolver: execx.SystemResolver()},
		Hostname: os.Hostname,
	}
}

func (collector Collector) Collect(ctx context.Context) (Snapshot, []protocol.StructuredError) {
	var snapshot Snapshot
	failures := []protocol.StructuredError{}
	if content, err := collector.read("etc/os-release"); err != nil {
		failures = append(failures, structuredFailure("os_release", err))
	} else {
		snapshot.OS = parseOSRelease(content)
	}
	if content, err := collector.read("proc/sys/kernel/osrelease"); err != nil {
		failures = append(failures, structuredFailure("kernel", err))
	} else {
		snapshot.Kernel = strings.TrimSpace(string(content))
	}
	snapshot.Architecture = runtime.GOARCH
	hostname := collector.Hostname
	if hostname == nil {
		hostname = os.Hostname
	}
	if value, err := hostname(); err != nil {
		failures = append(failures, structuredFailure("hostname", err))
	} else {
		snapshot.Hostname = value
	}
	if content, err := collector.read("proc/uptime"); err != nil {
		failures = append(failures, structuredFailure("uptime", err))
	} else if value, err := parseFirstFloat(content); err != nil {
		failures = append(failures, structuredFailure("uptime", err))
	} else {
		snapshot.UptimeSeconds = int64(value)
	}
	if content, err := collector.read("proc/cpuinfo"); err != nil {
		failures = append(failures, structuredFailure("cpu", err))
	} else {
		snapshot.CPU = parseCPUInfo(content)
	}
	if content, err := collector.read("proc/meminfo"); err != nil {
		failures = append(failures, structuredFailure("memory", err))
	} else {
		snapshot.Memory = parseMemoryInfo(content)
	}
	if content, err := collector.read("proc/loadavg"); err != nil {
		failures = append(failures, structuredFailure("load", err))
	} else if load, err := parseLoad(content); err != nil {
		failures = append(failures, structuredFailure("load", err))
	} else {
		snapshot.Load = load
	}
	snapshot.Virtualization = collector.virtualizationFromFiles()
	if output, err := collector.run(ctx, "systemd-detect-virt"); err == nil {
		detected := strings.TrimSpace(string(output.Stdout))
		if output.ExitCode == 0 && detected != "" {
			snapshot.Virtualization = detected
		} else if output.ExitCode == 1 {
			snapshot.Virtualization = "none"
		}
	}
	if content, err := collector.read("etc/timezone"); err == nil {
		snapshot.Timezone = strings.TrimSpace(string(content))
	}
	if snapshot.Timezone == "" {
		if target, err := filepath.EvalSymlinks(collector.path("etc/localtime")); err == nil {
			const marker = "zoneinfo" + string(filepath.Separator)
			if index := strings.LastIndex(target, marker); index >= 0 {
				snapshot.Timezone = filepath.ToSlash(target[index+len(marker):])
			}
		}
	}
	if snapshot.Timezone == "" {
		now := collector.now()
		if location := now.Location().String(); location != "" && location != "Local" {
			snapshot.Timezone = location
		} else {
			snapshot.Timezone, _ = now.Zone()
		}
	}
	if _, err := os.Stat(collector.path("var/run/reboot-required")); err == nil {
		snapshot.RebootRequired = true
	}
	synchronized, err := collector.timeSynchronized(ctx)
	if err != nil {
		failures = append(failures, dependencyFailure("timedatectl", err))
	} else {
		snapshot.TimeSynchronized = &synchronized
	}
	return snapshot, failures
}

func (collector Collector) virtualizationFromFiles() string {
	for _, marker := range []struct{ path, kind string }{
		{".dockerenv", "docker"}, {"run/.containerenv", "podman"},
	} {
		if _, err := os.Stat(collector.path(marker.path)); err == nil {
			return marker.kind
		}
	}
	if content, err := collector.read("proc/1/cgroup"); err == nil {
		cgroup := strings.ToLower(string(content))
		for _, marker := range []struct{ text, kind string }{
			{"kubepods", "kubernetes"}, {"docker", "docker"}, {"containerd", "containerd"}, {"lxc", "lxc"},
		} {
			if strings.Contains(cgroup, marker.text) {
				return marker.kind
			}
		}
	}
	if content, err := collector.read("sys/class/dmi/id/product_name"); err == nil {
		product := strings.ToLower(strings.TrimSpace(string(content)))
		for _, marker := range []struct{ text, kind string }{
			{"kvm", "kvm"}, {"qemu", "kvm"}, {"vmware", "vmware"},
			{"virtualbox", "virtualbox"}, {"virtual machine", "hyperv"}, {"xen", "xen"},
		} {
			if strings.Contains(product, marker.text) {
				return marker.kind
			}
		}
	}
	return "unknown"
}

func (collector Collector) InfoResult(ctx context.Context, tool protocol.Tool) protocol.Result {
	started := time.Now()
	snapshot, failures := collector.Collect(ctx)
	status := protocol.StatusPass
	if len(failures) > 0 {
		status = protocol.StatusPartial
	}
	return protocol.Normalize(protocol.Result{
		Command: "system info", Status: status,
		Timestamp:  collector.now().Format(time.RFC3339Nano),
		DurationMS: time.Since(started).Milliseconds(), Host: snapshot.Hostname,
		Tool: tool, Data: map[string]any{"system": snapshot}, Errors: failures,
	})
}

func (collector Collector) HealthResult(ctx context.Context, input HealthInput) protocol.Result {
	started := time.Now()
	thresholds := input.Thresholds
	if thresholds == (HealthThresholds{}) {
		thresholds = DefaultHealthThresholds()
	}
	snapshot, failures := collector.Collect(ctx)
	checks := []protocol.Check{
		osCheck(snapshot.OS),
		memoryCheck(snapshot.Memory, thresholds),
		{ID: "load", Status: protocol.StatusInfo,
			Summary: fmt.Sprintf("load average %.2f %.2f %.2f", snapshot.Load.One, snapshot.Load.Five, snapshot.Load.Fifteen)},
	}
	for _, filesystem := range input.Filesystems {
		check, failure := filesystemCheck(filesystem, thresholds)
		checks = append(checks, check)
		if failure != nil {
			failures = append(failures, *failure)
		}
	}
	checks = append(checks, timeCheck(snapshot.TimeSynchronized))
	if snapshot.RebootRequired {
		checks = append(checks, protocol.Check{ID: "reboot-required", Status: protocol.StatusWarning, Summary: "system reboot is required"})
	} else {
		checks = append(checks, protocol.Check{ID: "reboot-required", Status: protocol.StatusPass, Summary: "system reboot is not required"})
	}
	failedCheck, failure := collector.failedUnits(ctx)
	checks = append(checks, failedCheck)
	if failure != nil {
		failures = append(failures, *failure)
	}
	oomCheck, failure := collector.oomEvents(ctx, thresholds.OOMWindow)
	checks = append(checks, oomCheck)
	if failure != nil {
		failures = append(failures, *failure)
	}
	return protocol.Normalize(protocol.Result{
		Command: "system health", Status: aggregateStatus(checks, failures),
		Timestamp:  collector.now().Format(time.RFC3339Nano),
		DurationMS: time.Since(started).Milliseconds(), Host: snapshot.Hostname,
		Tool: input.Tool, Checks: checks,
		Data:   map[string]any{"system": snapshot, "filesystems": input.Filesystems},
		Errors: failures,
	})
}

func (collector Collector) failedUnits(ctx context.Context) (protocol.Check, *protocol.StructuredError) {
	output, err := collector.run(ctx, "systemctl", "--failed", "--no-legend", "--plain")
	if err != nil {
		failure := dependencyFailure("systemctl", err)
		return protocol.Check{ID: "failed-units", Status: protocol.StatusSkipped, Summary: "failed units could not be checked"}, &failure
	}
	if output.ExitCode != 0 {
		failure := structuredFailure("failed_units", fmt.Errorf("systemctl exited with %d: %s", output.ExitCode, strings.TrimSpace(string(output.Stderr))))
		return protocol.Check{ID: "failed-units", Status: protocol.StatusSkipped, Summary: "failed units could not be checked"}, &failure
	}
	lines := nonEmptyLines(output.Stdout)
	if len(lines) > 0 {
		return protocol.Check{ID: "failed-units", Status: protocol.StatusWarning,
			Summary: fmt.Sprintf("%d failed systemd unit(s)", len(lines)), Details: map[string]any{"units": lines}}, nil
	}
	return protocol.Check{ID: "failed-units", Status: protocol.StatusPass, Summary: "no failed systemd units"}, nil
}

func (collector Collector) oomEvents(ctx context.Context, window time.Duration) (protocol.Check, *protocol.StructuredError) {
	output, err := collector.run(ctx, "journalctl", "--since=-"+window.String(), "--no-pager", "-k", "-g", "Out of memory|Killed process")
	if err != nil {
		failure := dependencyFailure("journalctl", err)
		return protocol.Check{ID: "oom", Status: protocol.StatusSkipped, Summary: "OOM events could not be checked"}, &failure
	}
	if output.ExitCode != 0 {
		failure := structuredFailure("oom", fmt.Errorf("journalctl exited with %d: %s", output.ExitCode, strings.TrimSpace(string(output.Stderr))))
		return protocol.Check{ID: "oom", Status: protocol.StatusSkipped, Summary: "OOM events could not be checked"}, &failure
	}
	lines := nonEmptyLines(output.Stdout)
	if len(lines) > 0 {
		return protocol.Check{ID: "oom", Status: protocol.StatusCritical,
			Summary: fmt.Sprintf("%d OOM event line(s) found in %s", len(lines), window),
			Details: map[string]any{"events": lines}}, nil
	}
	return protocol.Check{ID: "oom", Status: protocol.StatusPass, Summary: "no recent OOM events"}, nil
}

func (collector Collector) timeSynchronized(ctx context.Context) (bool, error) {
	output, err := collector.run(ctx, "timedatectl", "show", "--property=NTPSynchronized", "--value")
	if err != nil {
		return false, err
	}
	if output.ExitCode != 0 {
		return false, fmt.Errorf("timedatectl exited with %d: %s", output.ExitCode, strings.TrimSpace(string(output.Stderr)))
	}
	switch strings.ToLower(strings.TrimSpace(string(output.Stdout))) {
	case "yes", "true", "1":
		return true, nil
	case "no", "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("unexpected timedatectl output %q", strings.TrimSpace(string(output.Stdout)))
	}
}

func (collector Collector) run(ctx context.Context, program string, arguments ...string) (execx.Output, error) {
	if collector.Runner == nil {
		return execx.Output{}, execx.ErrNotFound
	}
	return collector.Runner.Run(ctx, execx.Spec{
		Program: program, Arguments: arguments, StdoutLimit: 1 << 20, StderrLimit: 1 << 20,
	})
}

func (collector Collector) read(relative string) ([]byte, error) {
	return os.ReadFile(collector.path(relative)) // #nosec G304 -- root is fixed or a test fixture.
}
func (collector Collector) path(relative string) string {
	root := collector.Root
	if root == "" {
		root = string(filepath.Separator)
	}
	return filepath.Join(root, filepath.FromSlash(relative))
}
func (collector Collector) now() time.Time {
	if collector.Now != nil {
		return collector.Now()
	}
	return time.Now()
}

func parseOSRelease(content []byte) OSInfo {
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if found {
			values[key] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return OSInfo{ID: values["ID"], Version: values["VERSION_ID"], PrettyName: values["PRETTY_NAME"]}
}

func parseCPUInfo(content []byte) CPUInfo {
	var output CPUInfo
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			continue
		}
		switch strings.TrimSpace(key) {
		case "processor":
			output.LogicalCores++
		case "model name", "Hardware":
			if output.Model == "" {
				output.Model = strings.TrimSpace(value)
			}
		}
	}
	return output
}

func parseMemoryInfo(content []byte) MemoryInfo {
	values := map[string]uint64{}
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		key, value, found := strings.Cut(scanner.Text(), ":")
		if !found {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		number, err := strconv.ParseUint(fields[0], 10, 64)
		if err == nil {
			values[strings.TrimSpace(key)] = number * 1024
		}
	}
	return MemoryInfo{
		TotalBytes: values["MemTotal"], AvailableBytes: values["MemAvailable"],
		SwapTotalBytes: values["SwapTotal"], SwapFreeBytes: values["SwapFree"],
	}
}

func parseLoad(content []byte) (LoadInfo, error) {
	fields := strings.Fields(string(content))
	if len(fields) < 3 {
		return LoadInfo{}, errors.New("invalid /proc/loadavg")
	}
	values := [3]float64{}
	for index := range 3 {
		value, err := strconv.ParseFloat(fields[index], 64)
		if err != nil {
			return LoadInfo{}, err
		}
		values[index] = value
	}
	return LoadInfo{One: values[0], Five: values[1], Fifteen: values[2]}, nil
}

func parseFirstFloat(content []byte) (float64, error) {
	fields := strings.Fields(string(content))
	if len(fields) == 0 {
		return 0, errors.New("value is empty")
	}
	return strconv.ParseFloat(fields[0], 64)
}

func osCheck(info OSInfo) protocol.Check {
	if info.ID == "debian" && (info.Version == "12" || info.Version == "13") {
		return protocol.Check{ID: "os", Status: protocol.StatusPass, Summary: "supported Debian " + info.Version}
	}
	return protocol.Check{ID: "os", Status: protocol.StatusCritical,
		Summary: fmt.Sprintf("unsupported operating system %s %s", info.ID, info.Version)}
}

func memoryCheck(memory MemoryInfo, thresholds HealthThresholds) protocol.Check {
	if memory.TotalBytes == 0 {
		return protocol.Check{ID: "memory", Status: protocol.StatusSkipped, Summary: "memory usage is unavailable"}
	}
	usedBytes := uint64(0)
	if memory.TotalBytes > memory.AvailableBytes {
		usedBytes = memory.TotalBytes - memory.AvailableBytes
	}
	usedPercent := int((float64(usedBytes) / float64(memory.TotalBytes)) * 100) // #nosec G115 -- ratio is bounded.
	status := protocol.StatusPass
	if usedPercent >= thresholds.MemoryCritical {
		status = protocol.StatusCritical
	} else if usedPercent >= thresholds.MemoryWarning {
		status = protocol.StatusWarning
	}
	return protocol.Check{ID: "memory", Status: status,
		Summary: fmt.Sprintf("memory usage is %d%%", usedPercent),
		Details: map[string]any{"used_percent": usedPercent}}
}

func filesystemCheck(filesystem FilesystemUsage, thresholds HealthThresholds) (protocol.Check, *protocol.StructuredError) {
	id := "disk:" + filesystem.MountPoint
	if filesystem.Error != "" {
		failure := structuredFailure(id, errors.New(filesystem.Error))
		return protocol.Check{ID: id, Status: protocol.StatusSkipped, Summary: "filesystem usage is unavailable"}, &failure
	}
	status := protocol.StatusPass
	if filesystem.UsedPercent >= thresholds.DiskCritical {
		status = protocol.StatusCritical
	} else if filesystem.UsedPercent >= thresholds.DiskWarning {
		status = protocol.StatusWarning
	}
	return protocol.Check{ID: id, Status: status,
		Summary: fmt.Sprintf("%s usage is %d%%", filesystem.MountPoint, filesystem.UsedPercent),
		Details: map[string]any{"used_percent": filesystem.UsedPercent}}, nil
}

func timeCheck(synchronized *bool) protocol.Check {
	if synchronized == nil {
		return protocol.Check{ID: "time-sync", Status: protocol.StatusSkipped, Summary: "time synchronization is unavailable"}
	}
	if !*synchronized {
		return protocol.Check{ID: "time-sync", Status: protocol.StatusWarning, Summary: "time is not synchronized"}
	}
	return protocol.Check{ID: "time-sync", Status: protocol.StatusPass, Summary: "time is synchronized"}
}

func aggregateStatus(checks []protocol.Check, failures []protocol.StructuredError) protocol.Status {
	hasWarning, hasPartial := false, len(failures) > 0
	for _, check := range checks {
		switch check.Status {
		case protocol.StatusCritical:
			return protocol.StatusCritical
		case protocol.StatusWarning:
			hasWarning = true
		case protocol.StatusSkipped, protocol.StatusError:
			hasPartial = true
		}
	}
	if hasPartial {
		return protocol.StatusPartial
	}
	if hasWarning {
		return protocol.StatusWarning
	}
	return protocol.StatusPass
}

func dependencyFailure(dependency string, err error) protocol.StructuredError {
	kind := protocol.ErrorGeneral
	if errors.Is(err, execx.ErrNotFound) {
		kind = protocol.ErrorDependency
	}
	return protocol.StructuredError{
		Kind: kind, Code: "dependency_failed", Dependency: dependency, Message: err.Error(),
	}
}
func structuredFailure(code string, err error) protocol.StructuredError {
	return protocol.StructuredError{Kind: protocol.ErrorGeneral, Code: code, Message: err.Error()}
}
func nonEmptyLines(content []byte) []string {
	lines := []string{}
	for _, line := range strings.Split(string(content), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			lines = append(lines, trimmed)
		}
	}
	return lines
}
