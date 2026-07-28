package serversetup

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestLocalMutationCommandsUseOnlyFixedCachedArgv(t *testing.T) {
	t.Parallel()

	specs := []execx.Spec{}
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		specs = append(specs, spec)
		if spec.Program == "dpkg-query" {
			return execx.Output{Stdout: []byte("1.2.3\n"), ExitCode: 0}, nil
		}
		return execx.Output{ExitCode: 0}, nil
	})
	commands := localMutationCommands{Runner: runner}
	if _, err := commands.Package(context.Background(), "curl", false); err != nil {
		t.Fatal(err)
	}
	if _, err := commands.Package(context.Background(), "curl", true); err != nil {
		t.Fatal(err)
	}
	if _, err := commands.Group(context.Background(), GroupSpec{Name: "ohtools", System: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := commands.User(context.Background(), UserSpec{
		Name: "ohtools", PrimaryGroup: "ohtools",
		Home: "/var/lib/ohtools", Shell: "/usr/sbin/nologin", System: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := commands.Sysctl(context.Background(), SysctlSpec{
		Key: "fs.protected_hardlinks", Value: "1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := commands.Unit(context.Background(), "systemd-timesyncd.service"); err != nil {
		t.Fatal(err)
	}

	for _, spec := range specs {
		if spec.Program == "sh" || spec.Program == "bash" || spec.Program == "sudo" {
			t.Fatalf("unsafe program = %#v", spec)
		}
		rendered := strings.Join(spec.Arguments, " ")
		if strings.Contains(rendered, "://") || strings.Contains(rendered, "download") &&
			!strings.Contains(rendered, "--no-download") {
			t.Fatalf("network-capable argv = %#v", spec)
		}
	}
	if !containsSpec(specs, execx.Spec{
		Program:   "apt-get",
		Arguments: []string{"--no-download", "--yes", "install", "--", "curl"},
	}) {
		t.Fatalf("cached install argv missing: %#v", specs)
	}
	if !containsSpec(specs, execx.Spec{
		Program: "apt-get",
		Arguments: []string{
			"--no-download", "--yes", "--only-upgrade", "install", "--", "curl",
		},
	}) {
		t.Fatalf("cached upgrade argv missing: %#v", specs)
	}
}

func TestDefinitionUpgradePlanUsesOnlyCurrentCachedUpgradeState(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	cached := []string{"curl"}
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
		UpgradeProbe: CachedUpgradeProbeFunc(
			func(_ context.Context, packages []string) ([]string, error) {
				if !reflect.DeepEqual(packages, fixture.profile.Packages) {
					t.Fatalf("packages = %#v", packages)
				}
				return append([]string(nil), cached...), nil
			},
		),
	})
	invocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     []string{"setup", "upgrade"},
	}
	first, err := definition.Plan(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Changes) != 1 ||
		first.Changes[0].Object != "package:curl" ||
		first.Changes[0].Action != "upgrade-cached" {
		t.Fatalf("first plan = %#v", first)
	}

	cached = []string{}
	second, err := definition.Plan(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Changes) != 0 {
		t.Fatalf("second plan repeats consumed cached upgrade: %#v", second)
	}
}

func TestLocalCachedUpgradeProbeUsesNoDownloadAndFiltersCompiledPackages(t *testing.T) {
	t.Parallel()

	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		want := []string{
			"--no-download", "--simulate", "--only-upgrade", "install", "--",
			"ca-certificates", "curl", "jq",
		}
		if spec.Program != "apt-get" || !reflect.DeepEqual(spec.Arguments, want) {
			t.Fatalf("probe argv = %#v", spec)
		}
		return execx.Output{Stdout: []byte(
			"Inst curl [1.0] (2.0 local-cache)\n" +
				"Inst attacker [1.0] (2.0 local-cache)\n",
		), ExitCode: 0}, nil
	})
	probe := localCachedUpgradeProbe{Runner: runner}
	got, err := probe.CachedUpgrades(
		context.Background(), []string{"ca-certificates", "curl", "jq"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"curl"}) {
		t.Fatalf("cached upgrades = %#v", got)
	}
}

func containsSpec(specs []execx.Spec, want execx.Spec) bool {
	for _, spec := range specs {
		if spec.Program == want.Program && reflect.DeepEqual(spec.Arguments, want.Arguments) {
			return true
		}
	}
	return false
}
