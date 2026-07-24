package systemd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohinfra-plugins/internal/execx"
	"github.com/ohtoe02/ohinfra-plugins/internal/protocol"
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

func invocation(command string, options map[string]any) protocol.Invocation {
	if options == nil {
		options = map[string]any{}
	}
	return protocol.Invocation{
		ProtocolVersion: 1, CommandPath: []string{"service", command},
		Arguments: []string{"nginx.service"}, Options: options,
	}
}
