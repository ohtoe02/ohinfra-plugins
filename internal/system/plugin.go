package system

import (
	"context"
	"errors"
	"runtime"
	"slices"
	"time"

	pluginconfig "github.com/ohtoe02/ohtools-plugins/internal/config"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/storage"
)

const (
	Name        = "system-base"
	Description = "Operating system information and health diagnostics for ohtools."
	ConfigPath  = "/etc/ohtools/plugins/system-base.yaml"
)

type Config struct {
	DiskWarning    int    `yaml:"disk_warning"`
	DiskCritical   int    `yaml:"disk_critical"`
	MemoryWarning  int    `yaml:"memory_warning"`
	MemoryCritical int    `yaml:"memory_critical"`
	OOMWindow      string `yaml:"oom_window"`
}

func DefaultConfig() Config {
	return Config{
		DiskWarning: 80, DiskCritical: 90,
		MemoryWarning: 85, MemoryCritical: 95, OOMWindow: "24h",
	}
}

type Options struct {
	Version    string
	Commit     string
	BuildDate  string
	ConfigPath string
	Collector  Collector
	Storage    storage.Collector
}

func NewDefinition(options Options) protocol.Definition {
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.ConfigPath == "" {
		options.ConfigPath = ConfigPath
	}
	if options.Collector.Root == "" {
		options.Collector = DefaultCollector()
	}
	if options.Storage.MountInfoPath == "" {
		options.Storage = storage.DefaultCollector()
	}
	commands := []protocol.Command{
		{
			Path: []string{"system", "info"}, Use: "info",
			Short: "Show operating system information", Category: protocol.CategoryDiagnostic,
			Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
		},
		{
			Path: []string{"system", "health"}, Use: "health",
			Short: "Run operating system health checks", Category: protocol.CategoryDiagnostic,
			Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
		},
	}
	return protocol.Definition{
		Manifest: protocol.Manifest{
			ProtocolVersion: protocol.ProtocolVersion, Name: Name,
			Version: options.Version, Description: Description, Commands: commands,
		},
		Execute: func(ctx context.Context, invocation protocol.Invocation) (protocol.Result, error) {
			return execute(ctx, invocation, options)
		},
	}
}

func execute(ctx context.Context, invocation protocol.Invocation, options Options) (protocol.Result, error) {
	if len(invocation.Arguments) != 0 {
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("system commands accept no arguments"),
		}
	}
	tool := protocol.Tool{
		Name: Name, Version: options.Version, Commit: options.Commit,
		BuildDate: options.BuildDate, GoVersion: runtime.Version(), Architecture: runtime.GOARCH,
	}
	switch {
	case slices.Equal(invocation.CommandPath, []string{"system", "info"}):
		return options.Collector.InfoResult(ctx, tool), nil
	case slices.Equal(invocation.CommandPath, []string{"system", "health"}):
		settings, err := pluginconfig.Load(options.ConfigPath, DefaultConfig())
		if err != nil {
			return protocol.Result{}, protocol.ExitError{Code: protocol.ExitConfiguration, Err: err}
		}
		window, err := time.ParseDuration(settings.OOMWindow)
		if err != nil || window <= 0 ||
			!validThresholds(settings.DiskWarning, settings.DiskCritical) ||
			!validThresholds(settings.MemoryWarning, settings.MemoryCritical) {
			return protocol.Result{}, protocol.ExitError{
				Code: protocol.ExitConfiguration, Err: errors.New("invalid system-base thresholds or oom_window"),
			}
		}
		filesystems, err := options.Storage.Collect("")
		if err != nil {
			filesystems = []storage.Filesystem{{Error: err.Error()}}
		}
		usage := make([]FilesystemUsage, 0, len(filesystems))
		for _, filesystem := range filesystems {
			usage = append(usage, FilesystemUsage{
				MountPoint: filesystem.MountPoint, UsedPercent: filesystem.UsedPercent, Error: filesystem.Error,
			})
		}
		return options.Collector.HealthResult(ctx, HealthInput{
			Tool: tool, Filesystems: usage,
			Thresholds: HealthThresholds{
				DiskWarning: settings.DiskWarning, DiskCritical: settings.DiskCritical,
				MemoryWarning: settings.MemoryWarning, MemoryCritical: settings.MemoryCritical,
				OOMWindow: window,
			},
		}), nil
	default:
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("unsupported system-base command"),
		}
	}
}

func validThresholds(warning, critical int) bool {
	return warning >= 0 && critical <= 100 && warning < critical
}
