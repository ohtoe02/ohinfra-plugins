package systemd

import (
	"context"
	"errors"
	"os"
	"runtime"
	"slices"
	"time"

	pluginconfig "github.com/ohtoe02/ohtools-plugins/internal/config"
	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	Name        = "systemd-base"
	Description = "Systemd service status, logs, and controlled restart operations for ohtools."
	ConfigPath  = "/etc/ohtools/plugins/systemd-base.yaml"
)

type Config struct {
	LogsSince            string `yaml:"logs_since"`
	LogsLines            int    `yaml:"logs_lines"`
	RestartVerifyTimeout string `yaml:"restart_verify_timeout"`
}

func DefaultConfig() Config {
	return Config{LogsSince: "1h", LogsLines: 200, RestartVerifyTimeout: "15s"}
}

type Options struct {
	Version         string
	Commit          string
	BuildDate       string
	ConfigPath      string
	Runner          execx.Runner
	Inspector       Inspector
	MutationBackend MutationBackend
	Host            string
	Now             func() time.Time
	Sleep           func(context.Context, time.Duration) error
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
	if options.Inspector == nil {
		options.Inspector = commandInspector{runner: options.Runner}
	}
	if options.MutationBackend == nil {
		options.MutationBackend = commandMutationBackend{runner: options.Runner}
	}
	if options.Host == "" {
		options.Host, _ = os.Hostname()
	}
	return protocol.NewDefinition(
		protocol.DefinitionSpec{Name: Name, Version: options.Version, Description: Description},
		protocol.Diagnostic(protocol.CommandSpec{
			Path: []string{"service", "status"}, Use: "status <unit>", Short: "Show stable systemd unit properties",
			Arguments: []protocol.Argument{{Name: "unit", Description: "Systemd unit name", Required: true}},
			Flags:     []protocol.Flag{},
		}, func(ctx context.Context, invocation protocol.Invocation) (protocol.Result, error) {
			return execute(ctx, invocation, options)
		}),
		protocol.Diagnostic(protocol.CommandSpec{
			Path: []string{"service", "logs"}, Use: "logs <unit>", Short: "Show redacted systemd journal entries",
			Arguments: []protocol.Argument{{Name: "unit", Description: "Systemd unit name", Required: true}},
			Flags: []protocol.Flag{
				{Name: "since", Type: "duration", Description: "Journal lookback duration", Default: "1h0m0s"},
				{Name: "lines", Type: "int", Description: "Maximum journal lines", Default: 200},
			},
		}, func(ctx context.Context, invocation protocol.Invocation) (protocol.Result, error) {
			return execute(ctx, invocation, options)
		}),
		protocol.Mutation(
			protocol.CommandSpec{
				Path: []string{"service", "restart"}, Use: "restart <unit>", Short: "Restart and verify a systemd unit",
				Arguments: []protocol.Argument{{Name: "unit", Description: "Systemd unit name", Required: true}},
				Flags:     []protocol.Flag{},
			},
			protocol.CategoryOperational,
			protocol.Risk{RequiresRoot: true, RequiresConfirmation: true},
			func(ctx context.Context, invocation protocol.Invocation) (protocol.Plan, error) {
				manager, _, err := managerFor(options)
				if err != nil {
					return protocol.Plan{}, err
				}
				unit, err := validateInvocation(invocation, "restart")
				if err != nil {
					return protocol.Plan{}, err
				}
				return (RestartPlanner{Inspector: manager.Inspector}).Plan(ctx, unit)
			},
			func(ctx context.Context, invocation protocol.Invocation, plan protocol.Plan) (protocol.Result, error) {
				manager, _, err := managerFor(options)
				if err != nil {
					return protocol.Result{}, err
				}
				unit, err := validateInvocation(invocation, "restart")
				if err != nil {
					return protocol.Result{}, err
				}
				return manager.ExecuteRestart(ctx, unit, plan)
			},
		),
	)
}

func execute(ctx context.Context, invocation protocol.Invocation, options Options) (protocol.Result, error) {
	manager, settings, err := managerFor(options)
	if err != nil {
		return protocol.Result{}, err
	}
	switch {
	case slices.Equal(invocation.CommandPath, []string{"service", "status"}):
		unit, err := validateInvocation(invocation, "status")
		if err != nil {
			return protocol.Result{}, err
		}
		return manager.Status(ctx, unit), nil
	case slices.Equal(invocation.CommandPath, []string{"service", "logs"}):
		unit, err := validateInvocation(invocation, "logs")
		if err != nil {
			return protocol.Result{}, err
		}
		since, lines, err := logRange(invocation.Options, settings)
		if err != nil {
			return protocol.Result{}, protocol.ExitError{Code: protocol.ExitArguments, Err: err}
		}
		return manager.Logs(ctx, unit, since, lines), nil
	default:
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("unsupported systemd-base command"),
		}
	}
}

func managerFor(options Options) (Manager, Config, error) {
	settings, err := pluginconfig.Load(options.ConfigPath, DefaultConfig())
	if err != nil {
		return Manager{}, Config{}, protocol.ExitError{Code: protocol.ExitConfiguration, Err: err}
	}
	logsSince, logsErr := time.ParseDuration(settings.LogsSince)
	verify, verifyErr := time.ParseDuration(settings.RestartVerifyTimeout)
	if logsErr != nil || verifyErr != nil || logsSince <= 0 || verify <= 0 ||
		settings.LogsLines <= 0 || settings.LogsLines > 100000 {
		return Manager{}, Config{}, protocol.ExitError{
			Code: protocol.ExitConfiguration, Err: errors.New("invalid systemd-base durations or logs_lines"),
		}
	}
	return Manager{
		Inspector: options.Inspector, MutationBackend: options.MutationBackend, Host: options.Host,
		Tool: protocol.Tool{
			Name: Name, Version: options.Version, Commit: options.Commit,
			BuildDate: options.BuildDate, GoVersion: runtime.Version(), Architecture: runtime.GOARCH,
		},
		Now: options.Now, Sleep: options.Sleep, VerifyTimeout: verify,
	}, settings, nil
}

func validateInvocation(invocation protocol.Invocation, command string) (string, error) {
	if !slices.Equal(invocation.CommandPath, []string{"service", command}) ||
		len(invocation.Arguments) != 1 {
		return "", protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("service command requires exactly one unit"),
		}
	}
	if err := ValidateUnit(invocation.Arguments[0]); err != nil {
		return "", protocol.ExitError{Code: protocol.ExitArguments, Err: err}
	}
	return invocation.Arguments[0], nil
}

func logRange(options map[string]any, settings Config) (time.Duration, int, error) {
	sinceText := settings.LogsSince
	if value, present := options["since"]; present {
		typed, ok := value.(string)
		if !ok {
			return 0, 0, errors.New("since must be a duration")
		}
		sinceText = typed
	}
	lines := settings.LogsLines
	if value, present := options["lines"]; present {
		switch typed := value.(type) {
		case int:
			lines = typed
		case float64:
			lines = int(typed)
		default:
			return 0, 0, errors.New("lines must be an integer")
		}
	}
	since, err := time.ParseDuration(sinceText)
	if err != nil || since <= 0 || lines <= 0 || lines > 100000 {
		return 0, 0, errors.New("invalid service log range")
	}
	return since, lines, nil
}
