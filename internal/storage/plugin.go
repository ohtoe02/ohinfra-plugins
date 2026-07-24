package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"slices"
	"time"

	pluginconfig "github.com/ohtoe02/ohinfra-plugins/internal/config"
	"github.com/ohtoe02/ohinfra-plugins/internal/protocol"
)

const (
	Name        = "storage-base"
	Description = "Filesystem capacity and usage diagnostics for ohinfra."
	ConfigPath  = "/etc/ohinfra/plugins/storage-base.yaml"
)

type Config struct {
	DiskWarning  int `yaml:"disk_warning"`
	DiskCritical int `yaml:"disk_critical"`
}

func DefaultConfig() Config {
	return Config{DiskWarning: 80, DiskCritical: 90}
}

type Options struct {
	Version    string
	Commit     string
	BuildDate  string
	ConfigPath string
	Hostname   func() (string, error)
	Collector  Collector
}

func NewDefinition(options Options) protocol.Definition {
	if options.Version == "" {
		options.Version = "dev"
	}
	if options.ConfigPath == "" {
		options.ConfigPath = ConfigPath
	}
	if options.Hostname == nil {
		options.Hostname = os.Hostname
	}
	if options.Collector.MountInfoPath == "" {
		options.Collector = DefaultCollector()
	}
	manifest := protocol.Manifest{
		ProtocolVersion: protocol.ProtocolVersion,
		Name:            Name,
		Version:         options.Version,
		Description:     Description,
		Commands: []protocol.Command{{
			Path:     []string{"disk", "usage"},
			Use:      "usage [path]",
			Short:    "Show filesystem usage",
			Category: protocol.CategoryDiagnostic,
			Arguments: []protocol.Argument{{
				Name: "path", Description: "Path used to select a filesystem",
			}},
			Flags: []protocol.Flag{},
		}},
	}
	return protocol.Definition{
		Manifest: manifest,
		Execute: func(ctx context.Context, invocation protocol.Invocation) (protocol.Result, error) {
			return execute(ctx, invocation, options)
		},
	}
}

func execute(ctx context.Context, invocation protocol.Invocation, options Options) (protocol.Result, error) {
	if !slices.Equal(invocation.CommandPath, []string{"disk", "usage"}) ||
		len(invocation.Arguments) > 1 {
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments, Err: errors.New("disk usage accepts at most one path"),
		}
	}
	select {
	case <-ctx.Done():
		return protocol.Result{}, ctx.Err()
	default:
	}
	settings, err := pluginconfig.Load(options.ConfigPath, DefaultConfig())
	if err != nil {
		return protocol.Result{}, protocol.ExitError{Code: protocol.ExitConfiguration, Err: err}
	}
	if settings.DiskWarning < 0 || settings.DiskCritical > 100 ||
		settings.DiskWarning >= settings.DiskCritical {
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitConfiguration,
			Err:  errors.New("disk_warning must be lower than disk_critical and both within 0..100"),
		}
	}
	path := ""
	if len(invocation.Arguments) == 1 {
		path = invocation.Arguments[0]
	}
	started := time.Now()
	filesystems, err := options.Collector.Collect(path)
	if err != nil {
		return protocol.Result{}, fmt.Errorf("collect filesystem usage: %w", err)
	}
	status := protocol.StatusPass
	checks := make([]protocol.Check, 0, len(filesystems))
	failures := []protocol.StructuredError{}
	for _, filesystem := range filesystems {
		checkStatus := protocol.StatusPass
		summary := fmt.Sprintf("%s usage is %d%%", filesystem.MountPoint, filesystem.UsedPercent)
		if filesystem.Error != "" {
			checkStatus = protocol.StatusSkipped
			summary = filesystem.Error
			failures = append(failures, protocol.StructuredError{
				Kind: protocol.ErrorGeneral, Code: "statfs_failed",
				Message: filesystem.MountPoint + ": " + filesystem.Error,
			})
			if status != protocol.StatusCritical {
				status = protocol.StatusPartial
			}
		} else if filesystem.UsedPercent >= settings.DiskCritical {
			checkStatus = protocol.StatusCritical
			status = protocol.StatusCritical
		} else if filesystem.UsedPercent >= settings.DiskWarning {
			checkStatus = protocol.StatusWarning
			if status == protocol.StatusPass {
				status = protocol.StatusWarning
			}
		}
		checks = append(checks, protocol.Check{
			ID: "disk:" + filesystem.MountPoint, Status: checkStatus, Summary: summary,
		})
	}
	host, _ := options.Hostname()
	return protocol.Normalize(protocol.Result{
		Command: "disk usage", Status: status, Timestamp: time.Now().Format(time.RFC3339Nano),
		DurationMS: time.Since(started).Milliseconds(), Host: host,
		Tool: protocol.Tool{
			Name: Name, Version: options.Version, Commit: options.Commit,
			BuildDate: options.BuildDate, GoVersion: runtime.Version(), Architecture: runtime.GOARCH,
		},
		Checks: checks, Data: map[string]any{"filesystems": filesystems}, Errors: failures,
	}), nil
}
