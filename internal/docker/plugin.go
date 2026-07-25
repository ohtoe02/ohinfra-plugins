package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	pluginconfig "github.com/ohtoe02/ohtools-plugins/internal/config"
	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/redact"
)

const (
	Name        = "docker-base"
	Description = "Docker Engine and Compose diagnostics for ohtools."
	ConfigPath  = "/etc/ohtools/plugins/docker-base.yaml"
)

var objectName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/+~-]{0,254}$`)

type Config struct {
	LogsSince       string `yaml:"logs_since"`
	LogsLines       int    `yaml:"logs_lines"`
	SearchMaxDepth  int    `yaml:"search_max_depth"`
	SearchMaxFiles  int    `yaml:"search_max_files"`
	RegistryTimeout string `yaml:"registry_timeout"`
}

func DefaultConfig() Config {
	return Config{
		LogsSince: "1h", LogsLines: 200, SearchMaxDepth: 6,
		SearchMaxFiles: 1000, RegistryTimeout: "10s",
	}
}

type RegistryProbe func(context.Context, string, time.Duration) (map[string]any, error)

type Options struct {
	Version       string
	Commit        string
	BuildDate     string
	ConfigPath    string
	Runner        execx.Runner
	Host          string
	RegistryProbe RegistryProbe
}

func NewDefinition(options Options) protocol.Definition {
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.ConfigPath == "" {
		options.ConfigPath = ConfigPath
	}
	if options.Runner == nil {
		options.Runner = execx.OSRunner{Resolver: execx.SystemResolver()}
	}
	if options.Host == "" {
		options.Host, _ = os.Hostname()
	}
	if options.RegistryProbe == nil {
		options.RegistryProbe = probeRegistry
	}
	return protocol.Definition{
		Manifest: protocol.Manifest{
			ProtocolVersion: protocol.ProtocolVersion, Name: Name,
			Version: options.Version, Description: Description, Commands: manifestCommands(),
		},
		Execute: func(ctx context.Context, invocation protocol.Invocation) (protocol.Result, error) {
			return execute(ctx, invocation, options)
		},
	}
}

func manifestCommands() []protocol.Command {
	object := []protocol.Argument{{Name: "object", Description: "Docker object identifier", Required: true}}
	container := []protocol.Argument{{Name: "container", Description: "Container name or ID", Required: true}}
	file := []protocol.Argument{{Name: "file", Description: "Compose YAML file", Required: true}}
	diagnostic := func(path []string, use, short string, arguments []protocol.Argument, flags []protocol.Flag) protocol.Command {
		if arguments == nil {
			arguments = []protocol.Argument{}
		}
		if flags == nil {
			flags = []protocol.Flag{}
		}
		return protocol.Command{
			Path: path, Use: use, Short: short, Category: protocol.CategoryDiagnostic,
			Arguments: arguments, Flags: flags,
		}
	}
	return []protocol.Command{
		diagnostic([]string{"docker", "status"}, "status", "Check Docker client and daemon availability", nil, nil),
		diagnostic([]string{"docker", "info"}, "info", "Show Docker daemon information", nil, nil),
		diagnostic([]string{"docker", "ps"}, "ps", "List Docker containers", nil, nil),
		diagnostic([]string{"docker", "usage"}, "usage", "Show Docker disk usage", nil, nil),
		diagnostic([]string{"docker", "logs"}, "logs <container>", "Show redacted container logs", container,
			[]protocol.Flag{
				{Name: "since", Type: "duration", Description: "Log lookback duration", Default: "1h0m0s"},
				{Name: "lines", Type: "int", Description: "Maximum log lines", Default: 200},
			}),
		diagnostic([]string{"docker", "inspect"}, "inspect <object>", "Inspect a Docker object", object, nil),
		diagnostic([]string{"docker", "health"}, "health <container>", "Show container health state", container, nil),
		diagnostic([]string{"docker", "registry-test"}, "registry-test <registry>", "Test anonymous registry v2 access",
			[]protocol.Argument{{Name: "registry", Description: "HTTPS registry host or URL", Required: true}}, nil),
		diagnostic([]string{"compose", "find"}, "find [path]", "Find Compose files without following symlinks",
			[]protocol.Argument{{Name: "path", Description: "Search root"}}, nil),
		diagnostic([]string{"compose", "validate"}, "validate <file>", "Validate a Compose file", file, nil),
		diagnostic([]string{"compose", "inspect"}, "inspect <file>", "Render the normalized Compose model", file, nil),
		diagnostic([]string{"compose", "diff"}, "diff <file>", "Compare Compose model with running services", file, nil),
	}
}

func execute(ctx context.Context, invocation protocol.Invocation, options Options) (protocol.Result, error) {
	settings, err := pluginconfig.Load(options.ConfigPath, DefaultConfig())
	if err != nil {
		return protocol.Result{}, protocol.ExitError{Code: protocol.ExitConfiguration, Err: err}
	}
	registryTimeout, err := validateConfig(settings)
	if err != nil {
		return protocol.Result{}, protocol.ExitError{Code: protocol.ExitConfiguration, Err: err}
	}
	switch {
	case slices.Equal(invocation.CommandPath, []string{"docker", "status"}):
		if err := noArguments(invocation); err != nil {
			return protocol.Result{}, err
		}
		return runJSON(ctx, options, "docker status", "docker", []string{"version", "--format", "{{json .}}"}, false), nil
	case slices.Equal(invocation.CommandPath, []string{"docker", "info"}):
		if err := noArguments(invocation); err != nil {
			return protocol.Result{}, err
		}
		return runJSON(ctx, options, "docker info", "docker", []string{"info", "--format", "{{json .}}"}, false), nil
	case slices.Equal(invocation.CommandPath, []string{"docker", "ps"}):
		if err := noArguments(invocation); err != nil {
			return protocol.Result{}, err
		}
		return runJSON(ctx, options, "docker ps", "docker",
			[]string{"ps", "--no-trunc", "--format", "{{json .}}"}, true), nil
	case slices.Equal(invocation.CommandPath, []string{"docker", "usage"}):
		if err := noArguments(invocation); err != nil {
			return protocol.Result{}, err
		}
		return runJSON(ctx, options, "docker usage", "docker",
			[]string{"system", "df", "--format", "{{json .}}"}, true), nil
	case slices.Equal(invocation.CommandPath, []string{"docker", "logs"}):
		container, err := oneObject(invocation)
		if err != nil {
			return protocol.Result{}, err
		}
		since, lines, err := logRange(invocation.Options, settings)
		if err != nil {
			return protocol.Result{}, protocol.ExitError{Code: protocol.ExitArguments, Err: err}
		}
		return runLines(ctx, options, "docker logs", "docker",
			[]string{"logs", "--since", since.String(), "--tail", fmt.Sprint(lines), "--", container}), nil
	case slices.Equal(invocation.CommandPath, []string{"docker", "inspect"}):
		object, err := oneObject(invocation)
		if err != nil {
			return protocol.Result{}, err
		}
		return runJSON(ctx, options, "docker inspect", "docker", []string{"inspect", "--", object}, false), nil
	case slices.Equal(invocation.CommandPath, []string{"docker", "health"}):
		container, err := oneObject(invocation)
		if err != nil {
			return protocol.Result{}, err
		}
		return runJSON(ctx, options, "docker health", "docker",
			[]string{"inspect", "--format", "{{json .State.Health}}", "--", container}, false), nil
	case slices.Equal(invocation.CommandPath, []string{"docker", "registry-test"}):
		registry, err := oneRawArgument(invocation)
		if err != nil {
			return protocol.Result{}, err
		}
		started := time.Now()
		data, probeErr := options.RegistryProbe(ctx, registry, registryTimeout)
		if probeErr != nil {
			return failure(options, "docker registry-test", started, protocol.ErrorGeneral, "registry_probe_failed", probeErr), nil
		}
		return success(options, "docker registry-test", started, data), nil
	case slices.Equal(invocation.CommandPath, []string{"compose", "find"}):
		if len(invocation.Arguments) > 1 {
			return protocol.Result{}, argumentError("compose find accepts at most one path")
		}
		root := "."
		if len(invocation.Arguments) == 1 {
			root = invocation.Arguments[0]
		}
		started := time.Now()
		files, findErr := findComposeFiles(root, settings.SearchMaxDepth, settings.SearchMaxFiles)
		if findErr != nil {
			return failure(options, "compose find", started, protocol.ErrorGeneral, "compose_find_failed", findErr), nil
		}
		return success(options, "compose find", started, map[string]any{"root": root, "files": files}), nil
	case slices.Equal(invocation.CommandPath, []string{"compose", "validate"}):
		filePath, err := composeFile(invocation)
		if err != nil {
			return protocol.Result{}, err
		}
		return runText(ctx, options, "compose validate", "docker",
			[]string{"compose", "-f", filePath, "config", "--quiet"}), nil
	case slices.Equal(invocation.CommandPath, []string{"compose", "inspect"}):
		filePath, err := composeFile(invocation)
		if err != nil {
			return protocol.Result{}, err
		}
		return runJSON(ctx, options, "compose inspect", "docker",
			[]string{"compose", "-f", filePath, "config", "--format", "json"}, false), nil
	case slices.Equal(invocation.CommandPath, []string{"compose", "diff"}):
		filePath, err := composeFile(invocation)
		if err != nil {
			return protocol.Result{}, err
		}
		return composeDiff(ctx, options, filePath), nil
	default:
		return protocol.Result{}, argumentError("unsupported docker-base command")
	}
}

func runJSON(
	ctx context.Context,
	options Options,
	command, program string,
	arguments []string,
	lines bool,
) protocol.Result {
	started := time.Now()
	output, err := options.Runner.Run(ctx, execx.Spec{
		Program: program, Arguments: arguments, StdoutLimit: 10 << 20, StderrLimit: 1 << 20,
	})
	if err != nil {
		return commandError(options, command, started, program, execx.Output{}, err)
	}
	if output.ExitCode != 0 {
		return commandError(options, command, started, program, output, nil)
	}
	var data any
	if lines {
		items := []any{}
		for _, line := range strings.Split(strings.TrimSpace(string(output.Stdout)), "\n") {
			if line == "" {
				continue
			}
			var item any
			if err := json.Unmarshal([]byte(line), &item); err != nil {
				return failure(options, command, started, protocol.ErrorGeneral, "invalid_docker_json", err)
			}
			items = append(items, item)
		}
		data = items
	} else if len(strings.TrimSpace(string(output.Stdout))) == 0 {
		data = map[string]any{}
	} else if err := json.Unmarshal(output.Stdout, &data); err != nil {
		return failure(options, command, started, protocol.ErrorGeneral, "invalid_docker_json", err)
	}
	return success(options, command, started, map[string]any{"result": redact.Value(data)})
}

func runLines(ctx context.Context, options Options, command, program string, arguments []string) protocol.Result {
	started := time.Now()
	output, err := options.Runner.Run(ctx, execx.Spec{
		Program: program, Arguments: arguments, StdoutLimit: 10 << 20, StderrLimit: 1 << 20,
	})
	if err != nil || output.ExitCode != 0 {
		return commandError(options, command, started, program, output, err)
	}
	lines := []string{}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		if line != "" {
			lines = append(lines, redact.String(line))
		}
	}
	return success(options, command, started, map[string]any{"lines": lines})
}

func runText(ctx context.Context, options Options, command, program string, arguments []string) protocol.Result {
	started := time.Now()
	output, err := options.Runner.Run(ctx, execx.Spec{
		Program: program, Arguments: arguments, StdoutLimit: 10 << 20, StderrLimit: 1 << 20,
	})
	if err != nil || output.ExitCode != 0 {
		return commandError(options, command, started, program, output, err)
	}
	return success(options, command, started, map[string]any{
		"stdout": redact.String(strings.TrimSpace(string(output.Stdout))),
	})
}

func composeDiff(ctx context.Context, options Options, filePath string) protocol.Result {
	started := time.Now()
	configOutput, err := options.Runner.Run(ctx, execx.Spec{
		Program: "docker", Arguments: []string{"compose", "-f", filePath, "config", "--format", "json"},
		StdoutLimit: 10 << 20, StderrLimit: 1 << 20,
	})
	if err != nil || configOutput.ExitCode != 0 {
		return commandError(options, "compose diff", started, "docker", configOutput, err)
	}
	psOutput, err := options.Runner.Run(ctx, execx.Spec{
		Program: "docker", Arguments: []string{"compose", "-f", filePath, "ps", "--format", "json"},
		StdoutLimit: 10 << 20, StderrLimit: 1 << 20,
	})
	if err != nil || psOutput.ExitCode != 0 {
		return commandError(options, "compose diff", started, "docker", psOutput, err)
	}
	var desired any
	if err := json.Unmarshal(configOutput.Stdout, &desired); err != nil {
		return failure(options, "compose diff", started, protocol.ErrorGeneral, "invalid_compose_json", err)
	}
	running := []any{}
	for _, line := range strings.Split(strings.TrimSpace(string(psOutput.Stdout)), "\n") {
		if line == "" {
			continue
		}
		var item any
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return failure(options, "compose diff", started, protocol.ErrorGeneral, "invalid_compose_json", err)
		}
		running = append(running, item)
	}
	return success(options, "compose diff", started, map[string]any{
		"desired": redact.Value(desired), "running": redact.Value(running),
	})
}

func commandError(
	options Options, command string, started time.Time, dependency string, output execx.Output, err error,
) protocol.Result {
	if errors.Is(err, execx.ErrNotFound) {
		return failure(options, command, started, protocol.ErrorDependency, "missing_dependency",
			fmt.Errorf("%s executable is unavailable", dependency))
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return failure(options, command, started, protocol.ErrorTimeout, "command_timeout", err)
	}
	message := strings.TrimSpace(string(output.Stderr))
	if err != nil {
		message = err.Error()
	}
	kind := protocol.ErrorGeneral
	code := "docker_command_failed"
	lower := strings.ToLower(message)
	if strings.Contains(lower, "permission denied") &&
		(strings.Contains(lower, "socket") || strings.Contains(lower, "docker daemon")) {
		kind, code = protocol.ErrorPrivilege, "docker_socket_denied"
	}
	if message == "" {
		message = fmt.Sprintf("%s exited with %d", dependency, output.ExitCode)
	}
	return failure(options, command, started, kind, code, errors.New(message))
}

func success(options Options, command string, started time.Time, data map[string]any) protocol.Result {
	return redact.Result(protocol.Normalize(protocol.Result{
		Command: command, Status: protocol.StatusPass, Timestamp: time.Now().Format(time.RFC3339Nano),
		DurationMS: time.Since(started).Milliseconds(), Host: options.Host, Tool: tool(options), Data: data,
	}))
}

func failure(
	options Options, command string, started time.Time,
	kind protocol.ErrorKind, code string, err error,
) protocol.Result {
	status := protocol.StatusError
	if kind == protocol.ErrorDependency {
		status = protocol.StatusSkipped
	}
	return redact.Result(protocol.Normalize(protocol.Result{
		Command: command, Status: status, Timestamp: time.Now().Format(time.RFC3339Nano),
		DurationMS: time.Since(started).Milliseconds(), Host: options.Host, Tool: tool(options),
		Errors: []protocol.StructuredError{{Kind: kind, Code: code, Message: err.Error()}},
	}))
}

func tool(options Options) protocol.Tool {
	return protocol.Tool{
		Name: Name, Version: options.Version, Commit: options.Commit,
		BuildDate: options.BuildDate, GoVersion: runtime.Version(), Architecture: runtime.GOARCH,
	}
}

func noArguments(invocation protocol.Invocation) error {
	if len(invocation.Arguments) != 0 {
		return argumentError("command accepts no arguments")
	}
	return nil
}

func oneObject(invocation protocol.Invocation) (string, error) {
	value, err := oneRawArgument(invocation)
	if err != nil {
		return "", err
	}
	if !objectName.MatchString(value) || strings.HasPrefix(value, "-") ||
		strings.ContainsAny(value, `;\`+"\n\r\t ") || strings.Contains(value, "..") {
		return "", argumentError("invalid Docker object identifier")
	}
	return value, nil
}

func oneRawArgument(invocation protocol.Invocation) (string, error) {
	if len(invocation.Arguments) != 1 || strings.TrimSpace(invocation.Arguments[0]) == "" {
		return "", argumentError("command requires exactly one argument")
	}
	return invocation.Arguments[0], nil
}

func argumentError(message string) error {
	return protocol.ExitError{Code: protocol.ExitArguments, Err: errors.New(message)}
}

func logRange(options map[string]any, settings Config) (time.Duration, int, error) {
	sinceText, lines := settings.LogsSince, settings.LogsLines
	if value, present := options["since"]; present {
		typed, ok := value.(string)
		if !ok {
			return 0, 0, errors.New("since must be a duration")
		}
		sinceText = typed
	}
	if value, present := options["lines"]; present {
		switch typed := value.(type) {
		case int:
			lines = typed
		case float64:
			if typed != float64(int(typed)) {
				return 0, 0, errors.New("lines must be an integer")
			}
			lines = int(typed)
		default:
			return 0, 0, errors.New("lines must be an integer")
		}
	}
	since, err := time.ParseDuration(sinceText)
	if err != nil || since <= 0 || lines <= 0 || lines > 100000 {
		return 0, 0, errors.New("invalid Docker log range")
	}
	return since, lines, nil
}

func validateConfig(settings Config) (time.Duration, error) {
	if _, _, err := logRange(nil, settings); err != nil {
		return 0, err
	}
	timeout, err := time.ParseDuration(settings.RegistryTimeout)
	if err != nil || timeout <= 0 || settings.SearchMaxDepth < 0 ||
		settings.SearchMaxDepth > 32 || settings.SearchMaxFiles <= 0 ||
		settings.SearchMaxFiles > 10000 {
		return 0, errors.New("invalid docker-base search or registry limits")
	}
	return timeout, nil
}

func composeFile(invocation protocol.Invocation) (string, error) {
	value, err := oneRawArgument(invocation)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(value)
	if err != nil {
		return "", argumentError("invalid Compose file path")
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return "", argumentError("Compose file is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", argumentError("Compose path must be a regular non-symlink file")
	}
	return absolute, nil
}

func findComposeFiles(root string, maxDepth, maxFiles int) ([]string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, errors.New("Compose search root must be a non-symlink directory")
	}
	names := map[string]struct{}{
		"compose.yaml": {}, "compose.yml": {}, "docker-compose.yaml": {}, "docker-compose.yml": {},
	}
	files := []string{}
	err = filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(absolute, path)
		if err != nil {
			return err
		}
		depth := 0
		if relative != "." {
			depth = len(strings.Split(relative, string(filepath.Separator)))
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() && depth > maxDepth {
			return filepath.SkipDir
		}
		if entry.Type().IsRegular() {
			if _, match := names[strings.ToLower(entry.Name())]; match {
				files = append(files, path)
				if len(files) > maxFiles {
					return errors.New("Compose search exceeded file limit")
				}
			}
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func probeRegistry(ctx context.Context, raw string, timeout time.Duration) (map[string]any, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("registry must be an HTTPS host without credentials, query, or path")
	}
	parsed.Path = "/v2/"
	probeContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(probeContext, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{
		Transport: &http.Transport{Proxy: nil},
		CheckRedirect: func(request *http.Request, previous []*http.Request) error {
			if len(previous) >= 5 || request.URL.Scheme != "https" ||
				!strings.EqualFold(request.URL.Hostname(), parsed.Hostname()) {
				return errors.New("unsafe registry redirect")
			}
			return nil
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	return map[string]any{
		"registry": parsed.Host, "status_code": response.StatusCode,
		"anonymous": response.StatusCode >= 200 && response.StatusCode < 300,
	}, nil
}
