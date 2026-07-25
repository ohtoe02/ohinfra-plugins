package system

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/storage"
)

func TestCollectParsesLinuxSystemFiles(t *testing.T) {
	root := linuxFixture(t, "debian", "13", 4096, 256)
	collector := Collector{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch spec.Program {
			case "timedatectl":
				return execx.Output{Stdout: []byte("yes\n")}, nil
			case "systemd-detect-virt":
				return execx.Output{Stdout: []byte("podman\n")}, nil
			}
			return execx.Output{}, execx.ErrNotFound
		}),
		Hostname: func() (string, error) { return "server01", nil },
	}
	got, failures := collector.Collect(context.Background())
	if len(failures) != 0 {
		t.Fatalf("failures = %#v", failures)
	}
	if got.OS.ID != "debian" || got.OS.Version != "13" || got.Kernel != "6.12-test" {
		t.Fatalf("snapshot = %#v", got)
	}
	if got.Memory.TotalBytes != 4096*1024 || got.Memory.AvailableBytes != 256*1024 {
		t.Fatalf("memory = %#v", got.Memory)
	}
	if got.TimeSynchronized == nil || !*got.TimeSynchronized || got.Virtualization != "podman" {
		t.Fatalf("time/virtualization = %#v / %q", got.TimeSynchronized, got.Virtualization)
	}
}

func TestHealthResultClassifiesCriticalAndWarningChecks(t *testing.T) {
	root := linuxFixture(t, "debian", "13", 1000, 40)
	collector := Collector{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch spec.Program {
			case "timedatectl":
				return execx.Output{Stdout: []byte("no\n")}, nil
			case "systemctl":
				return execx.Output{Stdout: []byte("nginx.service loaded failed failed nginx\n")}, nil
			case "journalctl":
				return execx.Output{Stdout: []byte("kernel: Out of memory: Killed process 42\n")}, nil
			default:
				return execx.Output{}, execx.ErrNotFound
			}
		}),
		Hostname: func() (string, error) { return "server01", nil },
		Now:      func() time.Time { return time.Unix(100, 0).UTC() },
	}
	got := collector.HealthResult(context.Background(), HealthInput{
		Tool:        protocol.Tool{Name: "system-base", Version: "1.0.0"},
		Filesystems: []FilesystemUsage{{MountPoint: "/", UsedPercent: 91}},
		Thresholds:  DefaultHealthThresholds(),
	})
	if got.Status != protocol.StatusCritical {
		t.Fatalf("status = %s, checks=%#v errors=%#v", got.Status, got.Checks, got.Errors)
	}
	for _, id := range []string{"memory", "disk:/", "failed-units", "oom", "time-sync"} {
		if !hasCheck(got.Checks, id) {
			t.Fatalf("missing check %q in %#v", id, got.Checks)
		}
	}
}

func TestSystemDefinitionProvidesInfoAndHealthWithoutStoragePlugin(t *testing.T) {
	root := linuxFixture(t, "debian", "12", 1000, 900)
	mountInfo := filepath.Join(t.TempDir(), "mountinfo")
	if err := os.WriteFile(mountInfo, []byte("36 25 8:1 / / rw - ext4 /dev/sda1 rw\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	definition := NewDefinition(Options{
		Version:    "1.0.0",
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		Collector: Collector{
			Root: root,
			Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
				return execx.Output{}, execx.ErrNotFound
			}),
			Hostname: func() (string, error) { return "server01", nil },
		},
		Storage: storage.Collector{
			MountInfoPath: mountInfo,
			StatFS: func(string) (storage.Stats, error) {
				return storage.Stats{Blocks: 100, Free: 20, Available: 20, BlockSize: 1}, nil
			},
		},
	})
	if definition.Manifest.Name != "system-base" ||
		definition.Manifest.Description == "" ||
		len(definition.Manifest.Commands) != 2 {
		t.Fatalf("manifest = %#v", definition.Manifest)
	}
	for _, path := range [][]string{{"system", "info"}, {"system", "health"}} {
		got, err := definition.Execute(context.Background(), protocol.Invocation{
			ProtocolVersion: 1, CommandPath: path, Arguments: []string{}, Options: map[string]any{},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got.Command != path[0]+" "+path[1] || got.Tool.Name != "system-base" {
			t.Fatalf("result = %#v", got)
		}
	}
}

func linuxFixture(t *testing.T, osID, osVersion string, totalKB, availableKB int) string {
	t.Helper()
	root := t.TempDir()
	writeRootFile(t, root, "etc/os-release", "ID="+osID+"\nVERSION_ID=\""+osVersion+"\"\nPRETTY_NAME=\"Debian Test\"\n")
	writeRootFile(t, root, "proc/sys/kernel/osrelease", "6.12-test\n")
	writeRootFile(t, root, "proc/uptime", "1234.50 100.00\n")
	writeRootFile(t, root, "proc/cpuinfo", "processor: 0\nmodel name: Test CPU\nprocessor: 1\n")
	writeRootFile(t, root, "proc/meminfo",
		"MemTotal: "+itoa(totalKB)+" kB\nMemAvailable: "+itoa(availableKB)+" kB\nSwapTotal: 512 kB\nSwapFree: 256 kB\n")
	writeRootFile(t, root, "proc/loadavg", "0.10 0.20 0.30 1/10 42\n")
	writeRootFile(t, root, "sys/class/dmi/id/product_name", "KVM\n")
	writeRootFile(t, root, "etc/timezone", "Asia/Yekaterinburg\n")
	writeRootFile(t, root, ".dockerenv", "")
	return root
}

func writeRootFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hasCheck(checks []protocol.Check, id string) bool {
	for _, check := range checks {
		if check.ID == id {
			return true
		}
	}
	return false
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	var output []byte
	for value > 0 {
		output = append([]byte{byte('0' + value%10)}, output...)
		value /= 10
	}
	return string(output)
}
