package monitoring

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	dpkgStatusLimit     = 4 << 20
	processFileLimit    = 256
	maxProcessEntries   = 4096
	unitOutputLimit     = 32 << 10
	listenerOutputLimit = 128 << 10
	commandErrorLimit   = 16 << 10
)

type productSpec struct {
	ID           string
	Name         string
	Packages     []string
	Units        []string
	Processes    []string
	Ports        []int
	ConfigPaths  []string
	ConfigFormat string
}

var products = []productSpec{
	{
		ID: "alloy", Name: "Grafana Alloy", Packages: []string{"alloy"},
		Units: []string{"alloy.service"}, Processes: []string{"alloy"}, Ports: []int{12345},
		ConfigPaths: []string{"etc/alloy/config.alloy"}, ConfigFormat: "alloy",
	},
	{
		ID: "grafana-agent", Name: "Grafana Agent", Packages: []string{"grafana-agent"},
		Units: []string{"grafana-agent.service"}, Processes: []string{"grafana-agent"}, Ports: []int{12345},
		ConfigPaths: []string{"etc/grafana-agent.yaml"}, ConfigFormat: "yaml",
	},
	{
		ID: "node-exporter", Name: "Prometheus node_exporter",
		Packages: []string{"prometheus-node-exporter"}, Units: []string{"prometheus-node-exporter.service"},
		Processes: []string{"node_exporter", "prometheus-node"},
		Ports:     []int{9100}, ConfigPaths: []string{"etc/default/prometheus-node-exporter"},
		ConfigFormat: "environment",
	},
	{
		ID: "prometheus", Name: "Prometheus", Packages: []string{"prometheus"},
		Units: []string{"prometheus.service"}, Processes: []string{"prometheus"}, Ports: []int{9090},
		ConfigPaths: []string{"etc/prometheus/prometheus.yml"}, ConfigFormat: "yaml",
	},
	{
		ID: "telegraf", Name: "Telegraf", Packages: []string{"telegraf"},
		Units: []string{"telegraf.service"}, Processes: []string{"telegraf"}, Ports: []int{9273},
		ConfigPaths: []string{"etc/telegraf/telegraf.conf"}, ConfigFormat: "toml",
	},
	{
		ID: "zabbix-agent", Name: "Zabbix Agent",
		Packages:  []string{"zabbix-agent", "zabbix-agent2"},
		Units:     []string{"zabbix-agent.service", "zabbix-agent2.service"},
		Processes: []string{"zabbix_agentd", "zabbix_agent2"}, Ports: []int{10050},
		ConfigPaths:  []string{"etc/zabbix/zabbix_agent2.conf", "etc/zabbix/zabbix_agentd.conf"},
		ConfigFormat: "properties",
	},
}

type ProductInventory struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Installed      bool   `json:"installed"`
	PackageName    string `json:"package_name,omitempty"`
	PackageVersion string `json:"package_version,omitempty"`
	UnitState      string `json:"unit_state,omitempty"`
	ProcessCount   int    `json:"process_count"`
	ListeningPorts []int  `json:"listening_ports"`
	ConfigPresent  bool   `json:"config_present"`
}

type packageInfo struct {
	Name    string
	Version string
}

func executeInventory(
	ctx context.Context,
	options Options,
	local probe.Local,
) (protocol.Result, error) {
	inventory, checks, failures, err := collectInventory(ctx, local)
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return protocol.Result{}, fatal
		}
		return protocol.Result{}, dependencyFailure("local monitoring inventory is unavailable")
	}
	return buildResult(options, "monitoring inventory",
		map[string]any{"products": inventory}, checks, failures), nil
}

func collectInventory(
	ctx context.Context,
	local probe.Local,
) ([]ProductInventory, []protocol.Check, []protocol.StructuredError, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	packages, packageIssue := installedPackages(local)
	processCounts, processIssue := monitoringProcesses(ctx, local)
	if fatal := contextFailure(processIssue); fatal != nil {
		return nil, nil, nil, fatal
	}
	listeners, listenerIssue, err := listeningPorts(ctx, local)
	if err != nil {
		return nil, nil, nil, err
	}

	output := make([]ProductInventory, 0, len(products))
	checks := make([]protocol.Check, 0, len(products))
	failures := make([]protocol.StructuredError, 0, 3)
	addIssue := func(issue error, code, message string) {
		if issue != nil {
			failures = append(failures, protocol.StructuredError{
				Kind: protocol.ErrorDependency, Code: code, Message: message, Retryable: false,
			})
		}
	}
	addIssue(packageIssue, "package_inventory", "Local package inventory is unavailable")
	addIssue(processIssue, "process_inventory", "Local process inventory is incomplete")
	addIssue(listenerIssue, "listener_inventory", "Local listener inventory is unavailable")

	for _, product := range products {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, err
		}
		item := ProductInventory{
			ID: product.ID, Name: product.Name, ListeningPorts: []int{},
		}
		for _, packageName := range product.Packages {
			if installed, ok := packages[packageName]; ok {
				item.Installed = true
				item.PackageName = installed.Name
				item.PackageVersion = installed.Version
				break
			}
		}
		for _, processName := range product.Processes {
			item.ProcessCount += processCounts[processName]
		}
		if item.ProcessCount > 0 {
			item.Installed = true
		}
		state, loaded, issue, err := productUnitState(ctx, local, product.Units)
		if err != nil {
			return nil, nil, nil, err
		}
		if issue != nil {
			failures = append(failures, protocol.StructuredError{
				Kind: protocol.ErrorDependency, Code: "unit_inventory",
				Message:    "A local monitoring unit could not be inspected",
				Dependency: "systemctl", Retryable: false,
				Details: map[string]any{"product": product.ID},
			})
		}
		if loaded {
			item.Installed = true
			item.UnitState = state
		}
		for _, port := range product.Ports {
			if listeners[port] {
				item.ListeningPorts = append(item.ListeningPorts, port)
			}
		}
		item.ConfigPresent, issue = productConfigPresent(local, product.ConfigPaths)
		if issue != nil {
			failures = append(failures, protocol.StructuredError{
				Kind: protocol.ErrorConfiguration, Code: "config_metadata",
				Message:   "A local monitoring config path is unsafe",
				Retryable: false, Details: map[string]any{"product": product.ID},
			})
		}
		if item.ConfigPresent {
			item.Installed = true
		}
		check := protocol.Check{
			ID:     "monitoring." + product.ID + ".present",
			Status: protocol.StatusPass, Summary: product.Name + " is installed locally",
		}
		if !item.Installed {
			check.ID = "monitoring." + product.ID + ".missing"
			check.Status = protocol.StatusInfo
			check.Summary = product.Name + " is not installed"
		}
		output = append(output, item)
		checks = append(checks, check)
	}
	return output, checks, failures, nil
}

func installedPackages(local probe.Local) (map[string]packageInfo, error) {
	data, err := local.Read(filepath.Join("var", "lib", "dpkg", "status"), dpkgStatusLimit)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]packageInfo{}, nil
	}
	if err != nil {
		return map[string]packageInfo{}, err
	}
	result := map[string]packageInfo{}
	for _, stanza := range strings.Split(string(data), "\n\n") {
		fields := map[string]string{}
		for _, line := range strings.Split(stanza, "\n") {
			key, value, ok := strings.Cut(line, ":")
			if ok {
				fields[strings.TrimSpace(key)] = strings.TrimSpace(value)
			}
		}
		name := fields["Package"]
		if name != "" && fields["Status"] == "install ok installed" {
			result[name] = packageInfo{Name: name, Version: safeVersion(fields["Version"])}
		}
	}
	return result, nil
}

func safeVersion(value string) string {
	if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\t /\\@") {
		return ""
	}
	if epoch, remainder, found := strings.Cut(value, ":"); found {
		if epoch == "" || remainder == "" {
			return ""
		}
		for _, character := range epoch {
			if character < '0' || character > '9' {
				return ""
			}
		}
		if strings.Contains(remainder, ":") {
			return ""
		}
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			!strings.ContainsRune(".+~_-:", character) {
			return ""
		}
	}
	return value
}

func productUnitState(
	ctx context.Context,
	local probe.Local,
	units []string,
) (string, bool, error, error) {
	var issue error
	for _, unit := range units {
		state, loaded, currentIssue, err := unitState(ctx, local, unit)
		if err != nil {
			return "", false, nil, err
		}
		if currentIssue != nil {
			issue = currentIssue
			continue
		}
		if loaded {
			return state, true, issue, nil
		}
	}
	return "", false, issue, nil
}

func monitoringProcesses(ctx context.Context, local probe.Local) (map[string]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	exists, err := local.Exists("proc")
	if errors.Is(err, os.ErrNotExist) || !exists {
		return map[string]int{}, nil
	}
	if err != nil {
		return map[string]int{}, err
	}
	procPath := filepath.Join(local.Root, "proc")
	before, err := os.Lstat(procPath)
	if err != nil {
		return map[string]int{}, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return map[string]int{}, probe.ErrSymlink
	}
	directory, err := os.Open(procPath)
	if err != nil {
		return map[string]int{}, err
	}
	defer func() { _ = directory.Close() }()
	opened, err := directory.Stat()
	if err != nil || !opened.IsDir() || !os.SameFile(before, opened) {
		return map[string]int{}, probe.ErrInvalidPath
	}
	entries, err := directory.ReadDir(maxProcessEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return map[string]int{}, err
	}
	if len(entries) > maxProcessEntries {
		return map[string]int{}, probe.ErrTooLarge
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Name() < entries[right].Name()
	})
	result := map[string]int{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.ParseUint(entry.Name(), 10, 32); err != nil {
			continue
		}
		data, readErr := local.Read(filepath.Join("proc", entry.Name(), "comm"), processFileLimit)
		if readErr != nil {
			continue
		}
		name := strings.TrimSpace(string(data))
		for _, product := range products {
			if slicesContain(product.Processes, name) {
				result[name]++
			}
		}
	}
	return result, nil
}

func slicesContain(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func unitState(
	ctx context.Context,
	local probe.Local,
	unit string,
) (string, bool, error, error) {
	output, err := local.Run(ctx, probe.Command{
		Program: "systemctl",
		Arguments: []string{
			"show", unit, "--property=LoadState,ActiveState,SubState", "--no-pager",
		},
		StdoutLimit: unitOutputLimit, StderrLimit: commandErrorLimit,
	})
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return "", false, nil, fatal
		}
		if errors.Is(err, execx.ErrNotFound) {
			return "", false, nil, nil
		}
		return "", false, err, nil
	}
	if output.StdoutTruncated || output.StderrTruncated {
		return "", false, errors.New("truncated unit output"), nil
	}
	if len(output.Stderr) != 0 {
		return "", false, errors.New("unexpected unit diagnostics"), nil
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !validUnitKey(key) || !validUnitValue(value) {
			return "", false, errors.New("malformed unit output"), nil
		}
		if _, duplicate := values[key]; duplicate {
			return "", false, errors.New("duplicate unit field"), nil
		}
		values[key] = value
	}
	if values["LoadState"] == "not-found" {
		return "", false, nil, nil
	}
	if output.ExitCode != 0 || len(values) != 3 ||
		values["LoadState"] != "loaded" ||
		values["ActiveState"] == "" || values["SubState"] == "" {
		return "", false, errors.New("invalid unit state"), nil
	}
	return values["ActiveState"] + "/" + values["SubState"], true, nil, nil
}

func validUnitKey(value string) bool {
	switch value {
	case "LoadState", "ActiveState", "SubState":
		return true
	default:
		return false
	}
}

func validUnitValue(value string) bool {
	switch value {
	case "loaded", "not-found", "active", "inactive", "failed", "activating",
		"deactivating", "running", "dead", "exited":
		return true
	default:
		return false
	}
}

func listeningPorts(
	ctx context.Context,
	local probe.Local,
) (map[int]bool, error, error) {
	output, err := local.Run(ctx, probe.Command{
		Program: "ss", Arguments: []string{"-H", "-lntu"},
		StdoutLimit: listenerOutputLimit, StderrLimit: commandErrorLimit,
	})
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return nil, nil, fatal
		}
		if errors.Is(err, execx.ErrNotFound) {
			return map[int]bool{}, nil, nil
		}
		return map[int]bool{}, err, nil
	}
	if output.StdoutTruncated || output.StderrTruncated {
		return map[int]bool{}, errors.New("truncated listener output"), nil
	}
	if output.ExitCode != 0 || len(output.Stderr) != 0 {
		return map[int]bool{}, errors.New("listener probe failed"), nil
	}
	result := map[int]bool{}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 6 || !validListenerProtocolState(fields[0], fields[1]) {
			return map[int]bool{}, errors.New("malformed listener output"), nil
		}
		if _, err := strconv.ParseUint(fields[2], 10, 64); err != nil {
			return map[int]bool{}, errors.New("malformed listener queue"), nil
		}
		if _, err := strconv.ParseUint(fields[3], 10, 64); err != nil {
			return map[int]bool{}, errors.New("malformed listener queue"), nil
		}
		port, ok := parseAddressPort(fields[4])
		if !ok {
			return map[int]bool{}, errors.New("malformed listener address"), nil
		}
		result[port] = true
	}
	return result, nil, nil
}

func validListenerProtocolState(protocolName, state string) bool {
	switch protocolName {
	case "tcp":
		return state == "LISTEN"
	case "udp":
		return state == "UNCONN"
	default:
		return false
	}
}

func parseAddressPort(value string) (int, bool) {
	index := strings.LastIndexByte(value, ':')
	if index < 0 || index == len(value)-1 || strings.Contains(value[index+1:], "*") {
		return 0, false
	}
	port, err := strconv.Atoi(value[index+1:])
	if err != nil || port < 1 || port > 65535 {
		return 0, false
	}
	return port, true
}

func productConfigPresent(local probe.Local, paths []string) (bool, error) {
	for _, path := range paths {
		_, err := local.Read(filepath.FromSlash(path), configFileLimit)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, err
		}
		return true, nil
	}
	return false, nil
}
