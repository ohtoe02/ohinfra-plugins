package serversetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestSystemBackendStagesActivatesAndVerifiesOwnedFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	var calls []execx.Spec
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			if spec.Program == "sysctl" && containsArgument(spec.Arguments, "-n") {
				return execx.Output{Stdout: []byte("2\n1\n1\n1\n")}, nil
			}
			return execx.Output{}, nil
		}),
	}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemSysctl},
	}
	observation, err := backend.Observe(context.Background(), ItemSysctl, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("missing managed sysctl file was reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemSysctl, profile); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), ItemSysctl, profile); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "kernel.kptr_restrict = 2") {
		t.Fatalf("managed content = %q", content)
	}
	if len(calls) != 3 || calls[0].Program != "sysctl" ||
		!reflect.DeepEqual(calls[0].Arguments, []string{"--system"}) {
		t.Fatalf("runner calls = %#v", calls)
	}
}

func TestManagedItemsRequirePersistentAndEffectiveConvergence(t *testing.T) {
	t.Parallel()

	for _, item := range []Item{
		ItemCronPermissions,
		ItemFail2Ban,
		ItemTimeSync,
		ItemLogging,
		ItemAuditd,
		ItemSysctl,
		ItemZabbix,
	} {
		item := item
		t.Run(string(item), func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			backend := SystemBackend{
				Root: root,
				Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
					if spec.Program == "sysctl" {
						return execx.Output{Stdout: []byte("0\n0\n0\n0\n")}, nil
					}
					if spec.Program == "systemctl" &&
						containsArgument(spec.Arguments, "is-active") {
						return execx.Output{ExitCode: 3}, nil
					}
					return execx.Output{ExitCode: 1}, nil
				}),
			}
			config := DefaultConfig()
			config.Zabbix = &ZabbixConfig{
				Enabled: true, Server: "127.0.0.1", Hostname: "fixture",
			}
			profile := Profile{
				Platform: Platform{ID: "debian", Version: "12"},
				Config:   config,
				Items:    []Item{ItemPackages, item},
			}
			managed, err := backend.desiredFile(item, profile)
			if err != nil {
				t.Fatal(err)
			}
			target := backend.path(managed.Path)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, managed.Content, managed.Mode); err != nil {
				t.Fatal(err)
			}
			if item == ItemCronPermissions {
				unsafe := filepath.Join(root, "etc", "cron.d", "foreign")
				if err := os.WriteFile(unsafe, []byte("* * * * * root true\n"), 0o666); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(unsafe, 0o666); err != nil {
					t.Fatal(err)
				}
			}
			observation, err := backend.Observe(context.Background(), item, profile)
			if err != nil {
				t.Fatal(err)
			}
			if observation.Converged {
				t.Fatal("persistent file hid effective-state drift")
			}
		})
	}
}

func TestSystemBackendRestoresOwnedFileWhenActivationFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# previous\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{ExitCode: 1, Stderr: []byte("rejected")}, nil
		}),
	}
	err := backend.Apply(context.Background(), ItemSysctl, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemSysctl},
	})
	if err == nil {
		t.Fatal("activation failure was ignored")
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "# previous\n" {
		t.Fatalf("rollback content = %q", content)
	}
}

func TestSystemBackendRollsBackWhenPostVerificationFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := []byte("# previous\n")
	if err := os.WriteFile(target, previous, 0o644); err != nil {
		t.Fatal(err)
	}
	activationCalls := 0
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program == "sysctl" {
				activationCalls++
				if activationCalls == 1 {
					if err := os.WriteFile(target, []byte("# rejected effective state\n"), 0o644); err != nil {
						return execx.Output{}, err
					}
				}
			}
			return execx.Output{}, nil
		}),
	}
	err := backend.Apply(context.Background(), ItemSysctl, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemSysctl},
	})
	if err == nil {
		t.Fatal("post-verification drift was ignored")
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(content, previous) {
		t.Fatalf("post-verification rollback content = %q", content)
	}
	if activationCalls != 2 {
		t.Fatalf("activation calls = %d, want apply and rollback reactivation", activationCalls)
	}
}

func TestManagedActivationRollbackUsesIndependentBoundedContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := []byte("# previous\n")
	if err := os.WriteFile(target, previous, 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	activationCalls := 0
	cleanupSawCanceledContext := false
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(runContext context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program != "sysctl" || !containsArgument(spec.Arguments, "--system") {
				return execx.Output{}, errors.New("unexpected command")
			}
			activationCalls++
			if activationCalls == 1 {
				cancel()
				return execx.Output{}, context.Canceled
			}
			cleanupSawCanceledContext = runContext.Err() != nil
			return execx.Output{}, nil
		}),
	}
	err := backend.Apply(ctx, ItemSysctl, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemSysctl},
	})
	if err == nil {
		t.Fatal("canceled activation was accepted")
	}
	if activationCalls != 2 {
		t.Fatalf("activation calls = %d, want failed apply and rollback reactivation", activationCalls)
	}
	if cleanupSawCanceledContext {
		t.Fatal("rollback activation inherited the canceled operation context")
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(content, previous) {
		t.Fatalf("rollback content = %q", content)
	}
}

func TestSystemBackendFailsClosedOnStaleManagedTransactionArtifact(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{Root: root, Runner: successfulRunner()}
	managed, err := backend.desiredFile(ItemSysctl, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, managed.Content, managed.Mode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".rollback", []byte("# previous\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		target+".transaction-v1.json",
		[]byte("{\"schema_version\":\"1\",\"phase\":\"unknown\",\"had_target\":true}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	_, err = backend.Observe(context.Background(), ItemSysctl, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
	})
	if err == nil {
		t.Fatal("stale managed transaction artifact was ignored")
	}
}

func TestSystemBackendRecoversInterruptedManagedTransactionsBeforeRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		phase     string
		hadTarget bool
		target    bool
		stage     bool
		rollback  bool
	}{
		{name: "staged", phase: "staged", hadTarget: true, target: true, stage: true},
		{name: "backup rename", phase: "backed_up", hadTarget: true, stage: true, rollback: true},
		{name: "activated", phase: "activated", hadTarget: true, target: true, rollback: true},
		{name: "postverify", phase: "verified", hadTarget: true, target: true, rollback: true},
		{name: "new target activated", phase: "activated", target: true},
		{name: "rollback before restore", phase: "rolling_back", hadTarget: true, target: true, rollback: true},
		{name: "rollback after restore", phase: "rolling_back", hadTarget: true, target: true},
		{name: "new target rolling back", phase: "rolling_back", target: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			backend := SystemBackend{Root: root, Runner: successfulRunner()}
			profile := Profile{
				Platform: Platform{ID: "debian", Version: "12"},
				Config:   DefaultConfig(),
				Items:    []Item{ItemPackages, ItemSysctl},
			}
			managed, err := backend.desiredFile(ItemSysctl, profile)
			if err != nil {
				t.Fatal(err)
			}
			target := backend.path(managed.Path)
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if test.target {
				if err := os.WriteFile(target, []byte("# interrupted target\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if test.stage {
				if err := os.WriteFile(target+".stage", managed.Content, managed.Mode); err != nil {
					t.Fatal(err)
				}
			}
			if test.rollback {
				if err := os.WriteFile(target+".rollback", []byte("# previous\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			journal := fmt.Sprintf(
				"{\"schema_version\":\"1\",\"phase\":%q,\"had_target\":%t}\n",
				test.phase,
				test.hadTarget,
			)
			if err := os.WriteFile(
				target+".transaction-v1.json",
				[]byte(journal),
				0o600,
			); err != nil {
				t.Fatal(err)
			}

			if err := backend.Apply(context.Background(), ItemSysctl, profile); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(content, managed.Content) {
				t.Fatalf("recovered content = %q", content)
			}
			for _, suffix := range []string{
				".stage", ".rollback", ".transaction-v1.json",
			} {
				if _, err := os.Lstat(target + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("recovery left %s: %v", suffix, err)
				}
			}
		})
	}
}

func TestSystemBackendObserveRepresentsRecoverableManagedTransactionAsDrift(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{Root: root, Runner: successfulRunner()}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemSysctl},
	}
	managed, err := backend.desiredFile(ItemSysctl, profile)
	if err != nil {
		t.Fatal(err)
	}
	target := backend.path(managed.Path)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, managed.Content, managed.Mode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".rollback", []byte("# previous\n"), managed.Mode); err != nil {
		t.Fatal(err)
	}
	journalPath := target + ".transaction-v1.json"
	if err := os.WriteFile(
		journalPath,
		[]byte("{\"schema_version\":\"1\",\"phase\":\"activated\",\"had_target\":true}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	observation, err := backend.Observe(context.Background(), ItemSysctl, profile)
	if err != nil {
		t.Fatalf("recoverable transaction blocked planning: %v", err)
	}
	if observation.Converged {
		t.Fatal("recoverable transaction was reported as converged")
	}
	if _, err := os.Lstat(journalPath); err != nil {
		t.Fatalf("read-only observation mutated recovery journal: %v", err)
	}
}

func TestManagedRecoveryValidationRejectsImpossibleArtifactCombinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		journal  managedTransactionJournal
		target   bool
		stage    bool
		rollback bool
	}{
		{
			name: "staged old target without stage",
			journal: managedTransactionJournal{
				Phase: "staged", HadTarget: true,
			},
			target: true,
		},
		{
			name: "backed up transaction without stage or activated target",
			journal: managedTransactionJournal{
				Phase: "backed_up", HadTarget: true,
			},
			rollback: true,
		},
		{
			name: "activated transaction retains stage",
			journal: managedTransactionJournal{
				Phase: "activated", HadTarget: false,
			},
			target: true,
			stage:  true,
		},
		{
			name: "rolling back transaction retains stage",
			journal: managedTransactionJournal{
				Phase: "rolling_back", HadTarget: false,
			},
			stage: true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := validateManagedRecoveryState(
				test.journal,
				test.target,
				test.stage,
				test.rollback,
			); err == nil {
				t.Fatal("impossible managed recovery state was accepted")
			}
		})
	}
}

func TestSystemBackendObserveRepresentsRecoverableSSHIncludeAsDrift(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{Root: root}
	target := backend.path("/etc/ssh/sshd_config")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(sshIncludeDirective+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup := target + ".ohtools-include.rollback"
	if err := os.WriteFile(backup, []byte("# original\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	present, err := backend.sshIncludePresent()
	if err != nil {
		t.Fatalf("recoverable SSH transaction blocked planning: %v", err)
	}
	if present {
		t.Fatal("interrupted SSH include transaction was reported as converged")
	}
	if _, err := os.Lstat(backup); err != nil {
		t.Fatalf("read-only SSH observation mutated rollback artifact: %v", err)
	}
}

func TestSSHIncludeRecoveryRejectsImpossibleArtifactCombination(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{Root: root}
	target := backend.path("/etc/ssh/sshd_config")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		target+".ohtools-include.stage",
		[]byte(sshIncludeDirective+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.sshIncludeRecoveryPending(target); err == nil {
		t.Fatal("orphaned SSH include stage without original or rollback was accepted")
	}
}

func TestManagedRecoveryRemovesUnverifiedNewTargetBeforeActivatedPhase(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{Root: root}
	target := filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# unverified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		target+".transaction-v1.json",
		[]byte("{\"schema_version\":\"1\",\"phase\":\"staged\",\"had_target\":false}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if err := backend.recoverManagedTransaction(
		context.Background(),
		target,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unverified target survived recovery: %v", err)
	}
}

func TestManagedRecoveryReactivatesAfterRollingBackEscapedEffectiveState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemSysctl},
	}
	managed, err := backend.desiredFile(ItemSysctl, profile)
	if err != nil {
		t.Fatal(err)
	}
	backend.Root = root
	target := backend.path(managed.Path)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, managed.Content, managed.Mode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".rollback", []byte("# previous\n"), managed.Mode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		target+".transaction-v1.json",
		[]byte("{\"schema_version\":\"1\",\"phase\":\"activated\",\"had_target\":true}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	activationCalls := 0
	effective := false
	backend.Runner = execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch {
		case spec.Program == "sysctl" && containsArgument(spec.Arguments, "--system"):
			activationCalls++
			effective = true
			return execx.Output{}, nil
		case spec.Program == "sysctl" && containsArgument(spec.Arguments, "-n"):
			if effective {
				return execx.Output{Stdout: []byte("2\n1\n1\n1\n")}, nil
			}
			return execx.Output{Stdout: []byte("0\n0\n0\n0\n")}, nil
		default:
			return execx.Output{}, errors.New("unexpected command")
		}
	})

	if err := backend.Apply(context.Background(), ItemSysctl, profile); err != nil {
		t.Fatal(err)
	}
	if activationCalls != 2 {
		t.Fatalf(
			"activation calls = %d, want recovered and desired config activation",
			activationCalls,
		)
	}
}

func TestActivatedRecoveryRestoresSysctlRuntimeBeforeReapplyingDesiredState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{Root: root}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemSysctl},
	}
	managed, err := backend.desiredFile(ItemSysctl, profile)
	if err != nil {
		t.Fatal(err)
	}
	target := backend.path(managed.Path)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := []byte("kernel.kptr_restrict = 1\n")
	writeActivatedManagedTransaction(t, target, managed.Content, previous)
	var activatedContents [][]byte
	backend.Runner = execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch {
		case spec.Program == "sysctl" && containsArgument(spec.Arguments, "--system"):
			content, err := os.ReadFile(target)
			if err != nil {
				return execx.Output{}, err
			}
			activatedContents = append(activatedContents, append([]byte(nil), content...))
			return execx.Output{}, nil
		case spec.Program == "sysctl" && containsArgument(spec.Arguments, "-n"):
			return execx.Output{Stdout: []byte("2\n1\n1\n1\n")}, nil
		default:
			return execx.Output{}, errors.New("unexpected command")
		}
	})
	if err := backend.Apply(context.Background(), ItemSysctl, profile); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(activatedContents, [][]byte{previous, managed.Content}) {
		t.Fatalf("activated contents = %q", activatedContents)
	}
}

func TestActivatedRecoveryRestoresServiceRuntimeBeforeReapplyingDesiredState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{Root: root}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemFail2Ban},
	}
	managed, err := backend.desiredFile(ItemFail2Ban, profile)
	if err != nil {
		t.Fatal(err)
	}
	target := backend.path(managed.Path)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := []byte("[sshd]\nenabled = false\n")
	writeActivatedManagedTransaction(t, target, managed.Content, previous)
	var reloadContents [][]byte
	backend.Runner = execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch {
		case spec.Program == "systemctl" &&
			(containsArgument(spec.Arguments, "is-enabled") ||
				containsArgument(spec.Arguments, "is-active")):
			return execx.Output{}, nil
		case spec.Program == "systemctl" && containsArgument(spec.Arguments, "reload"):
			content, err := os.ReadFile(target)
			if err != nil {
				return execx.Output{}, err
			}
			reloadContents = append(reloadContents, append([]byte(nil), content...))
			return execx.Output{}, nil
		case spec.Program == "fail2ban-client":
			return execx.Output{}, nil
		default:
			return execx.Output{}, errors.New("unexpected command")
		}
	})
	if err := backend.Apply(context.Background(), ItemFail2Ban, profile); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloadContents, [][]byte{previous, managed.Content}) {
		t.Fatalf("reloaded contents = %q", reloadContents)
	}
}

func TestActivatedRecoveryRestoresFirewallRuntimeBeforeReapplyingDesiredState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	config := DefaultConfig()
	config.ManageFirewall = true
	config.SSHPort = 2222
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemFirewall},
	}
	backend := SystemBackend{Root: root}
	files := mustFirewallFiles(t, backend, profile)
	target := backend.path(files[0].Path)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := []byte(
		"table inet ohtools_server_setup { chain input { type filter hook input priority 0; policy accept; tcp dport 22 accept; } }\n",
	)
	writeActivatedManagedTransaction(t, target, files[0].Content, previous)
	unitTarget := backend.path(files[1].Path)
	if err := os.MkdirAll(filepath.Dir(unitTarget), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unitTarget, files[1].Content, files[1].Mode); err != nil {
		t.Fatal(err)
	}
	activeRules := append([]byte(nil), files[0].Content...)
	var directActivations [][]byte
	backend.Runner = execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch {
		case spec.Program == "systemctl" &&
			(containsArgument(spec.Arguments, "is-enabled") ||
				containsArgument(spec.Arguments, "is-active")):
			return execx.Output{}, nil
		case spec.Program == "systemctl" && containsArgument(spec.Arguments, "restart"):
			content, err := os.ReadFile(target)
			if err != nil {
				return execx.Output{}, err
			}
			activeRules = append([]byte(nil), content...)
			return execx.Output{}, nil
		case spec.Program == "systemctl":
			return execx.Output{}, nil
		case spec.Program == "nft" && containsArgument(spec.Arguments, "list"):
			return execx.Output{Stdout: append([]byte(nil), activeRules...)}, nil
		case spec.Program == "nft" && containsArgument(spec.Arguments, "-c"):
			return execx.Output{}, nil
		case spec.Program == "nft" && containsArgument(spec.Arguments, "-f") &&
			containsArgument(spec.Arguments, target):
			content, err := os.ReadFile(target)
			if err != nil {
				return execx.Output{}, err
			}
			directActivations = append(directActivations, append([]byte(nil), content...))
			activeRules = append([]byte(nil), content...)
			return execx.Output{}, nil
		default:
			return execx.Output{}, nil
		}
	})
	if err := backend.Apply(context.Background(), ItemFirewall, profile); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(directActivations, [][]byte{previous}) {
		t.Fatalf("direct firewall activations = %q", directActivations)
	}
}

func TestActivatedRecoveryFailurePreservesJournal(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{Root: root}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemSysctl},
	}
	managed, err := backend.desiredFile(ItemSysctl, profile)
	if err != nil {
		t.Fatal(err)
	}
	target := backend.path(managed.Path)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	previous := []byte("kernel.kptr_restrict = 1\n")
	writeActivatedManagedTransaction(t, target, managed.Content, previous)
	backend.Runner = execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		if spec.Program == "sysctl" && containsArgument(spec.Arguments, "--system") {
			content, err := os.ReadFile(target)
			if err != nil {
				return execx.Output{}, err
			}
			if bytes.Equal(content, previous) {
				return execx.Output{
					ExitCode: 1,
					Stderr:   []byte("restore runtime failed"),
				}, nil
			}
		}
		return execx.Output{}, nil
	})
	err = backend.Apply(context.Background(), ItemSysctl, profile)
	if err == nil || !strings.Contains(err.Error(), "restore recovered effective state") ||
		!strings.Contains(err.Error(), "restore runtime failed") {
		t.Fatalf("recovery error = %v", err)
	}
	journal, readErr := backend.readManagedTransactionJournal(
		target + ".transaction-v1.json",
	)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if journal.Phase != "rolling_back" {
		t.Fatalf("journal phase = %q", journal.Phase)
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(content, previous) {
		t.Fatalf("recovered content = %q", content)
	}
}

func writeActivatedManagedTransaction(
	t *testing.T,
	target string,
	active []byte,
	previous []byte,
) {
	t.Helper()
	if err := os.WriteFile(target, active, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target+".rollback", previous, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		target+".transaction-v1.json",
		[]byte("{\"schema_version\":\"1\",\"phase\":\"activated\",\"had_target\":true}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}

func TestSystemBackendRefusesSymlinkedManagedFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	backend := SystemBackend{Root: root, Runner: successfulRunner()}
	_, err := backend.Observe(context.Background(), ItemSysctl, Profile{
		Platform: Platform{ID: "debian", Version: "12"}, Config: DefaultConfig(),
	})
	if err == nil {
		t.Fatal("symlinked managed file was accepted")
	}
}

func TestSystemBackendUsesDirectPackageArgv(t *testing.T) {
	t.Parallel()

	var calls []execx.Spec
	installed := map[string]bool{}
	backend := SystemBackend{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			if spec.Program == "dpkg-query" {
				var output strings.Builder
				for name := range installed {
					fmt.Fprintf(&output, "%s\tinstall ok installed\n", name)
				}
				return execx.Output{ExitCode: 1, Stdout: []byte(output.String())}, nil
			}
			if spec.Program == "apt-get" && containsArgument(spec.Arguments, "install") {
				separator := slices.Index(spec.Arguments, "--")
				for _, name := range spec.Arguments[separator+1:] {
					installed[name] = true
				}
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Packages = []string{"curl"}
	config.Administrators = []Administrator{{Name: "operator"}}
	profile := Profile{
		Platform: Platform{ID: "ubuntu", Version: "22.04"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemSSH},
	}
	if err := backend.Apply(context.Background(), ItemPackages, profile); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[1].Program != "apt-get" || !reflect.DeepEqual(calls[1].Arguments, []string{"update"}) {
		t.Fatalf("apt update = %#v", calls[1])
	}
	install := calls[2]
	if install.Program != "apt-get" || !reflect.DeepEqual(install.Arguments,
		[]string{"install", "-y", "--no-install-recommends", "--no-upgrade", "--", "curl", "openssh-server", "sudo"}) {
		t.Fatalf("apt install = %#v", install)
	}
	if len(install.Environment) == 0 || install.Environment["DEBIAN_FRONTEND"] != "noninteractive" {
		t.Fatalf("apt environment = %#v", install.Environment)
	}
}

func TestSystemBackendInstallsOnlyMissingPackagesWithoutUpgradingInstalledOnes(t *testing.T) {
	t.Parallel()

	installed := map[string]bool{"curl": true}
	var install []string
	backend := SystemBackend{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch spec.Program {
			case "dpkg-query":
				var output strings.Builder
				for name := range installed {
					if installed[name] {
						fmt.Fprintf(&output, "%s\tinstall ok installed\n", name)
					}
				}
				return execx.Output{ExitCode: 1, Stdout: []byte(output.String())}, nil
			case "apt-get":
				if containsArgument(spec.Arguments, "install") {
					install = append([]string(nil), spec.Arguments...)
					installed["vim"] = true
				}
				return execx.Output{}, nil
			default:
				return execx.Output{}, fmt.Errorf("unexpected program %s", spec.Program)
			}
		}),
	}
	config := DefaultConfig()
	config.Packages = []string{"curl", "vim"}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages},
	}

	if err := backend.Apply(context.Background(), ItemPackages, profile); err != nil {
		t.Fatal(err)
	}
	if !containsArgument(install, "--no-upgrade") {
		t.Fatalf("package install may upgrade existing packages: %#v", install)
	}
	separator := slices.Index(install, "--")
	if separator < 0 || !reflect.DeepEqual(install[separator+1:], []string{"vim"}) {
		t.Fatalf("package install targets = %#v, want only vim", install)
	}
}

func TestManagerApplyRejectsNewlyMissingPackageAfterApprovedPlan(t *testing.T) {
	t.Parallel()

	installed := map[string]bool{"curl": true}
	installCalls := 0
	backend := SystemBackend{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch {
			case spec.Program == "dpkg-query":
				var output strings.Builder
				for name, present := range installed {
					if present {
						fmt.Fprintf(&output, "%s\tinstall ok installed\n", name)
					}
				}
				return execx.Output{ExitCode: 1, Stdout: []byte(output.String())}, nil
			case spec.Program == "apt-get" && containsArgument(spec.Arguments, "install"):
				installCalls++
				for _, name := range []string{"curl", "vim"} {
					if containsArgument(spec.Arguments, name) {
						installed[name] = true
					}
				}
				return execx.Output{}, nil
			case spec.Program == "apt-get":
				return execx.Output{}, nil
			default:
				return execx.Output{}, errors.New("unexpected command")
			}
		}),
	}
	config := DefaultConfig()
	config.Packages = []string{"curl", "vim"}
	manager := Manager{
		Backend:  backend,
		Config:   config,
		Platform: Platform{ID: "debian", Version: "12"},
		Host:     "fixture",
		Tool:     protocol.Tool{Name: Name, Version: "1.0.0"},
	}
	approved, err := manager.Plan(context.Background(), []string{"packages"})
	if err != nil {
		t.Fatal(err)
	}
	installed["curl"] = false
	result, err := manager.Apply(context.Background(), approved)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusError ||
		len(result.Errors) != 1 ||
		result.Errors[0].Code != "setup_plan_stale" {
		t.Fatalf("result = %#v", result)
	}
	if installCalls != 0 {
		t.Fatalf("package install calls = %d", installCalls)
	}
}

func TestSystemBackendVerifiesPinnedZabbixRepositoryPackage(t *testing.T) {
	t.Parallel()

	content := []byte("repo-pkg")
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	var calls []execx.Spec
	installed := map[string]bool{}
	backend := SystemBackend{
		Root: t.TempDir(),
		HTTPClient: httpDoerFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Scheme != "https" || request.URL.Host != "repo.zabbix.com" {
				t.Fatalf("request URL = %s", request.URL)
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: int64(len(content)),
				Body:          io.NopCloser(strings.NewReader(string(content))),
				Header:        make(http.Header),
			}, nil
		}),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			switch spec.Program {
			case "dpkg-query":
				var output strings.Builder
				for name := range installed {
					fmt.Fprintf(&output, "%s\tinstall ok installed\n", name)
				}
				return execx.Output{ExitCode: 1, Stdout: []byte(output.String())}, nil
			case "dpkg":
				installed["zabbix-release"] = true
			case "apt-get":
				if containsArgument(spec.Arguments, "install") {
					installed["zabbix-agent2"] = true
				}
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Zabbix = &ZabbixConfig{
		Enabled: true, Server: "192.0.2.10", Hostname: "web-01",
		RepositoryPackageURL:    "https://repo.zabbix.com/release.deb",
		RepositoryPackageSize:   int64(len(content)),
		RepositoryPackageSHA256: digest,
	}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemZabbix},
	}
	if err := backend.Apply(context.Background(), ItemPackages, profile); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 5 || calls[0].Program != "dpkg-query" ||
		calls[1].Program != "dpkg" ||
		!reflect.DeepEqual(calls[1].Arguments[:2], []string{"--install", "--"}) ||
		calls[2].Program != "apt-get" || calls[3].Program != "apt-get" {
		t.Fatalf("calls = %#v", calls)
	}

	config.Zabbix.RepositoryPackageSHA256 = strings.Repeat("0", 64)
	calls = nil
	installed = map[string]bool{}
	if err := backend.Apply(context.Background(), ItemPackages, Profile{
		Platform: profile.Platform, Config: config, Items: profile.Items,
	}); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
	if len(calls) != 1 || calls[0].Program != "dpkg-query" {
		t.Fatalf("commands ran after digest mismatch: %#v", calls)
	}
}

func TestSystemBackendDoesNotReinstallPinnedZabbixRepositoryPackage(t *testing.T) {
	t.Parallel()

	downloads := 0
	var calls []execx.Spec
	installed := map[string]bool{"zabbix-release": true}
	backend := SystemBackend{
		Root: t.TempDir(),
		HTTPClient: httpDoerFunc(func(*http.Request) (*http.Response, error) {
			downloads++
			return nil, errors.New("unexpected download")
		}),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			if spec.Program == "dpkg-query" {
				var output strings.Builder
				for name := range installed {
					fmt.Fprintf(&output, "%s\tinstall ok installed\n", name)
				}
				return execx.Output{ExitCode: 1, Stdout: []byte(output.String())}, nil
			}
			if spec.Program == "apt-get" && containsArgument(spec.Arguments, "install") {
				installed["zabbix-agent2"] = true
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Zabbix = &ZabbixConfig{
		Enabled: true, Server: "192.0.2.10", Hostname: "web-01",
		RepositoryPackageURL:    "https://repo.zabbix.com/release.deb",
		RepositoryPackageSize:   8,
		RepositoryPackageSHA256: strings.Repeat("0", 64),
	}
	if err := backend.Apply(context.Background(), ItemPackages, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemZabbix},
	}); err != nil {
		t.Fatal(err)
	}
	if downloads != 0 || len(calls) != 4 ||
		calls[0].Program != "dpkg-query" ||
		calls[1].Program != "apt-get" ||
		calls[2].Program != "apt-get" {
		t.Fatalf("downloads=%d calls=%#v", downloads, calls)
	}
}

func TestSystemBackendTreatsMissingPackagesAsDrift(t *testing.T) {
	t.Parallel()

	backend := SystemBackend{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program != "dpkg-query" {
				return execx.Output{}, errors.New("unexpected program")
			}
			return execx.Output{
				ExitCode: 1,
				Stdout:   []byte("curl\tinstall ok installed\n"),
			}, nil
		}),
	}
	config := DefaultConfig()
	config.Packages = []string{"curl", "vim"}
	observation, err := backend.Observe(context.Background(), ItemPackages, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged ||
		!reflect.DeepEqual(observation.Details["missing"], []string{"vim"}) {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestShellHistoryWithoutAdministratorsDoesNotRequireSudo(t *testing.T) {
	t.Parallel()

	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemUsers, ItemShellHistory},
	}
	if packages := desiredPackages(profile); len(packages) != 0 {
		t.Fatalf("packages = %#v, want none", packages)
	}
}

func TestSystemBackendRepairsAndRollsBackServiceState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	enabled := false
	active := false
	var actions []string
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program == "fail2ban-client" {
				if active {
					return execx.Output{}, nil
				}
				return execx.Output{ExitCode: 1}, nil
			}
			if spec.Program != "systemctl" {
				return execx.Output{}, errors.New("unexpected program")
			}
			switch {
			case containsArgument(spec.Arguments, "is-enabled"):
				if enabled {
					return execx.Output{}, nil
				}
				return execx.Output{ExitCode: 1}, nil
			case containsArgument(spec.Arguments, "is-active"):
				if active {
					return execx.Output{}, nil
				}
				return execx.Output{ExitCode: 3}, nil
			case containsArgument(spec.Arguments, "enable"):
				actions = append(actions, "enable")
				enabled = true
			case containsArgument(spec.Arguments, "start"):
				actions = append(actions, "start")
				active = true
			case containsArgument(spec.Arguments, "reload"):
				actions = append(actions, "reload")
				if len(actions) == 3 {
					return execx.Output{ExitCode: 1}, nil
				}
			case containsArgument(spec.Arguments, "disable"):
				actions = append(actions, "disable")
				enabled = false
			case containsArgument(spec.Arguments, "stop"):
				actions = append(actions, "stop")
				active = false
			}
			return execx.Output{}, nil
		}),
	}
	err := backend.Apply(context.Background(), ItemFail2Ban, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemFail2Ban},
	})
	if err == nil {
		t.Fatal("failed service activation was accepted")
	}
	if enabled || active {
		t.Fatalf("service state was not restored: enabled=%v active=%v", enabled, active)
	}
	if !reflect.DeepEqual(
		actions,
		[]string{"enable", "start", "reload", "reload", "stop", "disable"},
	) {
		t.Fatalf("service actions = %#v", actions)
	}
}

func TestSystemBackendRepairsDisabledActiveService(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	enabled := false
	var actions []string
	backend := SystemBackend{Root: root}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemFail2Ban},
	}
	managed, err := backend.desiredFile(ItemFail2Ban, profile)
	if err != nil {
		t.Fatal(err)
	}
	target := backend.path(managed.Path)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, managed.Content, managed.Mode); err != nil {
		t.Fatal(err)
	}
	backend.Runner = execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch {
		case spec.Program == "fail2ban-client":
			return execx.Output{}, nil
		case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-enabled"):
			if enabled {
				return execx.Output{}, nil
			}
			return execx.Output{ExitCode: 1}, nil
		case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-active"):
			return execx.Output{}, nil
		case spec.Program == "systemctl" && containsArgument(spec.Arguments, "enable"):
			enabled = true
			actions = append(actions, "enable")
			return execx.Output{}, nil
		case spec.Program == "systemctl" && containsArgument(spec.Arguments, "reload"):
			actions = append(actions, "reload")
			return execx.Output{}, nil
		default:
			return execx.Output{}, errors.New("unexpected command")
		}
	})
	observation, err := backend.Observe(context.Background(), ItemFail2Ban, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("disabled managed service was reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemFail2Ban, profile); err != nil {
		t.Fatal(err)
	}
	if !enabled || !reflect.DeepEqual(actions, []string{"enable", "reload"}) {
		t.Fatalf("enabled=%v actions=%#v", enabled, actions)
	}
}

func TestSystemBackendCreatesMissingAdministratorWithDirectArgv(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	passwd := filepath.Join(root, "etc", "passwd")
	if err := os.MkdirAll(filepath.Dir(passwd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwd, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []execx.Spec
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			if spec.Program == "id" {
				return execx.Output{ExitCode: 1}, nil
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{Name: "operator", Groups: []string{"sudo", "adm"}}}
	if err := backend.Apply(context.Background(), ItemUsers, Profile{
		Platform: Platform{ID: "ubuntu", Version: "22.04"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers},
	}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Program != "id" || calls[1].Program != "useradd" {
		t.Fatalf("calls = %#v", calls)
	}
	if !reflect.DeepEqual(calls[1].Arguments,
		[]string{"--create-home", "--shell", "/bin/bash", "--groups", "adm,sudo", "--", "operator"}) {
		t.Fatalf("useradd argv = %#v", calls[1].Arguments)
	}
}

func TestSystemBackendRepairsOnlyMissingAdministratorGroups(t *testing.T) {
	t.Parallel()

	groups := []string{"operator", "sudo"}
	var calls []execx.Spec
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "home", "operator"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			switch {
			case spec.Program == "id" && containsArgument(spec.Arguments, "-u"):
				return execx.Output{Stdout: []byte("1000\n")}, nil
			case spec.Program == "id" && containsArgument(spec.Arguments, "-nG"):
				return execx.Output{Stdout: []byte(strings.Join(groups, " ") + "\n")}, nil
			case spec.Program == "getent":
				return execx.Output{Stdout: []byte(
					"operator:x:1000:1000:Operator:/home/operator:/bin/bash\n",
				)}, nil
			case spec.Program == "usermod":
				groups = append(groups, "adm")
				return execx.Output{}, nil
			default:
				return execx.Output{}, errors.New("unexpected command")
			}
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{
		Name: "operator", Groups: []string{"sudo", "adm"},
	}}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers},
	}
	observation, err := backend.Observe(context.Background(), ItemUsers, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("missing administrator group was reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemUsers, profile); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), ItemUsers, profile); err != nil {
		t.Fatal(err)
	}
	var usermodCalls []execx.Spec
	for _, call := range calls {
		if call.Program == "usermod" {
			usermodCalls = append(usermodCalls, call)
		}
	}
	if len(usermodCalls) != 1 || !reflect.DeepEqual(
		usermodCalls[0].Arguments,
		[]string{"--append", "--groups", "adm", "--", "operator"},
	) {
		t.Fatalf("usermod calls = %#v", usermodCalls)
	}
}

func TestSystemBackendObservesAndInstallsAuthorizedKeysUnderInjectedRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "etc", "ohtools", "plugins", "keys", "operator.pub")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	key := validEd25519PublicKey("operator@example")
	if err := os.WriteFile(source, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "home", "operator"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program == "getent" {
				return execx.Output{Stdout: []byte(
					"operator:x:1000:1000:Operator:/home/operator:/bin/bash\n",
				)}, nil
			}
			if spec.Program == "id" && containsArgument(spec.Arguments, "-u") {
				return execx.Output{Stdout: []byte("1000\n")}, nil
			}
			if spec.Program == "id" {
				return execx.Output{Stdout: []byte("operator sudo\n")}, nil
			}
			return execx.Output{}, errors.New("unexpected command")
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{
		Name: "operator", Groups: []string{"sudo"},
		AuthorizedKeySources: []string{
			"/etc/ohtools/plugins/keys/operator.pub",
		},
	}}
	profile := Profile{
		Platform: Platform{ID: "ubuntu", Version: "24.04"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers},
	}
	observation, err := backend.Observe(context.Background(), ItemUsers, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("missing authorized key was reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemUsers, profile); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), ItemUsers, profile); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "home", "operator", ".ssh", "authorized_keys")
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != key+"\n" {
		t.Fatalf("authorized_keys = %q", content)
	}
}

func TestSystemBackendRejectsMalformedAuthorizedKeyBeforeSSHHardening(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "etc", "ohtools", "plugins", "keys", "operator.pub")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("ssh-ed25519 not-base64 operator\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "home", "operator"), 0o700); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if output, ok := usableAdministratorOutput(spec); ok {
				return output, nil
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{
		Name: "operator", Groups: []string{"sudo"},
		AuthorizedKeySources: []string{"/etc/ohtools/plugins/keys/operator.pub"},
	}}
	_, err := backend.Observe(context.Background(), ItemUsers, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemSSH},
	})
	if err == nil {
		t.Fatal("malformed SSH public key was accepted")
	}
}

func TestSystemBackendRejectsAdministratorWithNonLoginShell(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	key := validEd25519PublicKey("operator@example")
	source := filepath.Join(root, "etc", "ohtools", "plugins", "keys", "operator.pub")
	target := filepath.Join(root, "home", "operator", ".ssh", "authorized_keys")
	for _, directory := range []string{filepath.Dir(source), filepath.Dir(target)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(source, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch {
			case spec.Program == "id" && containsArgument(spec.Arguments, "-u"):
				return execx.Output{Stdout: []byte("1000\n")}, nil
			case spec.Program == "id":
				return execx.Output{Stdout: []byte("operator sudo\n")}, nil
			case spec.Program == "getent":
				return execx.Output{Stdout: []byte(
					"operator:x:1000:1000:Operator:/home/operator:/usr/sbin/nologin\n",
				)}, nil
			default:
				return execx.Output{}, nil
			}
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{
		Name: "operator", Groups: []string{"sudo"},
		AuthorizedKeySources: []string{"/etc/ohtools/plugins/keys/operator.pub"},
	}}
	_, err := backend.Observe(context.Background(), ItemUsers, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemSSH},
	})
	if err == nil {
		t.Fatal("administrator with non-login shell was accepted")
	}
}

func TestSystemBackendRejectsAdministratorWithMissingHomeDirectory(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch {
			case spec.Program == "id" && containsArgument(spec.Arguments, "-u"):
				return execx.Output{Stdout: []byte("1000\n")}, nil
			case spec.Program == "getent":
				return execx.Output{Stdout: []byte(
					"operator:x:1000:1000:Operator:/home/operator:/bin/bash\n",
				)}, nil
			default:
				return execx.Output{}, nil
			}
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{Name: "operator"}}
	_, err := backend.Observe(context.Background(), ItemUsers, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers},
	})
	if err == nil {
		t.Fatal("administrator with a missing home directory was accepted")
	}
	if _, statErr := os.Lstat(filepath.Join(root, "home")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("read-only administrator observation created a home parent: %v", statErr)
	}
}

func TestFirewallObservationRejectsWrongActivePort(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch {
			case spec.Program == "systemctl":
				return execx.Output{}, nil
			case spec.Program == "nft" && containsArgument(spec.Arguments, "list"):
				return execx.Output{Stdout: []byte(
					"table inet ohtools_server_setup { chain input { type filter hook input priority 0; policy accept; tcp dport 22 accept; } }\n",
				)}, nil
			default:
				return execx.Output{}, nil
			}
		}),
	}
	config := DefaultConfig()
	config.SSHPort = 2222
	config.ManageFirewall = true
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemFirewall},
	}
	for _, managed := range mustFirewallFiles(t, backend, profile) {
		target := backend.path(managed.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, managed.Content, managed.Mode); err != nil {
			t.Fatal(err)
		}
	}
	observation, err := backend.Observe(context.Background(), ItemFirewall, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("firewall with the wrong active SSH port was accepted")
	}
}

func TestFirewallObservationRejectsInactiveManagedUnit(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	config := DefaultConfig()
	config.ManageFirewall = true
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemFirewall},
	}
	backend := SystemBackend{Root: root}
	rules := mustFirewallFiles(t, backend, profile)[0].Content
	backend.Runner = execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch {
		case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-enabled"):
			return execx.Output{}, nil
		case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-active"):
			return execx.Output{ExitCode: 3}, nil
		case spec.Program == "nft":
			return execx.Output{Stdout: rules}, nil
		default:
			return execx.Output{}, nil
		}
	})
	for _, managed := range mustFirewallFiles(t, backend, profile) {
		target := backend.path(managed.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, managed.Content, managed.Mode); err != nil {
			t.Fatal(err)
		}
	}
	observation, err := backend.Observe(context.Background(), ItemFirewall, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("inactive firewall unit was accepted as converged")
	}
}

func TestFirewallRollbackRestoresActiveRulesAndEnablement(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	oldConfig := DefaultConfig()
	oldConfig.ManageFirewall = true
	oldProfile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   oldConfig,
		Items:    []Item{ItemPackages, ItemFirewall},
	}
	enabled := false
	unitActive := true
	activeRules := append([]byte(nil), mustFirewallFiles(
		t,
		SystemBackend{Root: root},
		oldProfile,
	)[0].Content...)
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch {
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-enabled"):
				if enabled {
					return execx.Output{}, nil
				}
				return execx.Output{ExitCode: 1}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-active"):
				if unitActive {
					return execx.Output{}, nil
				}
				return execx.Output{ExitCode: 3}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "enable"):
				enabled = true
				return execx.Output{}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "disable"):
				enabled = false
				return execx.Output{}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "restart"):
				activeRules = nil
				unitActive = false
				return execx.Output{ExitCode: 1, Stderr: []byte("restart failed")}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "start"):
				unitActive = true
				return execx.Output{}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "stop"):
				unitActive = false
				return execx.Output{}, nil
			case spec.Program == "systemctl":
				return execx.Output{}, nil
			case spec.Program == "nft" && containsArgument(spec.Arguments, "list"):
				if len(activeRules) == 0 {
					return execx.Output{ExitCode: 1}, nil
				}
				return execx.Output{Stdout: append([]byte(nil), activeRules...)}, nil
			case spec.Program == "nft" && containsArgument(spec.Arguments, "-c"):
				return execx.Output{}, nil
			case spec.Program == "nft" && containsArgument(spec.Arguments, "-f"):
				if len(spec.Stdin) > 0 {
					activeRules = append([]byte(nil), bytes.TrimPrefix(
						spec.Stdin,
						[]byte("delete table inet ohtools_server_setup\n"),
					)...)
					return execx.Output{}, nil
				}
				return execx.Output{}, errors.New("nft restore path is missing")
			case spec.Program == "nft" && containsArgument(spec.Arguments, "delete"):
				activeRules = nil
				return execx.Output{}, nil
			default:
				return execx.Output{}, nil
			}
		}),
	}
	for _, managed := range mustFirewallFiles(t, backend, oldProfile) {
		target := backend.path(managed.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, managed.Content, managed.Mode); err != nil {
			t.Fatal(err)
		}
	}
	newConfig := oldConfig
	newConfig.SSHPort = 2222
	err := backend.Apply(context.Background(), ItemFirewall, Profile{
		Platform: oldProfile.Platform,
		Config:   newConfig,
		Items:    oldProfile.Items,
	})
	if err == nil {
		t.Fatal("firewall restart failure was ignored")
	}
	if enabled {
		t.Fatal("firewall service enablement was not rolled back")
	}
	if !unitActive {
		t.Fatal("firewall service active state was not rolled back")
	}
	if !firewallRulesEqual(activeRules, mustFirewallFiles(t, backend, oldProfile)[0].Content) {
		t.Fatalf("active firewall rules were not restored: %q", activeRules)
	}
}

func TestFirewallRollbackUsesIndependentBoundedContext(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	config := DefaultConfig()
	config.ManageFirewall = true
	oldProfile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemFirewall},
	}
	backend := SystemBackend{Root: root}
	oldRules := append([]byte(nil), mustFirewallFiles(t, backend, oldProfile)[0].Content...)
	for _, managed := range mustFirewallFiles(t, backend, oldProfile) {
		target := backend.path(managed.Path)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, managed.Content, managed.Mode); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	rollbackCalls := 0
	cleanupSawCanceledContext := false
	backend.Runner = execx.RunnerFunc(func(runContext context.Context, spec execx.Spec) (execx.Output, error) {
		switch {
		case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-enabled"):
			return execx.Output{ExitCode: 1}, nil
		case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-active"):
			return execx.Output{}, nil
		case spec.Program == "systemctl" && containsArgument(spec.Arguments, "enable"):
			return execx.Output{}, nil
		case spec.Program == "systemctl" && containsArgument(spec.Arguments, "restart"):
			cancel()
			return execx.Output{}, context.Canceled
		case spec.Program == "systemctl" &&
			(containsArgument(spec.Arguments, "disable") ||
				containsArgument(spec.Arguments, "start")):
			rollbackCalls++
			cleanupSawCanceledContext = cleanupSawCanceledContext || runContext.Err() != nil
			return execx.Output{}, nil
		case spec.Program == "systemctl":
			return execx.Output{}, nil
		case spec.Program == "nft" && containsArgument(spec.Arguments, "list"):
			return execx.Output{Stdout: append([]byte(nil), oldRules...)}, nil
		case spec.Program == "nft" && containsArgument(spec.Arguments, "-c"):
			return execx.Output{}, nil
		case spec.Program == "nft" &&
			(containsArgument(spec.Arguments, "delete") ||
				containsArgument(spec.Arguments, "-f")):
			rollbackCalls++
			cleanupSawCanceledContext = cleanupSawCanceledContext || runContext.Err() != nil
			return execx.Output{}, nil
		default:
			return execx.Output{}, nil
		}
	})
	newProfile := oldProfile
	newProfile.Config.SSHPort = 2222
	if err := backend.Apply(ctx, ItemFirewall, newProfile); err == nil {
		t.Fatal("canceled firewall restart was accepted")
	}
	if rollbackCalls != 3 {
		t.Fatalf("rollback calls = %d, want disable, start, and atomic rules restore", rollbackCalls)
	}
	if cleanupSawCanceledContext {
		t.Fatal("firewall rollback inherited the canceled operation context")
	}
}

func TestFirewallRollbackGivesEveryCleanupActionAFreshDeadline(t *testing.T) {
	t.Parallel()

	liveFollowupContexts := 0
	backend := SystemBackend{
		rollbackTimeout: time.Millisecond,
		Runner: execx.RunnerFunc(func(ctx context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program == "systemctl" &&
				containsArgument(spec.Arguments, "daemon-reload") {
				<-ctx.Done()
				return execx.Output{}, ctx.Err()
			}
			if ctx.Err() != nil {
				return execx.Output{}, errors.New("cleanup action inherited an expired deadline")
			}
			liveFollowupContexts++
			return execx.Output{}, nil
		}),
	}
	err := backend.rollbackFirewall(
		context.Background(),
		nil,
		true,
		firewallState{},
	)
	if err == nil {
		t.Fatal("timed out cleanup action was ignored")
	}
	if liveFollowupContexts != 3 {
		t.Fatalf("live follow-up cleanup contexts = %d, want 3", liveFollowupContexts)
	}
}

func TestFirewallPreflightsEveryManagedFileBeforeMutation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	config := DefaultConfig()
	config.ManageFirewall = true
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemFirewall},
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch {
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-enabled"):
				return execx.Output{ExitCode: 1}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-active"):
				return execx.Output{ExitCode: 3}, nil
			case spec.Program == "nft" && containsArgument(spec.Arguments, "list"):
				return execx.Output{ExitCode: 1}, nil
			default:
				return execx.Output{}, nil
			}
		}),
	}
	files := mustFirewallFiles(t, backend, profile)
	rulesPath := backend.path(files[0].Path)
	if err := os.MkdirAll(filepath.Dir(rulesPath), 0o755); err != nil {
		t.Fatal(err)
	}
	oldRules := []byte("old rules\n")
	if err := os.WriteFile(rulesPath, oldRules, files[0].Mode); err != nil {
		t.Fatal(err)
	}
	unitPath := backend.path(files[1].Path)
	if err := os.MkdirAll(unitPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := backend.Apply(context.Background(), ItemFirewall, profile); err == nil {
		t.Fatal("unsafe unit path was accepted")
	}
	got, err := os.ReadFile(rulesPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, oldRules) {
		t.Fatalf("rules were mutated before complete preflight: %q", got)
	}
}

func TestFirewallRestoresExistingTableInOneAtomicBatch(t *testing.T) {
	t.Parallel()

	var calls []execx.Spec
	backend := SystemBackend{
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			return execx.Output{}, nil
		}),
	}
	previous := firewallState{
		rulesActive: true,
		activeRules: []byte("table inet ohtools_server_setup { chain input { policy drop; } }\n"),
	}
	if err := backend.restoreFirewallActiveState(context.Background(), previous); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || !reflect.DeepEqual(calls[0].Arguments, []string{"-f", "-"}) {
		t.Fatalf("restore calls = %#v", calls)
	}
	if !bytes.Contains(calls[0].Stdin, []byte("delete table inet ohtools_server_setup\n")) ||
		!bytes.Contains(calls[0].Stdin, previous.activeRules) {
		t.Fatalf("atomic restore batch = %q", calls[0].Stdin)
	}
}

func TestForeignInputBaseChainBlocksSSHAndFirewallActivation(t *testing.T) {
	t.Parallel()

	foreign := []byte(`{"nftables":[{"chain":{"family":"inet","table":"foreign","name":"input","type":"filter","hook":"input","prio":0,"policy":"drop"}}]}`)
	if err := rejectForeignInputBaseChains(foreign); err == nil {
		t.Fatal("foreign input base chain was accepted")
	}
	own := []byte(`{"nftables":[{"chain":{"family":"inet","table":"ohtools_server_setup","name":"input","type":"filter","hook":"input","prio":0,"policy":"accept"}}]}`)
	if err := rejectForeignInputBaseChains(own); err != nil {
		t.Fatalf("managed input chain was rejected: %v", err)
	}
}

func TestSameNamedInputChainInForeignFamilyIsNotTrusted(t *testing.T) {
	t.Parallel()

	for _, family := range []string{"ip", "ip6", "bridge", "arp", "netdev"} {
		family := family
		t.Run(family, func(t *testing.T) {
			t.Parallel()
			ruleset := []byte(fmt.Sprintf(
				`{"nftables":[{"chain":{"family":%q,"table":"ohtools_server_setup","name":"input","type":"filter","hook":"input","prio":0,"policy":"drop"}}]}`,
				family,
			))
			if err := rejectForeignInputBaseChains(ruleset); err == nil {
				t.Fatalf("%s same-named input base chain was trusted", family)
			}
		})
	}
}

func mustFirewallFiles(t *testing.T, backend SystemBackend, profile Profile) []managedFile {
	t.Helper()
	files, err := backend.firewallFiles(profile)
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func validEd25519PublicKey(comment string) string {
	return validEd25519PublicKeyWithSeed(comment, 1)
}

func validEd25519PublicKeyWithSeed(comment string, seed byte) string {
	keyType := []byte("ssh-ed25519")
	public := make([]byte, 32)
	for index := range public {
		public[index] = byte(index) + seed
	}
	blob := make([]byte, 0, 4+len(keyType)+4+len(public))
	blob = binary.BigEndian.AppendUint32(blob, uint32(len(keyType)))
	blob = append(blob, keyType...)
	blob = binary.BigEndian.AppendUint32(blob, uint32(len(public)))
	blob = append(blob, public...)
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob) + " " + comment
}

func prepareUsableSSHAdministrator(t *testing.T, root string) Administrator {
	t.Helper()
	key := validEd25519PublicKey("operator@example")
	source := filepath.Join(root, "etc", "ohtools", "plugins", "keys", "operator.pub")
	target := filepath.Join(root, "home", "operator", ".ssh", "authorized_keys")
	for _, directory := range []string{filepath.Dir(source), filepath.Dir(target)} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{source, target} {
		if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return Administrator{
		Name: "operator", Groups: []string{"sudo"},
		AuthorizedKeySources: []string{"/etc/ohtools/plugins/keys/operator.pub"},
	}
}

func usableAdministratorOutput(spec execx.Spec) (execx.Output, bool) {
	switch {
	case spec.Program == "id" && containsArgument(spec.Arguments, "-u"):
		return execx.Output{Stdout: []byte("1000\n")}, true
	case spec.Program == "id":
		return execx.Output{Stdout: []byte("operator sudo\n")}, true
	case spec.Program == "getent":
		return execx.Output{Stdout: []byte(
			"operator:x:1000:1000:Operator:/home/operator:/bin/bash\n",
		)}, true
	default:
		return execx.Output{}, false
	}
}

func TestSystemBackendChecksLegacyEntitlementWithoutMutation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{Root: root, Runner: successfulRunner()}
	if err := backend.Entitled(context.Background(), Platform{
		ID: "debian", Version: "10", RequiresExtendedSupport: true,
	}); err == nil {
		t.Fatal("missing Debian ELTS source was accepted")
	}
	source := filepath.Join(root, "etc", "apt", "sources.list.d", "elts.list")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("deb https://deb.freexian.com/extended-lts buster-lts main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := backend.Entitled(context.Background(), Platform{
		ID: "debian", Version: "10", RequiresExtendedSupport: true,
	}); err != nil {
		t.Fatal(err)
	}

	ubuntu := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program != "pro" {
				return execx.Output{}, errors.New("unexpected program")
			}
			return execx.Output{Stdout: []byte(`{"attached":true}`)}, nil
		}),
	}
	if err := ubuntu.Entitled(context.Background(), Platform{
		ID: "ubuntu", Version: "20.04", RequiresExtendedSupport: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSystemBackendAddsEarlySSHIncludeAndVerifiesEffectiveState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mainConfig := filepath.Join(root, "etc", "ssh", "sshd_config")
	if err := os.MkdirAll(filepath.Dir(mainConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		mainConfig,
		[]byte("# vendor configuration\nUsePAM yes\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	var calls []execx.Spec
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			if output, ok := usableAdministratorOutput(spec); ok {
				return output, nil
			}
			if spec.Program == "sshd" && containsArgument(spec.Arguments, "-T") {
				return execx.Output{Stdout: []byte(
					"port 2222\n" +
						"permitrootlogin prohibit-password\n" +
						"passwordauthentication no\n" +
						"kbdinteractiveauthentication no\n" +
						"pubkeyauthentication yes\n",
				)}, nil
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.SSHPort = 2222
	config.ManageFirewall = true
	config.Administrators = []Administrator{prepareUsableSSHAdministrator(t, root)}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "10"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemFirewall, ItemSSH},
	}
	observation, err := backend.Observe(context.Background(), ItemSSH, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("missing SSH include and drop-in were reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemSSH, profile); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), ItemSSH, profile); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(mainConfig)
	if err != nil {
		t.Fatal(err)
	}
	const include = "Include /etc/ssh/sshd_config.d/*.conf\n"
	if !strings.HasPrefix(string(content), include) ||
		strings.Count(string(content), include) != 1 {
		t.Fatalf("sshd_config = %q", content)
	}
	if len(calls) < 3 {
		t.Fatalf("expected syntax, reload, and effective-state checks; calls=%#v", calls)
	}
}

func TestSSHIncludeRecoversInterruptedActivationBeforeRetry(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "etc", "ssh", "sshd_config")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		target,
		[]byte(sshIncludeDirective+"\n# interrupted\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		target+".ohtools-include.rollback",
		[]byte("# previous\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		target+".ohtools-include.stage",
		[]byte(sshIncludeDirective+"\n# staged\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{Root: root, Runner: successfulRunner()}
	change, err := backend.beginSSHInclude(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := change.commit(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(content), sshIncludeDirective+"\n") {
		t.Fatalf("recovered SSH include content = %q", content)
	}
	for _, suffix := range []string{
		".ohtools-include.stage",
		".ohtools-include.rollback",
	} {
		if _, err := os.Lstat(target + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("SSH recovery left %s: %v", suffix, err)
		}
	}
}

func TestSystemBackendBlocksSSHHardeningUntilAdministratorKeyIsUsable(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mainConfig := filepath.Join(root, "etc", "ssh", "sshd_config")
	if err := os.MkdirAll(filepath.Dir(mainConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mainConfig, []byte("# vendor configuration\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program == "id" {
				return execx.Output{ExitCode: 1}, nil
			}
			if spec.Program == "sshd" && containsArgument(spec.Arguments, "-T") {
				return execx.Output{Stdout: []byte(
					"port 22\n" +
						"permitrootlogin prohibit-password\n" +
						"passwordauthentication no\n" +
						"kbdinteractiveauthentication no\n" +
						"pubkeyauthentication yes\n",
				)}, nil
			}
			if spec.Program == "systemctl" {
				reloads++
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{
		Name: "operator",
		AuthorizedKeySources: []string{
			"/etc/ohtools/plugins/keys/operator.pub",
		},
	}}
	err := backend.Apply(context.Background(), ItemSSH, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemSSH},
	})
	if err == nil {
		t.Fatal("SSH hardening accepted an unavailable administrator key")
	}
	if reloads != 0 {
		t.Fatalf("SSH was reloaded before administrator access validation: %d", reloads)
	}
}

func TestSystemBackendRollsBackSSHIncludeWhenActivationFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mainConfig := filepath.Join(root, "etc", "ssh", "sshd_config")
	if err := os.MkdirAll(filepath.Dir(mainConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("# vendor configuration\n")
	if err := os.WriteFile(mainConfig, original, 0o600); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if output, ok := usableAdministratorOutput(spec); ok {
				return output, nil
			}
			if spec.Program == "sshd" && containsArgument(spec.Arguments, "-T") {
				return execx.Output{Stdout: []byte(
					"port 22\n" +
						"permitrootlogin prohibit-password\n" +
						"passwordauthentication no\n" +
						"kbdinteractiveauthentication no\n" +
						"pubkeyauthentication yes\n",
				)}, nil
			}
			if spec.Program == "systemctl" {
				return execx.Output{ExitCode: 1, Stderr: []byte("reload rejected")}, nil
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{prepareUsableSSHAdministrator(t, root)}
	err := backend.Apply(context.Background(), ItemSSH, Profile{
		Platform: Platform{ID: "debian", Version: "10"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemSSH},
	})
	if err == nil {
		t.Fatal("SSH activation failure was ignored")
	}
	content, readErr := os.ReadFile(mainConfig)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(content, original) {
		t.Fatalf("main SSH configuration was not rolled back: %q", content)
	}
}

func TestSystemBackendBlocksForeignSSHConflictBeforeReload(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mainConfig := filepath.Join(root, "etc", "ssh", "sshd_config")
	if err := os.MkdirAll(filepath.Dir(mainConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		mainConfig,
		[]byte(sshIncludeDirective+"\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "etc", "ssh", "sshd_config.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	previousDropIn := []byte("# previous managed state\n")
	if err := os.WriteFile(target, previousDropIn, 0o600); err != nil {
		t.Fatal(err)
	}
	reloads := 0
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if output, ok := usableAdministratorOutput(spec); ok {
				return output, nil
			}
			if spec.Program == "sshd" && containsArgument(spec.Arguments, "-T") {
				return execx.Output{Stdout: []byte(
					"port 22\n" +
						"permitrootlogin yes\n" +
						"passwordauthentication yes\n" +
						"kbdinteractiveauthentication yes\n" +
						"pubkeyauthentication yes\n",
				)}, nil
			}
			if spec.Program == "systemctl" {
				reloads++
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{prepareUsableSSHAdministrator(t, root)}
	err := backend.Apply(context.Background(), ItemSSH, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemSSH},
	})
	if err == nil {
		t.Fatal("foreign effective SSH conflict was accepted")
	}
	if reloads != 0 {
		t.Fatalf("sshd reloaded before effective-state validation: %d", reloads)
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !reflect.DeepEqual(content, previousDropIn) {
		t.Fatalf("managed drop-in was not restored after conflict: %q", content)
	}
}

func TestSystemBackendPersistsFirewallWithoutSecondUpdate(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	enabled := false
	active := false
	unitActive := false
	daemonReloads := 0
	enableCalls := 0
	restartCalls := 0
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch {
			case spec.Program == "nft" && containsArgument(spec.Arguments, "-c"):
				return execx.Output{}, nil
			case spec.Program == "nft" && containsArgument(spec.Arguments, "list"):
				if active {
					return execx.Output{Stdout: []byte(
						"table inet ohtools_server_setup {\n chain input { type filter hook input priority 0; policy accept; tcp dport 22 accept; }\n}\n",
					)}, nil
				}
				return execx.Output{ExitCode: 1}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-enabled"):
				if enabled {
					return execx.Output{}, nil
				}
				return execx.Output{ExitCode: 1}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "is-active"):
				if unitActive {
					return execx.Output{}, nil
				}
				return execx.Output{ExitCode: 3}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "daemon-reload"):
				daemonReloads++
				return execx.Output{}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "enable"):
				enableCalls++
				enabled = true
				return execx.Output{}, nil
			case spec.Program == "systemctl" && containsArgument(spec.Arguments, "restart"):
				restartCalls++
				active = true
				unitActive = true
				return execx.Output{}, nil
			default:
				return execx.Output{}, errors.New("unexpected command")
			}
		}),
	}
	config := DefaultConfig()
	config.ManageFirewall = true
	profile := Profile{
		Platform: Platform{ID: "ubuntu", Version: "24.04"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemFirewall},
	}
	observation, err := backend.Observe(context.Background(), ItemFirewall, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("missing persistent firewall unit was reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemFirewall, profile); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), ItemFirewall, profile); err != nil {
		t.Fatal(err)
	}
	second, err := backend.Observe(context.Background(), ItemFirewall, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Converged || daemonReloads != 1 || enableCalls != 1 ||
		restartCalls != 1 {
		t.Fatalf(
			"second observation=%#v daemonReloads=%d enableCalls=%d restartCalls=%d",
			second,
			daemonReloads,
			enableCalls,
			restartCalls,
		)
	}
	unit := filepath.Join(
		root,
		"etc",
		"systemd",
		"system",
		"ohtools-server-setup-firewall.service",
	)
	content, err := os.ReadFile(unit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		string(content),
		"ExecStart=/usr/sbin/nft -f /etc/nftables.d/ohtools-server-setup.nft",
	) {
		t.Fatalf("firewall unit = %q", content)
	}
}

func successfulRunner() execx.Runner {
	return execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		if spec.Program == "sysctl" && containsArgument(spec.Arguments, "-n") {
			return execx.Output{Stdout: []byte("2\n1\n1\n1\n")}, nil
		}
		if spec.Program == "timedatectl" {
			return execx.Output{Stdout: []byte("yes\n")}, nil
		}
		if spec.Program == "zabbix_agent2" {
			return execx.Output{Stdout: []byte("agent.ping [t|1]\n")}, nil
		}
		return execx.Output{}, nil
	})
}

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (function httpDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}
