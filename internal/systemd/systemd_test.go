package systemd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestValidateUnitRejectsInjectionAndOptionSmuggling(t *testing.T) {
	for _, unit := range []string{"-nginx", "nginx;reboot", "nginx service", "../nginx", "nginx\n.service"} {
		if err := ValidateUnit(unit); err == nil {
			t.Fatalf("ValidateUnit(%q) succeeded", unit)
		}
	}
	for _, unit := range []string{"nginx", "nginx.service", "postgresql@15-main.service"} {
		if err := ValidateUnit(unit); err != nil {
			t.Fatalf("ValidateUnit(%q) = %v", unit, err)
		}
	}
}

func TestDefinitionPreservesStatusLogsAndRestartContracts(t *testing.T) {
	var calls []execx.Spec
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		calls = append(calls, spec)
		switch spec.Program {
		case "systemctl":
			switch spec.Arguments[0] {
			case "show":
				return execx.Output{Stdout: []byte("Id=nginx.service\nActiveState=active\nMainPID=42\n")}, nil
			case "restart", "is-active":
				return execx.Output{}, nil
			}
		case "journalctl":
			return execx.Output{Stdout: []byte("password=hunter2\nnormal line\n")}, nil
		}
		return execx.Output{ExitCode: 1}, nil
	})
	definition := NewDefinition(Options{
		Version: "1.0.0", ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		Runner: runner, Host: "server01", Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if definition.Manifest.Name != "systemd-base" ||
		definition.Manifest.Description == "" ||
		len(definition.Manifest.Commands) != 3 {
		t.Fatalf("manifest = %#v", definition.Manifest)
	}

	status, err := definition.Execute(context.Background(), invocation("status", nil))
	if err != nil || status.Status != protocol.StatusPass || status.Data["active_state"] != "active" {
		t.Fatalf("status = %#v, err=%v", status, err)
	}
	logsInvocation := invocation("logs", map[string]any{"since": "1h0m0s", "lines": 20})
	logs, err := definition.Execute(context.Background(), logsInvocation)
	if err != nil || strings.Contains(strings.Join(logs.Data["lines"].([]string), " "), "hunter2") {
		t.Fatalf("logs = %#v, err=%v", logs, err)
	}

	restartInvocation := invocation("restart", nil)
	plan, err := definition.Plan(context.Background(), restartInvocation)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := protocol.PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	restartInvocation.PlanDigest = digest
	restarted, err := definition.Execute(context.Background(), restartInvocation)
	if err != nil || restarted.Status != protocol.StatusPass ||
		len(restarted.Changes) != 1 || restarted.Changes[0].Status != "completed" {
		t.Fatalf("restart = %#v, err=%v", restarted, err)
	}
	if len(calls) < 5 {
		t.Fatalf("calls = %#v", calls)
	}
}

func TestRestartRejectsUnapprovedPlanDigest(t *testing.T) {
	definition := NewDefinition(Options{
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{Stdout: []byte("ActiveState=active\n")}, nil
		}),
	})
	restart := invocation("restart", nil)
	restart.PlanDigest = "wrong"
	if _, err := definition.Execute(context.Background(), restart); err == nil {
		t.Fatal("execute accepted an unapproved plan digest")
	}
}

func TestRestartPlannerUsesOnlyReadOnlyInspection(t *testing.T) {
	inspector := &inspectorSpy{}
	mutation := &mutationBackendSpy{}
	definition := NewDefinition(Options{
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		Inspector:  inspector, MutationBackend: mutation,
	})
	if _, err := definition.Plan(context.Background(), invocation("restart", nil)); err != nil {
		t.Fatal(err)
	}
	if inspector.showCalls != 1 {
		t.Fatalf("planner inspection calls = %d, want 1", inspector.showCalls)
	}
	if mutation.restartCalls != 0 || mutation.isActiveCalls != 0 {
		t.Fatalf("planner reached mutation backend: %#v", mutation)
	}
}

func TestRestartPlannerLeavesProtocolIdentityAndRiskToRegistry(t *testing.T) {
	plan, err := (RestartPlanner{Inspector: &inspectorSpy{}}).Plan(
		context.Background(),
		"nginx.service",
	)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CommandID != "" || plan.RequiresRoot || plan.RequiresForce || plan.RequiresConfirmation {
		t.Fatalf("domain planner supplied protocol-owned fields: %#v", plan)
	}
}

func TestRestartDiagnosticLogsDoNotInventALookbackWindow(t *testing.T) {
	var captured execx.Spec
	inspector := commandInspector{runner: execx.RunnerFunc(
		func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			captured = spec
			return execx.Output{}, nil
		},
	)}
	if _, err := inspector.Logs(context.Background(), "nginx.service", 0, 50); err != nil {
		t.Fatal(err)
	}
	for _, argument := range captured.Arguments {
		if strings.HasPrefix(argument, "--since=") {
			t.Fatalf("restart diagnostics changed their historical scope: %#v", captured.Arguments)
		}
	}
}

type inspectorSpy struct {
	showCalls int
	logCalls  int
}

func (inspector *inspectorSpy) Show(
	context.Context,
	string,
) (map[string]string, execx.Output, error) {
	inspector.showCalls++
	return map[string]string{
		"Id": "nginx.service", "ActiveState": "active",
	}, execx.Output{}, nil
}

func (inspector *inspectorSpy) Logs(
	context.Context,
	string,
	time.Duration,
	int,
) (execx.Output, error) {
	inspector.logCalls++
	return execx.Output{}, nil
}

type mutationBackendSpy struct {
	restartCalls  int
	isActiveCalls int
}

func (backend *mutationBackendSpy) Restart(context.Context, string) (execx.Output, error) {
	backend.restartCalls++
	return execx.Output{}, nil
}

func (backend *mutationBackendSpy) IsActive(context.Context, string) (execx.Output, error) {
	backend.isActiveCalls++
	return execx.Output{}, nil
}

func invocation(command string, options map[string]any) protocol.Invocation {
	if options == nil {
		options = map[string]any{}
	}
	return protocol.Invocation{
		ProtocolVersion: 1, CommandPath: []string{"service", command},
		Arguments: []string{"nginx.service"}, Options: options,
	}
}
