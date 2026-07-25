package serversetupreadiness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/serversetup"
)

type Report struct {
	SchemaVersion        string               `json:"schema_version"`
	Platform             serversetup.Platform `json:"platform"`
	InitialApplyStatus   protocol.Status      `json:"initial_apply_status"`
	InitialApplyChanges  int                  `json:"initial_apply_changes"`
	SecondApplyStatus    protocol.Status      `json:"second_apply_status"`
	SecondApplyChanges   int                  `json:"second_apply_changes"`
	DriftRepairStatus    protocol.Status      `json:"drift_repair_status"`
	DriftRepairChanges   int                  `json:"drift_repair_changes"`
	UpgradeStatus        protocol.Status      `json:"upgrade_status"`
	UpgradeChanges       int                  `json:"upgrade_changes"`
	SecondUpgradeStatus  protocol.Status      `json:"second_upgrade_status"`
	SecondUpgradeChanges int                  `json:"second_upgrade_changes"`
	ExactUpgradeApplied  bool                 `json:"exact_upgrade_applied"`
}

func Run(ctx context.Context, osRelease []byte, root string) (Report, error) {
	platform, err := serversetup.ResolvePlatform(osRelease)
	if err != nil {
		return Report{}, err
	}
	configPath := filepath.Join(root, "server-setup-base.yaml")
	if err := os.WriteFile(configPath, []byte(
		"enabled_items: [packages, sysctl]\n"+
			"packages: []\n"+
			"administrators: []\n"+
			"ssh_port: 22\n"+
			"manage_firewall: false\n",
	), 0o600); err != nil {
		return Report{}, err
	}
	backend := &fixtureBackend{
		drift: map[serversetup.Item]bool{
			serversetup.ItemPackages: true,
			serversetup.ItemSysctl:   true,
		},
		upgrades: []serversetup.PackageUpgrade{{
			Name: "fixture-package", CurrentVersion: "1.0.0", CandidateVersion: "1.0.1",
		}},
	}
	definition := serversetup.NewDefinition(serversetup.Options{
		Version: "1.0.0", ConfigPath: configPath,
		Platform: &platform, Backend: backend, Host: "readiness-fixture",
	})
	applyInvocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "readiness-apply",
		CommandPath:     []string{"setup", "apply"},
		Arguments:       []string{"sysctl"},
		Options:         map[string]any{},
	}
	initial, err := planAndExecute(ctx, definition, applyInvocation)
	if err != nil {
		return Report{}, err
	}
	applyInvocation.RequestID = "readiness-apply-second"
	second, err := planAndExecute(ctx, definition, applyInvocation)
	if err != nil {
		return Report{}, err
	}
	backend.drift[serversetup.ItemSysctl] = true
	applyInvocation.RequestID = "readiness-drift-repair"
	drift, err := planAndExecute(ctx, definition, applyInvocation)
	if err != nil {
		return Report{}, err
	}
	upgradeInvocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "readiness-upgrade",
		CommandPath:     []string{"setup", "upgrade"},
		Arguments:       []string{},
		Options:         map[string]any{},
	}
	upgrade, err := planAndExecute(ctx, definition, upgradeInvocation)
	if err != nil {
		return Report{}, err
	}
	upgradeInvocation.RequestID = "readiness-upgrade-second"
	secondUpgrade, err := planAndExecute(ctx, definition, upgradeInvocation)
	if err != nil {
		return Report{}, err
	}
	safeSystemUpgrade, err := exerciseSafeSystemUpgrade(ctx, platform, root)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		SchemaVersion:        "1",
		Platform:             platform,
		InitialApplyStatus:   initial.Status,
		InitialApplyChanges:  len(initial.Changes),
		SecondApplyStatus:    second.Status,
		SecondApplyChanges:   len(second.Changes),
		DriftRepairStatus:    drift.Status,
		DriftRepairChanges:   len(drift.Changes),
		UpgradeStatus:        upgrade.Status,
		UpgradeChanges:       len(upgrade.Changes),
		SecondUpgradeStatus:  secondUpgrade.Status,
		SecondUpgradeChanges: len(secondUpgrade.Changes),
		ExactUpgradeApplied:  backend.exactUpgradeApplied && safeSystemUpgrade,
	}
	if err := ValidateReport(report, platform.ID, platform.Version); err != nil {
		return Report{}, err
	}
	return report, nil
}

func exerciseSafeSystemUpgrade(
	ctx context.Context,
	platform serversetup.Platform,
	root string,
) (bool, error) {
	systemRoot := filepath.Join(root, "system-backend")
	if err := os.Mkdir(systemRoot, 0o700); err != nil {
		return false, err
	}
	var calls []execx.Spec
	backend := serversetup.SystemBackend{
		Root: systemRoot,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			if contains(spec.Arguments, "--simulate") {
				return execx.Output{Stdout: []byte(
					"Inst fixture-package [1.0.0] (1.0.1 fixture [amd64])\n",
				)}, nil
			}
			return execx.Output{}, nil
		}),
	}
	profile := serversetup.Profile{Platform: platform, Config: serversetup.DefaultConfig()}
	approved, err := backend.PlanUpgrade(ctx, profile)
	if err != nil {
		return false, err
	}
	if err := backend.ApplyUpgrade(ctx, profile, approved); err != nil {
		return false, err
	}
	installCalls := 0
	for _, call := range calls {
		if call.Program != "apt-get" || !hasIsolatedAPTOptions(call.Arguments) {
			return false, errors.New("system upgrade fixture escaped isolated APT options")
		}
		for _, argument := range call.Arguments {
			if strings.Contains(argument, "dist-upgrade") ||
				strings.Contains(argument, "autoremove") ||
				strings.Contains(argument, "reboot") {
				return false, fmt.Errorf("unsafe APT argument %q", argument)
			}
		}
		if contains(call.Arguments, "install") {
			installCalls++
			if !contains(call.Arguments, "fixture-package=1.0.1") {
				return false, errors.New("system upgrade fixture lost the exact package version")
			}
		}
	}
	return installCalls == 1, nil
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func hasIsolatedAPTOptions(arguments []string) bool {
	lists := false
	archives := false
	for index, argument := range arguments {
		if argument != "-o" || index+1 >= len(arguments) {
			continue
		}
		lists = lists || strings.HasPrefix(arguments[index+1], "Dir::State::Lists=")
		archives = archives ||
			strings.HasPrefix(arguments[index+1], "Dir::Cache::Archives=")
	}
	return lists && archives
}

func ValidateReport(report Report, expectedID string, expectedVersion string) error {
	if report.SchemaVersion != "1" ||
		report.Platform.ID != expectedID ||
		report.Platform.Version != expectedVersion {
		return errors.New("readiness report platform or schema mismatch")
	}
	for name, status := range map[string]protocol.Status{
		"initial apply":  report.InitialApplyStatus,
		"second apply":   report.SecondApplyStatus,
		"drift repair":   report.DriftRepairStatus,
		"upgrade":        report.UpgradeStatus,
		"second upgrade": report.SecondUpgradeStatus,
	} {
		if status != protocol.StatusPass {
			return fmt.Errorf("%s status is %s", name, status)
		}
	}
	if report.InitialApplyChanges == 0 ||
		report.SecondApplyChanges != 0 ||
		report.DriftRepairChanges != 1 ||
		report.UpgradeChanges == 0 ||
		report.SecondUpgradeChanges != 0 ||
		!report.ExactUpgradeApplied {
		return errors.New("readiness report does not prove idempotency and drift repair")
	}
	return nil
}

func planAndExecute(
	ctx context.Context,
	definition protocol.Definition,
	invocation protocol.Invocation,
) (protocol.Result, error) {
	plan, err := definition.Plan(ctx, invocation)
	if err != nil {
		return protocol.Result{}, err
	}
	digest, err := protocol.PlanDigest(plan)
	if err != nil {
		return protocol.Result{}, err
	}
	invocation.PlanDigest = digest
	return definition.Execute(ctx, invocation)
}

type fixtureBackend struct {
	drift               map[serversetup.Item]bool
	upgrades            []serversetup.PackageUpgrade
	exactUpgradeApplied bool
}

func (*fixtureBackend) Entitled(context.Context, serversetup.Platform) error {
	return nil
}

func (backend *fixtureBackend) Observe(
	_ context.Context,
	item serversetup.Item,
	_ serversetup.Profile,
) (serversetup.Observation, error) {
	return serversetup.Observation{
		Converged: !backend.drift[item],
		Summary:   string(item) + " fixture state",
	}, nil
}

func (backend *fixtureBackend) Apply(
	_ context.Context,
	item serversetup.Item,
	_ serversetup.Profile,
) error {
	backend.drift[item] = false
	return nil
}

func (backend *fixtureBackend) Verify(
	_ context.Context,
	item serversetup.Item,
	_ serversetup.Profile,
) error {
	if backend.drift[item] {
		return fmt.Errorf("%s fixture drift remains", item)
	}
	return nil
}

func (backend *fixtureBackend) PlanUpgrade(
	context.Context,
	serversetup.Profile,
) ([]serversetup.PackageUpgrade, error) {
	return append([]serversetup.PackageUpgrade(nil), backend.upgrades...), nil
}

func (backend *fixtureBackend) ApplyUpgrade(
	_ context.Context,
	_ serversetup.Profile,
	upgrades []serversetup.PackageUpgrade,
) error {
	backend.exactUpgradeApplied = reflect.DeepEqual(upgrades, backend.upgrades)
	if !backend.exactUpgradeApplied {
		return errors.New("fixture upgrade did not receive the exact approved set")
	}
	backend.upgrades = nil
	return nil
}

func (*fixtureBackend) VerifyUpgrades(
	context.Context,
	serversetup.Profile,
	[]serversetup.PackageUpgrade,
) error {
	return nil
}
