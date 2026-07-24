package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ohtoe02/ohinfra-plugins/internal/protocol"
)

func TestCollectFiltersPseudoFilesystemsAndCalculatesUsage(t *testing.T) {
	mountInfo := writeMountInfo(t, `
36 25 8:1 / / rw,relatime - ext4 /dev/sda1 rw
37 36 0:5 / /proc rw,nosuid - proc proc rw
38 36 253:0 / /var/lib/docker rw,relatime - xfs /dev/mapper/vg-docker rw
`)
	collector := Collector{
		MountInfoPath: mountInfo,
		StatFS: func(string) (Stats, error) {
			return Stats{Blocks: 100, Free: 25, Available: 20, BlockSize: 1024}, nil
		},
	}

	got, err := collector.Collect("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("filesystems = %#v, want 2 real mounts", got)
	}
	if got[0].UsedPercent != 78 {
		t.Fatalf("used percent = %d, want 78", got[0].UsedPercent)
	}
}

func TestCollectSelectsContainingFilesystemForPath(t *testing.T) {
	mountInfo := writeMountInfo(t, `
36 25 8:1 / / rw,relatime - ext4 /dev/sda1 rw
38 36 253:0 / /var/lib/docker rw,relatime - xfs /dev/mapper/vg-docker rw
`)
	collector := Collector{
		MountInfoPath: mountInfo,
		StatFS: func(string) (Stats, error) {
			return Stats{Blocks: 1, Free: 1, Available: 1, BlockSize: 1}, nil
		},
		ResolvePath: func(path string) (string, error) { return filepath.Clean(path), nil },
	}

	got, err := collector.Collect("/var/lib/docker/containers")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].MountPoint != "/var/lib/docker" {
		t.Fatalf("selected filesystems = %#v", got)
	}
}

func TestStorageDefinitionPreservesDiskUsageContract(t *testing.T) {
	mountInfo := writeMountInfo(t, "36 25 8:1 / / rw,relatime - ext4 /dev/sda1 rw\n")
	definition := NewDefinition(Options{
		Version:    "1.0.0",
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		Hostname:   func() (string, error) { return "fixture-host", nil },
		Collector: Collector{
			MountInfoPath: mountInfo,
			StatFS: func(string) (Stats, error) {
				return Stats{Blocks: 100, Free: 10, Available: 10, BlockSize: 1}, nil
			},
		},
	})
	if definition.Manifest.Name != "storage-base" ||
		definition.Manifest.Description == "" ||
		len(definition.Manifest.Commands) != 1 {
		t.Fatalf("manifest = %#v", definition.Manifest)
	}

	result, err := definition.Execute(context.Background(), protocol.Invocation{
		ProtocolVersion: 1,
		CommandPath:     []string{"disk", "usage"},
		Arguments:       []string{"/"},
		Options:         map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Command != "disk usage" || result.Status != protocol.StatusCritical {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Checks) != 1 || result.Checks[0].ID != "disk:/" {
		t.Fatalf("checks = %#v", result.Checks)
	}
	if result.Host != "fixture-host" || result.Tool.Name != "storage-base" {
		t.Fatalf("metadata = %#v / %q", result.Tool, result.Host)
	}
}

func TestStorageDefinitionRejectsInvalidInvocation(t *testing.T) {
	definition := NewDefinition(Options{
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		Collector:  Collector{MountInfoPath: filepath.Join(t.TempDir(), "missing")},
	})
	for _, invocation := range []protocol.Invocation{
		{CommandPath: []string{"disk", "other"}},
		{CommandPath: []string{"disk", "usage"}, Arguments: []string{"one", "two"}},
	} {
		if _, err := definition.Execute(context.Background(), invocation); err == nil {
			t.Fatalf("accepted invocation %#v", invocation)
		}
	}
}

func TestStorageConfigValidationUsesCanonicalConfigurationExit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "storage-base.yaml")
	if err := os.WriteFile(path, []byte("disk_warning: 95\ndisk_critical: 90\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definition := NewDefinition(Options{
		ConfigPath: path,
		Collector:  Collector{MountInfoPath: filepath.Join(t.TempDir(), "missing")},
	})
	_, err := definition.Execute(context.Background(), protocol.Invocation{
		CommandPath: []string{"disk", "usage"},
	})
	var exit protocol.ExitError
	if err == nil || !errorAs(err, &exit) || exit.Code != protocol.ExitConfiguration {
		t.Fatalf("error = %#v", err)
	}
}

func errorAs(err error, target *protocol.ExitError) bool {
	for err != nil {
		if typed, ok := err.(protocol.ExitError); ok {
			*target = typed
			return true
		}
		type unwrapper interface{ Unwrap() error }
		next, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = next.Unwrap()
	}
	return false
}

func writeMountInfo(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
