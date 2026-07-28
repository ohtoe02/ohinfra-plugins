package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	statusOutputLimit int64 = 256 * 1024
	commandErrorLimit int64 = 32 * 1024
	processNameLimit  int64 = 256
	maxProcesses            = 4096
)

var (
	unitStatePattern     = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
	clientVersionPattern = regexp.MustCompile(
		`^v[0-9]+(?:\.[0-9]+){1,2}(?:[-+][0-9A-Za-z.-]+)?$`,
	)
	clientPlatformPattern = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9_]+$`)
)

type ClientVersion struct {
	Version  string `json:"version"`
	Platform string `json:"platform"`
}

type KubeletState struct {
	LoadState      string `json:"load_state"`
	ActiveState    string `json:"active_state"`
	SubState       string `json:"sub_state"`
	ProcessRunning bool   `json:"process_running"`
	ProcessIDs     []int  `json:"process_ids"`
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
		return errors.New("k8s local runner rejected environment or stdin overrides")
	}
	var expected []string
	switch spec.Program {
	case "kubectl":
		expected = []string{"version", "--client", "--output=json"}
	case "systemctl":
		expected = []string{
			"show", "--no-pager", "--property=LoadState,ActiveState,SubState",
			"--", "kubelet.service",
		}
	default:
		return fmt.Errorf("k8s local runner rejected executable %q", spec.Program)
	}
	if !equalStrings(spec.Arguments, expected) {
		return fmt.Errorf("k8s local runner rejected non-local argv for %s", spec.Program)
	}
	return nil
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

func executeStatus(ctx context.Context, options Options) (protocol.Result, error) {
	local := probe.Local{
		Root:   options.Root,
		Runner: guardedRunner{runner: options.Runner},
	}
	data := map[string]any{}
	checks := []protocol.Check{}
	failures := []protocol.StructuredError{}

	client, clientErr := collectClientVersion(ctx, local)
	if fatal := contextFailure(clientErr); fatal != nil {
		return protocol.Result{}, fatal
	}
	if clientErr == nil {
		data["client"] = client
		checks = append(checks, protocol.Check{
			ID: "k8s.client", Status: protocol.StatusPass,
			Summary: "Collected local kubectl client metadata",
		})
	} else {
		checks = append(checks, protocol.Check{
			ID: "k8s.client", Status: protocol.StatusSkipped,
			Summary: "Local kubectl client metadata is unavailable",
		})
		failures = append(failures, probeFailure(
			"kubectl", "client_version_unavailable", clientErr,
		))
	}

	kubelet, unitErr := collectKubeletUnit(ctx, local)
	if fatal := contextFailure(unitErr); fatal != nil {
		return protocol.Result{}, fatal
	}
	processIDs, processErr := findKubeletProcesses(ctx, local, options.Root)
	if fatal := contextFailure(processErr); fatal != nil {
		return protocol.Result{}, fatal
	}
	if processErr == nil {
		kubelet.ProcessIDs = processIDs
		kubelet.ProcessRunning = len(processIDs) > 0
	}
	if unitErr == nil || processErr == nil {
		data["kubelet"] = kubelet
	}
	if unitErr == nil && processErr == nil {
		checks = append(checks, protocol.Check{
			ID: "k8s.kubelet", Status: protocol.StatusPass,
			Summary: "Collected local kubelet state",
		})
	} else {
		checks = append(checks, protocol.Check{
			ID: "k8s.kubelet", Status: protocol.StatusPartial,
			Summary: "Local kubelet state is incomplete",
		})
	}
	if unitErr != nil {
		failures = append(failures, probeFailure(
			"systemctl", "kubelet_unit_unavailable", unitErr,
		))
	}
	if processErr != nil {
		failures = append(failures, protocol.StructuredError{
			Kind: protocol.ErrorGeneral, Code: "kubelet_processes_unavailable",
			Message: "Local kubelet process metadata is unavailable",
		})
	}
	return buildResult(options, "k8s status", data, checks, failures), nil
}

func contextFailure(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func probeFailure(dependency, code string, err error) protocol.StructuredError {
	if errors.Is(err, execx.ErrNotFound) {
		return protocol.StructuredError{
			Kind: protocol.ErrorDependency, Code: code,
			Message:    "Optional local dependency is unavailable",
			Dependency: dependency,
		}
	}
	return protocol.StructuredError{
		Kind: protocol.ErrorGeneral, Code: code,
		Message: "Local Kubernetes probe failed",
	}
}

func collectClientVersion(ctx context.Context, local probe.Local) (ClientVersion, error) {
	output, err := local.Run(ctx, probe.Command{
		Program: "kubectl", Arguments: []string{"version", "--client", "--output=json"},
		StdoutLimit: statusOutputLimit, StderrLimit: commandErrorLimit,
	})
	if err != nil {
		return ClientVersion{}, err
	}
	if err := ctx.Err(); err != nil {
		return ClientVersion{}, err
	}
	if output.ExitCode != 0 || output.StdoutTruncated {
		return ClientVersion{}, errors.New("kubectl client version probe failed")
	}
	var document struct {
		ClientVersion struct {
			Major        string `json:"major"`
			Minor        string `json:"minor"`
			GitVersion   string `json:"gitVersion"`
			GitCommit    string `json:"gitCommit"`
			GitTreeState string `json:"gitTreeState"`
			BuildDate    string `json:"buildDate"`
			GoVersion    string `json:"goVersion"`
			Compiler     string `json:"compiler"`
			Platform     string `json:"platform"`
		} `json:"clientVersion"`
		KustomizeVersion string `json:"kustomizeVersion"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(output.Stdout)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return ClientVersion{}, errors.New("kubectl returned invalid client version JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ClientVersion{}, errors.New("kubectl returned trailing client version JSON")
	}
	if !clientVersionPattern.MatchString(document.ClientVersion.GitVersion) ||
		!clientPlatformPattern.MatchString(document.ClientVersion.Platform) {
		return ClientVersion{}, errors.New("kubectl client version metadata is invalid")
	}
	return ClientVersion{
		Version: document.ClientVersion.GitVersion, Platform: document.ClientVersion.Platform,
	}, nil
}

func collectKubeletUnit(ctx context.Context, local probe.Local) (KubeletState, error) {
	output, err := local.Run(ctx, probe.Command{
		Program: "systemctl",
		Arguments: []string{
			"show", "--no-pager", "--property=LoadState,ActiveState,SubState",
			"--", "kubelet.service",
		},
		StdoutLimit: statusOutputLimit, StderrLimit: commandErrorLimit,
	})
	if err != nil {
		return KubeletState{}, err
	}
	if err := ctx.Err(); err != nil {
		return KubeletState{}, err
	}
	if output.ExitCode != 0 || output.StdoutTruncated {
		return KubeletState{}, errors.New("kubelet systemd probe failed")
	}
	state, err := parseKubeletUnit(output.Stdout)
	if err != nil {
		return KubeletState{}, err
	}
	return state, nil
}

func parseKubeletUnit(encoded []byte) (KubeletState, error) {
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(encoded)), "\n") {
		key, value, ok := strings.Cut(strings.TrimSuffix(line, "\r"), "=")
		if !ok {
			return KubeletState{}, errors.New("systemctl returned invalid kubelet state")
		}
		switch key {
		case "LoadState", "ActiveState", "SubState":
			if _, duplicate := values[key]; duplicate {
				return KubeletState{}, errors.New("systemctl returned duplicate kubelet state")
			}
			if !unitStatePattern.MatchString(value) {
				return KubeletState{}, errors.New("systemctl returned invalid kubelet state value")
			}
			values[key] = value
		default:
			return KubeletState{}, errors.New("systemctl returned unexpected kubelet state")
		}
	}
	for _, key := range []string{"LoadState", "ActiveState", "SubState"} {
		if values[key] == "" {
			return KubeletState{}, errors.New("systemctl omitted required kubelet state")
		}
	}
	return KubeletState{
		LoadState: values["LoadState"], ActiveState: values["ActiveState"],
		SubState: values["SubState"], ProcessIDs: []int{},
	}, nil
}

func findKubeletProcesses(
	ctx context.Context,
	local probe.Local,
	root string,
) ([]int, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	procPath := filepath.Join(root, "proc")
	info, err := os.Lstat(procPath)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, errors.New("proc root must be a non-symlink directory")
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
		name, err := local.Read(filepath.ToSlash(filepath.Join("proc", entry.Name(), "comm")),
			processNameLimit)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(string(name)) == "kubelet" {
			processIDs = append(processIDs, pid)
		}
	}
	sort.Ints(processIDs)
	return processIDs, nil
}
