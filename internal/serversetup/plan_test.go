package serversetup

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestBuildSetupPlanIsDeterministicAndListsExactChanges(t *testing.T) {
	t.Parallel()

	profile, err := ProfileByID("debian-12")
	if err != nil {
		t.Fatal(err)
	}
	checks := []protocol.Check{
		{ID: "unit:systemd-timesyncd.service", Status: protocol.StatusWarning},
		{ID: "sysctl:fs.protected_hardlinks", Status: protocol.StatusWarning},
		{ID: "file:/etc/ohtools/setup-state.yaml", Status: protocol.StatusWarning},
		{ID: "directory:/etc/ohtools", Status: protocol.StatusWarning},
		{ID: "user:ohtools", Status: protocol.StatusWarning},
		{ID: "group:ohtools", Status: protocol.StatusWarning},
		{ID: "package:curl", Status: protocol.StatusWarning},
	}

	first, err := BuildSetupPlan(PlanInput{
		Mode: PlanApply, Profile: profile, Checks: checks,
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := BuildSetupPlan(PlanInput{
		Mode: PlanApply, Profile: profile, Checks: checks,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("plans differ:\nfirst  %#v\nsecond %#v", first, second)
	}
	if first.CommandID != "setup.apply" || !first.RequiresRoot ||
		!first.RequiresConfirmation {
		t.Fatalf("plan contract = %#v", first)
	}

	got := map[string]string{}
	for _, change := range first.Changes {
		got[change.Object] = change.Action
	}
	want := map[string]string{
		"directory:/etc/ohtools":             "ensure",
		"file:/etc/ohtools/setup-state.yaml": "replace",
		"group:ohtools":                      "create",
		"package:curl":                       "install-cached",
		"sysctl:fs.protected_hardlinks":      "set",
		"unit:systemd-timesyncd.service":     "enable",
		"user:ohtools":                       "create",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("changes = %#v, want %#v", got, want)
	}
}

func TestDefinitionPlansCurrentApplyState(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	if err := os.Remove(fixture.path("etc/ohtools/setup-state.yaml")); err != nil {
		t.Fatal(err)
	}
	definition := NewDefinition(Options{
		Version: "1.0.0", Root: fixture.root,
		ConfigPath: fixture.path("missing-config.yaml"),
		Host:       "server01", Runner: healthyRunner(t),
		Now: func() time.Time { return fixture.now },
		Identity: FileIdentityReaderFunc(
			func(path string, _ os.FileInfo) (FileIdentity, error) {
				return expectedFixtureIdentity(path), nil
			},
		),
	})

	plan, err := definition.Plan(context.Background(), protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     []string{"setup", "apply"},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if len(plan.Changes) != 1 ||
		plan.Changes[0].Object != "file:/etc/ohtools/setup-state.yaml" ||
		plan.Changes[0].Action != "replace" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestBuildUpgradePlanUsesOnlyCompiledCachedPackages(t *testing.T) {
	t.Parallel()

	profile, err := ProfileByID("ubuntu-24.04")
	if err != nil {
		t.Fatal(err)
	}
	checks := make([]protocol.Check, 0, len(profile.Packages))
	for _, name := range profile.Packages {
		checks = append(checks, protocol.Check{
			ID: "package:" + name, Status: protocol.StatusPass,
		})
	}
	plan, err := BuildSetupPlan(PlanInput{
		Mode: PlanUpgrade, Profile: profile, Checks: checks,
		CachedUpgrades: append([]string(nil), profile.Packages...),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != len(profile.Packages) {
		t.Fatalf("changes = %#v", plan.Changes)
	}
	for index, change := range plan.Changes {
		if change.Object != "package:"+profile.Packages[index] ||
			change.Action != "upgrade-cached" {
			t.Fatalf("change = %#v", change)
		}
	}
}

func TestBuildSetupPlanOrdersIdentityBeforeOwnedPaths(t *testing.T) {
	t.Parallel()

	profile, err := ProfileByID("debian-12")
	if err != nil {
		t.Fatal(err)
	}
	checks := []protocol.Check{}
	for _, id := range []string{
		"package:curl",
		"group:ohtools",
		"user:ohtools",
		"directory:/var/lib/ohtools",
		"file:/etc/ohtools/setup-state.yaml",
		"sysctl:fs.protected_hardlinks",
		"unit:systemd-timesyncd.service",
	} {
		checks = append(checks, protocol.Check{ID: id, Status: protocol.StatusWarning})
	}
	plan, err := BuildSetupPlan(PlanInput{
		Mode: PlanApply, Profile: profile, Checks: checks,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(plan.Changes))
	for _, change := range plan.Changes {
		got = append(got, change.Object)
	}
	want := []string{
		"package:curl",
		"group:ohtools",
		"user:ohtools",
		"directory:/var/lib/ohtools",
		"file:/etc/ohtools/setup-state.yaml",
		"sysctl:fs.protected_hardlinks",
		"unit:systemd-timesyncd.service",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("change order = %#v, want %#v", got, want)
	}
}

func TestBuildUpgradePlanOmitsPackagesWithoutCachedUpgrade(t *testing.T) {
	t.Parallel()

	profile, err := ProfileByID("debian-12")
	if err != nil {
		t.Fatal(err)
	}
	checks := make([]protocol.Check, 0, len(profile.Packages))
	for _, name := range profile.Packages {
		checks = append(checks, protocol.Check{
			ID: "package:" + name, Status: protocol.StatusPass,
		})
	}
	plan, err := BuildSetupPlan(PlanInput{
		Mode: PlanUpgrade, Profile: profile, Checks: checks,
		CachedUpgrades: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("plan repeats an already-converged upgrade: %#v", plan)
	}
}
