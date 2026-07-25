package serversetup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestDefinitionPublishesStableSetupCommands(t *testing.T) {
	t.Parallel()

	platform := Platform{ID: "debian", Version: "12"}
	definition := NewDefinition(Options{
		Version: "1.0.0", Platform: &platform,
		Backend: &memoryBackend{drift: map[Item]bool{}},
	})
	if err := protocol.ValidateManifest(definition.Manifest); err != nil {
		t.Fatal(err)
	}
	if definition.Manifest.Name != Name || definition.Manifest.Version != "1.0.0" ||
		len(definition.Manifest.Commands) != 3 {
		t.Fatalf("manifest = %#v", definition.Manifest)
	}
	check, apply, upgrade := definition.Manifest.Commands[0], definition.Manifest.Commands[1],
		definition.Manifest.Commands[2]
	if !reflect.DeepEqual(check.Path, []string{"setup", "check"}) ||
		check.Category != protocol.CategoryDiagnostic {
		t.Fatalf("check command = %#v", check)
	}
	if !reflect.DeepEqual(apply.Path, []string{"setup", "apply"}) ||
		apply.Category != protocol.CategoryRunbook || !apply.RequiresRoot ||
		!apply.RequiresConfirmation || !apply.SupportsDryRun ||
		len(apply.Arguments) != 1 || !apply.Arguments[0].Variadic {
		t.Fatalf("apply command = %#v", apply)
	}
	if !reflect.DeepEqual(upgrade.Path, []string{"setup", "upgrade"}) ||
		upgrade.Category != protocol.CategoryRunbook || !upgrade.RequiresRoot ||
		!upgrade.RequiresConfirmation || !upgrade.SupportsDryRun {
		t.Fatalf("upgrade command = %#v", upgrade)
	}
}

func TestDefinitionCheckWorksWithoutMutationConfig(t *testing.T) {
	t.Parallel()

	platform := Platform{ID: "ubuntu", Version: "22.04"}
	backend := &memoryBackend{drift: map[Item]bool{ItemPackages: true}}
	definition := NewDefinition(Options{
		Version: "1.0.0", ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		Platform: &platform, Backend: backend,
	})
	result, err := definition.Execute(context.Background(), protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "check-request",
		CommandPath:     []string{"setup", "check"},
		Arguments:       []string{},
		Options:         map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Command != "setup check" || len(result.Checks) == 0 {
		t.Fatalf("result = %#v", result)
	}
	foundConfigurationGuidance := false
	for _, check := range result.Checks {
		if check.ID == "setup:configuration" &&
			check.Status == protocol.StatusWarning {
			foundConfigurationGuidance = true
		}
	}
	if !foundConfigurationGuidance {
		t.Fatalf("missing mutation profile guidance: %#v", result)
	}
}

func TestDefinitionCheckReportsExactMissingMutationProfilePath(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "missing-server-setup-base.yaml")
	platform := Platform{ID: "ubuntu", Version: "22.04"}
	definition := NewDefinition(Options{
		Version: "1.0.0", ConfigPath: configPath,
		Platform: &platform,
		Backend:  &memoryBackend{drift: map[Item]bool{}},
	})
	result, err := definition.Execute(context.Background(), protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "check-missing-config",
		CommandPath:     []string{"setup", "check"},
		Arguments:       []string{},
		Options:         map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Data["configuration_path"] != configPath {
		t.Fatalf("configuration path = %#v", result.Data["configuration_path"])
	}
	found := false
	for _, check := range result.Checks {
		if check.ID == "setup:configuration" &&
			check.Status == protocol.StatusWarning {
			found = true
		}
	}
	if !found {
		t.Fatalf("result lacks setup:configuration guidance: %#v", result)
	}
}

func TestDefinitionRejectsStaleApplyDigestBeforeMutation(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "server-setup-base.yaml")
	if err := os.WriteFile(configPath, []byte(
		"ssh_port: 22\nmanage_firewall: true\nadministrators:\n"+
			"  - name: operator\n"+
			"    authorized_key_sources: [/etc/ohtools/plugins/keys/operator.pub]\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	platform := Platform{ID: "debian", Version: "12"}
	backend := &memoryBackend{
		drift: map[Item]bool{
			ItemPackages: true, ItemUsers: true, ItemFirewall: true, ItemSSH: true,
		},
	}
	definition := NewDefinition(Options{
		Version: "1.0.0", ConfigPath: configPath,
		Platform: &platform, Backend: backend,
	})
	invocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "apply-request",
		CommandPath:     []string{"setup", "apply"},
		Arguments:       []string{"ssh"},
		Options:         map[string]any{},
	}
	plan, err := definition.Plan(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := protocol.PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	for item := range backend.drift {
		backend.drift[item] = false
	}
	invocation.PlanDigest = digest
	_, err = definition.Execute(context.Background(), invocation)
	var exitError protocol.ExitError
	if !errors.As(err, &exitError) || exitError.Code != protocol.ExitArguments {
		t.Fatalf("Execute() error = %#v", err)
	}
	if len(backend.applied) != 0 {
		t.Fatalf("stale plan mutated backend: %#v", backend.applied)
	}
}

func TestDefinitionRejectsConfigurationDriftWithSameObservedState(t *testing.T) {
	t.Parallel()

	configPath := filepath.Join(t.TempDir(), "server-setup-base.yaml")
	writeConfig := func(port int) {
		t.Helper()
		if err := os.WriteFile(configPath, []byte(fmt.Sprintf(
			"ssh_port: %d\nmanage_firewall: true\nadministrators:\n"+
				"  - name: operator\n"+
				"    authorized_key_sources: [/etc/ohtools/plugins/keys/operator.pub]\n",
			port,
		)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeConfig(22)
	platform := Platform{ID: "debian", Version: "12"}
	backend := &memoryBackend{drift: map[Item]bool{
		ItemPackages: true, ItemUsers: true, ItemFirewall: true, ItemSSH: true,
	}}
	definition := NewDefinition(Options{
		Version: "1.0.0", ConfigPath: configPath,
		Platform: &platform, Backend: backend,
	})
	invocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "configuration-drift",
		CommandPath:     []string{"setup", "apply"},
		Arguments:       []string{"ssh"},
		Options:         map[string]any{},
	}
	plan, err := definition.Plan(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := protocol.PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(2222)
	invocation.PlanDigest = digest
	_, err = definition.Execute(context.Background(), invocation)
	var exitError protocol.ExitError
	if !errors.As(err, &exitError) || exitError.Code != protocol.ExitArguments {
		t.Fatalf("Execute() error = %#v, want stale-plan argument failure", err)
	}
	if len(backend.applied) != 0 {
		t.Fatalf("configuration drift mutated backend: %#v", backend.applied)
	}
}
