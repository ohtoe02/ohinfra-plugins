package baseline

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestDefinitionDeclaresApprovedReadOnlyCommands(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{Version: "1.0.0", Root: baselineFixture(t)})
	got := make([][]string, 0, len(definition.Manifest.Commands))
	for _, command := range definition.Manifest.Commands {
		got = append(got, command.Path)
		if command.Category != protocol.CategoryDiagnostic || command.RequiresRoot ||
			command.RequiresConfirmation || command.SupportsDryRun {
			t.Fatalf("command can mutate: %#v", command)
		}
	}
	want := [][]string{{"baseline", "check"}, {"baseline", "profile"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
	if err := protocol.ValidateManifest(definition.Manifest); err != nil {
		t.Fatal(err)
	}
	plan, err := definition.Plan(context.Background(), baselineInvocation("profile"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 || plan.RequiresRoot || plan.RequiresConfirmation {
		t.Fatalf("read-only plan can mutate: %#v", plan)
	}
}

func TestProfileReturnsStableCompiledChecksWithoutRunningCommands(t *testing.T) {
	t.Parallel()

	calls := 0
	definition := NewDefinition(testOptions(t, execx.RunnerFunc(
		func(context.Context, execx.Spec) (execx.Output, error) {
			calls++
			return execx.Output{}, errors.New("must not run")
		},
	)))
	first, err := definition.Execute(context.Background(), baselineInvocation("profile"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := definition.Execute(context.Background(), baselineInvocation("profile"))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("profile ran %d external commands", calls)
	}
	want := []string{
		"supported-os",
		"time-sync",
		"filesystem-ownership",
		"kernel-controls",
		"ssh-posture",
		"package-state",
		"required-services",
	}
	if !reflect.DeepEqual(first.Data["check_ids"], want) ||
		!reflect.DeepEqual(second.Data["check_ids"], want) {
		t.Fatalf("compiled checks are unstable: %#v / %#v", first.Data, second.Data)
	}
	if first.Status != protocol.StatusPass || second.Status != protocol.StatusPass {
		t.Fatalf("profile status = %s / %s", first.Status, second.Status)
	}
}

func TestCheckRunsCompiledLocalChecksInStableOrder(t *testing.T) {
	t.Parallel()

	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "timedatectl":
			return execx.Output{Stdout: []byte("yes\n")}, nil
		case "systemctl":
			return execx.Output{Stdout: []byte("active\n")}, nil
		default:
			return execx.Output{}, execx.ErrNotFound
		}
	})
	definition := NewDefinition(testOptions(t, runner))
	result, err := definition.Execute(context.Background(), baselineInvocation("check"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPass {
		t.Fatalf("status = %s, result = %#v", result.Status, result)
	}
	got := make([]string, len(result.Checks))
	for index, check := range result.Checks {
		got[index] = check.ID
	}
	want := []string{
		"baseline:filesystem-ownership",
		"baseline:kernel-controls",
		"baseline:package-state",
		"baseline:required-services",
		"baseline:ssh-posture",
		"baseline:supported-os",
		"baseline:time-sync",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("check order = %#v, want %#v", got, want)
	}
}

func TestCheckIsPartialWhenOptionalToolsAreMissing(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(testOptions(t, execx.RunnerFunc(
		func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{}, execx.ErrNotFound
		},
	)))
	result, err := definition.Execute(context.Background(), baselineInvocation("check"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPartial {
		t.Fatalf("status = %s, want partial: %#v", result.Status, result)
	}
	if len(result.Errors) == 0 {
		t.Fatalf("partial result has no structured errors: %#v", result)
	}
}

func TestCheckDoesNotTurnCancellationIntoPartial(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(testOptions(t, execx.RunnerFunc(
		func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{}, context.Canceled
		},
	)))
	_, err := definition.Execute(context.Background(), baselineInvocation("check"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("execute error = %v, want context.Canceled", err)
	}
}

func TestConfigCanOnlySelectCompiledChecksAndThresholds(t *testing.T) {
	t.Parallel()

	valid := DefaultConfig()
	valid.EnabledChecks = []string{"supported-os", "package-state"}
	valid.DisabledChecks = []string{"package-state"}
	valid.MaxPendingPackageRecords = 2
	selected, err := selectedChecks(valid)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"supported-os"}; !reflect.DeepEqual(selected, want) {
		t.Fatalf("selected checks = %#v, want %#v", selected, want)
	}

	for _, invalid := range []Config{
		{Profile: "custom"},
		{Profile: "default", EnabledChecks: []string{"shell-command"}},
		{Profile: "default", DisabledChecks: []string{"../../path"}},
		{Profile: "default", MaxPendingPackageRecords: -1},
	} {
		if _, err := selectedChecks(invalid); err == nil {
			t.Fatalf("invalid config accepted: %#v", invalid)
		}
	}
}

func TestExecuteRejectsArgumentsOptionsAndUnknownCommands(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(testOptions(t, nil))
	for _, invocation := range []protocol.Invocation{
		{
			ProtocolVersion: protocol.ProtocolVersion,
			CommandPath:     []string{"baseline", "check"},
			Arguments:       []string{"extra"},
		},
		{
			ProtocolVersion: protocol.ProtocolVersion,
			CommandPath:     []string{"baseline", "profile"},
			Options:         map[string]any{"path": "/tmp/unsafe"},
		},
		baselineInvocation("missing"),
	} {
		_, err := definition.Execute(context.Background(), invocation)
		var failure protocol.ExitError
		if !errors.As(err, &failure) || failure.Code != protocol.ExitArguments {
			t.Fatalf("execute(%#v) error = %v, want ExitArguments", invocation, err)
		}
	}
}

func testOptions(t *testing.T, runner execx.Runner) Options {
	t.Helper()
	return Options{
		Version: "1.0.0",
		Root:    baselineFixture(t),
		Runner:  runner,
		Host:    "test-host",
		Now: func() time.Time {
			return time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
		},
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
	}
}

func baselineFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeBaselineFile(t, root, "etc/os-release", "ID=debian\nVERSION_ID=12\n")
	writeBaselineFile(t, root, "proc/sys/kernel/randomize_va_space", "2\n")
	writeBaselineFile(t, root, "proc/sys/net/ipv4/conf/all/rp_filter", "1\n")
	writeBaselineFile(t, root, "etc/passwd", "root:x:0:0:root:/root:/bin/bash\n")
	writeBaselineFile(t, root, "etc/shadow", "root:!:1:0:99999:7:::\n")
	writeBaselineFile(t, root, "etc/ssh/sshd_config",
		"PermitRootLogin prohibit-password\nPasswordAuthentication no\n")
	writeBaselineFile(t, root, "var/lib/dpkg/status",
		"Package: base-files\nStatus: install ok installed\n")
	return root
}

func writeBaselineFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func baselineInvocation(command string) protocol.Invocation {
	return protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     []string{"baseline", command},
		Arguments:       []string{},
		Options:         map[string]any{},
	}
}
