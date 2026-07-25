package serversetup

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestSystemBackendPlansAndAppliesOrdinaryUpgradeWithIsolatedIndexes(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	var calls []execx.Spec
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			for _, argument := range spec.Arguments {
				if strings.Contains(argument, "dist-upgrade") ||
					strings.Contains(argument, "autoremove") ||
					strings.Contains(argument, "reboot") {
					t.Fatalf("unsafe apt argument %q", argument)
				}
			}
			if containsArgument(spec.Arguments, "--simulate") {
				return execx.Output{
					Stdout: []byte("Inst curl [7.88.1] (7.88.2 Debian:12/stable [amd64])\n"),
				}, nil
			}
			return execx.Output{}, nil
		}),
	}
	profile := Profile{Platform: Platform{ID: "debian", Version: "12"}, Config: DefaultConfig()}
	upgrades, err := backend.PlanUpgrade(context.Background(), profile)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(upgrades, []PackageUpgrade{{
		Name: "curl", CurrentVersion: "7.88.1", CandidateVersion: "7.88.2",
	}}) {
		t.Fatalf("upgrades = %#v", upgrades)
	}
	if err := backend.ApplyUpgrade(context.Background(), profile, upgrades); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("calls = %#v", calls)
	}
	for _, call := range calls {
		if call.Program != "apt-get" {
			t.Fatalf("program = %q", call.Program)
		}
		if !hasIsolatedListsOption(call.Arguments) {
			t.Fatalf("apt call does not use isolated lists: %#v", call.Arguments)
		}
	}
	if !containsArgument(calls[3].Arguments, "upgrade") ||
		!containsArgument(calls[3].Arguments, "--with-new-pkgs") ||
		!containsArgument(calls[3].Arguments, "-y") {
		t.Fatalf("upgrade argv = %#v", calls[3].Arguments)
	}
}

func TestManagerUpgradePlanAndApplyAreDeterministic(t *testing.T) {
	t.Parallel()

	backend := &memoryUpgradeBackend{
		memoryBackend: &memoryBackend{drift: map[Item]bool{}},
		upgrades: []PackageUpgrade{
			{Name: "zlib1g", CurrentVersion: "1.2.13", CandidateVersion: "1.2.14"},
			{Name: "curl", CurrentVersion: "7.88.1", CandidateVersion: "7.88.2"},
		},
	}
	manager := Manager{
		Backend: backend, Config: DefaultConfig(),
		Platform: Platform{ID: "debian", Version: "12"},
		Host:     "fixture",
		Tool:     protocol.Tool{Name: Name, Version: "1.0.0"},
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}
	first, err := manager.UpgradePlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.UpgradePlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("upgrade plans differ:\n%#v\n%#v", first, second)
	}
	if len(first.Changes) != 2 || first.Changes[0].Object != "curl" {
		t.Fatalf("plan = %#v", first)
	}
	result, err := manager.Upgrade(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPass || len(result.Changes) != 2 ||
		backend.applyCalls != 1 {
		t.Fatalf("result=%#v applyCalls=%d", result, backend.applyCalls)
	}
}

type memoryUpgradeBackend struct {
	*memoryBackend
	upgrades   []PackageUpgrade
	applyCalls int
}

func (backend *memoryUpgradeBackend) PlanUpgrade(
	context.Context,
	Profile,
) ([]PackageUpgrade, error) {
	return append([]PackageUpgrade(nil), backend.upgrades...), nil
}

func (backend *memoryUpgradeBackend) ApplyUpgrade(
	_ context.Context,
	_ Profile,
	_ []PackageUpgrade,
) error {
	backend.applyCalls++
	backend.upgrades = nil
	return nil
}

func containsArgument(arguments []string, expected string) bool {
	for _, argument := range arguments {
		if argument == expected {
			return true
		}
	}
	return false
}

func hasIsolatedListsOption(arguments []string) bool {
	for index := range arguments {
		if arguments[index] == "-o" && index+1 < len(arguments) &&
			strings.HasPrefix(arguments[index+1], "Dir::State::Lists=") {
			return true
		}
	}
	return false
}
