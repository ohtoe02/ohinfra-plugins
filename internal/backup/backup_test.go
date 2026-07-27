package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestManifestDeclaresReadOnlyBackupCommands(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{Root: t.TempDir()})
	if definition.Manifest.Name != "backup-base" {
		t.Fatalf("manifest name = %q", definition.Manifest.Name)
	}
	var paths [][]string
	for _, command := range definition.Manifest.Commands {
		paths = append(paths, command.Path)
		if command.Category != protocol.CategoryDiagnostic ||
			command.RequiresRoot || command.RequiresForce ||
			command.SupportsDryRun || command.RequiresConfirmation {
			t.Fatalf("command is not read-only: %#v", command)
		}
		if command.Arguments == nil || command.Flags == nil {
			t.Fatalf("command collections are nil: %#v", command)
		}
	}
	want := [][]string{
		{"backup", "status"},
		{"backup", "inventory"},
		{"backup", "history"},
	}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}

	for _, path := range want {
		invocation := testInvocation(path...)
		plan, err := definition.Plan(context.Background(), invocation)
		if err != nil {
			t.Fatalf("plan %v: %v", path, err)
		}
		if len(plan.Changes) != 0 || plan.RequiresRoot ||
			plan.RequiresForce || plan.RequiresConfirmation {
			t.Fatalf("unsafe plan for %v: %#v", path, plan)
		}
	}
}

func TestInventoryUsesCompiledLocalAdaptersAndReturnsSanitizedMetadata(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "etc/restic/restic.conf", `
RESTIC_REPOSITORY=/srv/backups
RESTIC_PASSWORD=restic-password-secret
AWS_SECRET_ACCESS_KEY=cloud-secret
`)
	writeFixture(t, root, "etc/borgmatic/config.yaml", `
repositories:
  - path: ssh://alice:borg-secret@backup.internal/./repo
encryption_passphrase: borg-passphrase-secret
`)
	writeFixture(t, root, "etc/rsnapshot.conf", `
snapshot_root	/mnt/snapshots/
backup	alice@remote.internal:/srv/	remote/
`)

	var calls []execx.Spec
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		calls = append(calls, spec)
		switch spec.Program {
		case "restic":
			return execx.Output{Stdout: []byte("restic 0.16.4 compiled with go1.22.1 on linux/amd64\n")}, nil
		case "borg":
			return execx.Output{Stdout: []byte("borg 1.2.8\n")}, nil
		case "rsnapshot":
			return execx.Output{Stdout: []byte("rsnapshot 1.4.5\n")}, nil
		default:
			t.Fatalf("unexpected program %q", spec.Program)
			return execx.Output{}, nil
		}
	})
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	definition := NewDefinition(Options{
		Root: root, Runner: runner, Host: "backup-host",
		Now: func() time.Time { return now },
	})
	result, err := definition.Execute(context.Background(), testInvocation("backup", "inventory"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPass {
		t.Fatalf("status = %q, want pass: %#v", result.Status, result)
	}
	items, ok := result.Data["products"].([]ProductInventory)
	if !ok || len(items) != 3 {
		t.Fatalf("products = %#v, want three products", result.Data["products"])
	}
	wantItems := []ProductInventory{
		{
			ID: "borg", Name: "BorgBackup", Installed: true, Version: "1.2.8",
			ConfigPresent: true, RepositoryType: "ssh",
		},
		{
			ID: "restic", Name: "Restic", Installed: true, Version: "0.16.4",
			ConfigPresent: true, RepositoryType: "local",
		},
		{
			ID: "rsnapshot", Name: "rsnapshot", Installed: true, Version: "1.4.5",
			ConfigPresent: true, RepositoryType: "local",
		},
	}
	if !reflect.DeepEqual(items, wantItems) {
		t.Fatalf("products = %#v, want %#v", items, wantItems)
	}
	wantCalls := []execx.Spec{
		{Program: "borg", Arguments: []string{"--version"}, StdoutLimit: versionOutputLimit, StderrLimit: commandErrorLimit},
		{Program: "restic", Arguments: []string{"version"}, StdoutLimit: versionOutputLimit, StderrLimit: commandErrorLimit},
		{Program: "rsnapshot", Arguments: []string{"-V"}, StdoutLimit: versionOutputLimit, StderrLimit: commandErrorLimit},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	for _, call := range calls {
		if len(call.Environment) != 0 || len(call.Stdin) != 0 {
			t.Fatalf("unsafe inherited command input: %#v", call)
		}
	}
	text := resultJSON(t, result)
	for _, forbidden := range []string{
		"restic-password-secret", "cloud-secret", "borg-secret",
		"borg-passphrase-secret", "alice", "backup.internal", "remote.internal",
		"AWS_SECRET_ACCESS_KEY", "RESTIC_PASSWORD", "encryption_passphrase",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("result leaks %q: %s", forbidden, text)
		}
	}
}

func TestStatusInspectsOnlyCompiledServicesAndTimers(t *testing.T) {
	t.Parallel()

	var systemdCalls []execx.Spec
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "borg":
			return execx.Output{Stdout: []byte("borg 1.2.8\n")}, nil
		case "restic":
			return execx.Output{Stdout: []byte("restic 0.16.4 compiled with go1.22.1 on linux/amd64\n")}, nil
		case "rsnapshot":
			return execx.Output{Stdout: []byte("rsnapshot 1.4.5\n")}, nil
		case "systemctl":
			systemdCalls = append(systemdCalls, spec)
			if strings.HasSuffix(spec.Arguments[1], ".timer") {
				return execx.Output{
					Stdout: []byte("LoadState=loaded\nActiveState=active\nSubState=waiting\n"),
				}, nil
			}
			return execx.Output{
				Stdout: []byte("LoadState=loaded\nActiveState=inactive\nSubState=dead\n"),
			}, nil
		default:
			t.Fatalf("unexpected program %q", spec.Program)
			return execx.Output{}, nil
		}
	})
	definition := NewDefinition(Options{Root: t.TempDir(), Runner: runner})
	result, err := definition.Execute(context.Background(), testInvocation("backup", "status"))
	if err != nil {
		t.Fatal(err)
	}
	statuses, ok := result.Data["schedules"].([]ScheduleStatus)
	if !ok || len(statuses) != 3 {
		t.Fatalf("schedules = %#v, want three", result.Data["schedules"])
	}
	for _, status := range statuses {
		if status.ServiceState != "inactive/dead" || status.TimerState != "active/waiting" {
			t.Fatalf("status = %#v", status)
		}
	}
	var want []execx.Spec
	for _, unit := range []string{
		"borg-backup.service", "borg-backup.timer",
		"restic-backup.service", "restic-backup.timer",
		"rsnapshot.service", "rsnapshot.timer",
	} {
		want = append(want, execx.Spec{
			Program: "systemctl",
			Arguments: []string{
				"show", unit, "--property=LoadState,ActiveState,SubState", "--no-pager",
			},
			StdoutLimit: unitOutputLimit, StderrLimit: commandErrorLimit,
		})
	}
	if !reflect.DeepEqual(systemdCalls, want) {
		t.Fatalf("systemctl calls = %#v, want %#v", systemdCalls, want)
	}
}

func TestHistoryReturnsOnlyBoundedSanitizedAggregates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "var/log/borg/backup.log", `
2026-07-27T09:00:00Z backup completed successfully repository=ssh://alice:secret@backup.internal/repo
2026-07-27T10:00:00Z backup failed BORG_PASSPHRASE=history-secret
`)
	writeFixture(t, root, "var/log/restic/backup.log", `
2026-07-27T08:00:00Z snapshot completed https://cloud-user:cloud-secret@example.invalid/repo
`)
	called := false
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			called = true
			return execx.Output{}, nil
		}),
	})
	result, err := definition.Execute(context.Background(), testInvocation("backup", "history"))
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("history invoked an external command")
	}
	summaries, ok := result.Data["history"].([]HistorySummary)
	if !ok {
		t.Fatalf("history = %#v", result.Data["history"])
	}
	want := []HistorySummary{
		{
			ProductID: "borg", Entries: 2, Successes: 1, Failures: 1,
			LastTimestamp: "2026-07-27T10:00:00Z",
		},
		{
			ProductID: "restic", Entries: 1, Successes: 1,
			LastTimestamp: "2026-07-27T08:00:00Z",
		},
		{ProductID: "rsnapshot"},
	}
	if !reflect.DeepEqual(summaries, want) {
		t.Fatalf("history = %#v, want %#v", summaries, want)
	}
	text := resultJSON(t, result)
	for _, forbidden := range []string{
		"alice", "secret", "backup.internal", "cloud-user", "example.invalid",
		"BORG_PASSPHRASE", "repository=", "snapshot completed",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("history leaks %q: %s", forbidden, text)
		}
	}
}

func TestUnsafeLocalInputsArePartialAndNeverFollowed(t *testing.T) {
	t.Parallel()

	safeRunner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "borg":
			return execx.Output{Stdout: []byte("borg 1.2.8\n")}, nil
		case "restic":
			return execx.Output{Stdout: []byte("restic 0.16.4 compiled with go1.22.1 on linux/amd64\n")}, nil
		case "rsnapshot":
			return execx.Output{Stdout: []byte("rsnapshot 1.4.5\n")}, nil
		default:
			return execx.Output{}, execx.ErrNotFound
		}
	})

	t.Run("symlinked config", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "restic.conf")
		if err := os.WriteFile(outside, []byte(
			"RESTIC_REPOSITORY=ssh://alice:outside-secret@outside.invalid/repo\n",
		), 0o600); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "etc", "restic")
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(path, "restic.conf")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		result, err := NewDefinition(Options{Root: root, Runner: safeRunner}).Execute(
			context.Background(), testInvocation("backup", "inventory"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != protocol.StatusPartial || !hasError(result, "config_metadata", "restic") {
			t.Fatalf("result = %#v, want partial unsafe config", result)
		}
		if strings.Contains(resultJSON(t, result), "outside-secret") ||
			strings.Contains(resultJSON(t, result), "outside.invalid") {
			t.Fatalf("symlink target leaked: %s", resultJSON(t, result))
		}
	})

	t.Run("oversized history", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		writeFixture(t, root, "var/log/restic/backup.log",
			strings.Repeat("x", int(historyFileLimit)+1))
		result, err := NewDefinition(Options{Root: root}).Execute(
			context.Background(), testInvocation("backup", "history"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != protocol.StatusPartial || !hasError(result, "history_metadata", "restic") {
			t.Fatalf("result = %#v, want partial bounded history", result)
		}
	})

	t.Run("too many history lines", func(t *testing.T) {
		t.Parallel()

		root := t.TempDir()
		var lines strings.Builder
		for index := 0; index <= historyLineLimit; index++ {
			lines.WriteString("2026-07-27T09:00:00Z completed\n")
		}
		writeFixture(t, root, "var/log/borg/backup.log", lines.String())
		result, err := NewDefinition(Options{Root: root}).Execute(
			context.Background(), testInvocation("backup", "history"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != protocol.StatusPartial || !hasError(result, "history_metadata", "borg") {
			t.Fatalf("result = %#v, want bounded line count", result)
		}
	})
}

func TestMalformedCommandOutputIsPartialWithoutLeakingDiagnostics(t *testing.T) {
	t.Parallel()

	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "borg":
			return execx.Output{
				Stdout: []byte("borg unexpected\n"),
				Stderr: []byte("BORG_PASSPHRASE=version-secret"),
			}, nil
		case "restic", "rsnapshot":
			return execx.Output{}, execx.ErrNotFound
		case "systemctl":
			if spec.Arguments[1] == "borg-backup.timer" {
				return execx.Output{
					Stdout: []byte("LoadState=loaded\nEnvironment=PASSWORD=unit-secret\n"),
				}, nil
			}
			return execx.Output{Stdout: []byte("LoadState=not-found\n")}, nil
		default:
			t.Fatalf("unexpected program %q", spec.Program)
			return execx.Output{}, nil
		}
	})
	result, err := NewDefinition(Options{Root: t.TempDir(), Runner: runner}).Execute(
		context.Background(), testInvocation("backup", "status"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPartial ||
		!hasError(result, "version_probe", "borg") ||
		!hasError(result, "timer_metadata", "borg") {
		t.Fatalf("result = %#v, want partial version and timer probes", result)
	}
	text := resultJSON(t, result)
	for _, forbidden := range []string{"version-secret", "unit-secret", "PASSPHRASE", "Environment"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("result leaks %q: %s", forbidden, text)
		}
	}
}

func TestCommandsRejectCallerInputAndPropagateCancellation(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{Root: t.TempDir()})
	for _, path := range [][]string{
		{"backup", "status"}, {"backup", "inventory"}, {"backup", "history"},
	} {
		badArguments := testInvocation(path...)
		badArguments.Arguments = []string{"ssh://alice:secret@attacker.invalid/repo"}
		_, err := definition.Execute(context.Background(), badArguments)
		assertExitCode(t, err, protocol.ExitArguments)

		badOptions := testInvocation(path...)
		badOptions.Options = map[string]any{"repository": "https://attacker.invalid"}
		_, err = definition.Execute(context.Background(), badOptions)
		assertExitCode(t, err, protocol.ExitArguments)
	}

	called := false
	cancelled := NewDefinition(Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			called = true
			return execx.Output{}, nil
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := cancelled.Execute(ctx, testInvocation("backup", "status"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want cancellation", err)
	}
	if called {
		t.Fatal("runner called after cancellation")
	}
}

func TestBackupResultsAreDeterministic(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "etc/restic/restic.conf", "RESTIC_REPOSITORY=/srv/backups\n")
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "restic":
			return execx.Output{Stdout: []byte("restic 0.16.4 compiled with go1.22.1 on linux/amd64\n")}, nil
		default:
			return execx.Output{}, execx.ErrNotFound
		}
	})
	now := time.Date(2026, 7, 27, 18, 0, 0, 0, time.UTC)
	definition := NewDefinition(Options{
		Root: root, Runner: runner, Host: "backup-host",
		Now: func() time.Time { return now },
	})
	first, err := definition.Execute(context.Background(), testInvocation("backup", "inventory"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := definition.Execute(context.Background(), testInvocation("backup", "inventory"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("results differ:\nfirst=%#v\nsecond=%#v", first, second)
	}
}

func testInvocation(path ...string) protocol.Invocation {
	return protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "backup-test",
		CommandPath:     path,
		Arguments:       []string{},
		Options:         map[string]any{},
	}
}

func hasError(result protocol.Result, code, product string) bool {
	for _, issue := range result.Errors {
		if issue.Code == code && issue.Details["product"] == product {
			return true
		}
	}
	return false
}

func writeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func resultJSON(t *testing.T, result protocol.Result) string {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func assertExitCode(t *testing.T, err error, code int) {
	t.Helper()
	var failure protocol.ExitError
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("error = %v, want exit %d", err, code)
	}
}
