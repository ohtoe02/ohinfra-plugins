package serversetupreadiness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/serversetup"
)

type Report struct {
	SchemaVersion            string               `json:"schema_version"`
	Platform                 serversetup.Platform `json:"platform"`
	InitialApplyStatus       protocol.Status      `json:"initial_apply_status"`
	InitialApplyChanges      int                  `json:"initial_apply_changes"`
	SecondApplyStatus        protocol.Status      `json:"second_apply_status"`
	SecondApplyChanges       int                  `json:"second_apply_changes"`
	DriftRepairStatus        protocol.Status      `json:"drift_repair_status"`
	DriftRepairChanges       int                  `json:"drift_repair_changes"`
	UpgradeStatus            protocol.Status      `json:"upgrade_status"`
	UpgradeChanges           int                  `json:"upgrade_changes"`
	SecondUpgradeStatus      protocol.Status      `json:"second_upgrade_status"`
	SecondUpgradeChanges     int                  `json:"second_upgrade_changes"`
	ExactUpgradeApplied      bool                 `json:"exact_upgrade_applied"`
	SystemApplyStatus        protocol.Status      `json:"system_apply_status"`
	SystemApplyChanges       int                  `json:"system_apply_changes"`
	SystemSecondApplyStatus  protocol.Status      `json:"system_second_apply_status"`
	SystemSecondApplyChanges int                  `json:"system_second_apply_changes"`
	DependencyProbesPassed   bool                 `json:"dependency_probes_passed"`
}

type BinarySmokeReport struct {
	SchemaVersion     string          `json:"schema_version"`
	InitialStatus     protocol.Status `json:"initial_status"`
	InitialChanges    int             `json:"initial_changes"`
	SecondStatus      protocol.Status `json:"second_status"`
	SecondChanges     int             `json:"second_changes"`
	SecondPlanChanges int             `json:"second_plan_changes"`
}

func RunBinarySmoke(
	ctx context.Context,
	binaryPath string,
) (BinarySmokeReport, error) {
	invocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "readiness-binary-apply",
		CommandPath:     []string{"setup", "apply"},
		Arguments:       []string{"shell-history"},
		Options:         map[string]any{},
	}
	var firstPlan protocol.Plan
	if err := runProtocolVerb(
		ctx,
		binaryPath,
		"plan",
		invocation,
		&firstPlan,
	); err != nil {
		return BinarySmokeReport{}, err
	}
	digest, err := protocol.PlanDigest(firstPlan)
	if err != nil {
		return BinarySmokeReport{}, err
	}
	invocation.PlanDigest = digest
	var initial protocol.Result
	if err := runProtocolVerb(
		ctx,
		binaryPath,
		"execute",
		invocation,
		&initial,
	); err != nil {
		return BinarySmokeReport{}, err
	}
	invocation.RequestID = "readiness-binary-apply-second"
	invocation.PlanDigest = ""
	var secondPlan protocol.Plan
	if err := runProtocolVerb(
		ctx,
		binaryPath,
		"plan",
		invocation,
		&secondPlan,
	); err != nil {
		return BinarySmokeReport{}, err
	}
	secondDigest, err := protocol.PlanDigest(secondPlan)
	if err != nil {
		return BinarySmokeReport{}, err
	}
	invocation.PlanDigest = secondDigest
	var second protocol.Result
	if err := runProtocolVerb(
		ctx,
		binaryPath,
		"execute",
		invocation,
		&second,
	); err != nil {
		return BinarySmokeReport{}, err
	}
	if len(secondPlan.Changes) != 0 {
		return BinarySmokeReport{}, errors.New(
			"second real-binary plan was not idempotent",
		)
	}
	if err := validateBinarySmoke(initial, second); err != nil {
		return BinarySmokeReport{}, err
	}
	return BinarySmokeReport{
		SchemaVersion:     "1",
		InitialStatus:     initial.Status,
		InitialChanges:    len(initial.Changes),
		SecondStatus:      second.Status,
		SecondChanges:     len(second.Changes),
		SecondPlanChanges: len(secondPlan.Changes),
	}, nil
}

func validateBinarySmoke(initial protocol.Result, second protocol.Result) error {
	if initial.Status != protocol.StatusPass ||
		len(initial.Changes) != 1 ||
		initial.Changes[0].Object != string(serversetup.ItemShellHistory) {
		return errors.New("first real-binary apply did not converge shell-history")
	}
	if second.Status != protocol.StatusPass || len(second.Changes) != 0 {
		return errors.New("second real-binary apply was not a successful no-op")
	}
	return nil
}

func runProtocolVerb(
	ctx context.Context,
	binaryPath string,
	verb string,
	invocation protocol.Invocation,
	output any,
) error {
	encoded, err := json.Marshal(invocation)
	if err != nil {
		return err
	}
	// #nosec G204 -- readiness executes the exact CI-built binary path supplied by the gate.
	command := exec.CommandContext(ctx, binaryPath, verb, "--protocol=1")
	command.Stdin = bytes.NewReader(encoded)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf(
			"%s protocol %s failed: %w: %s",
			binaryPath,
			verb,
			err,
			strings.TrimSpace(stderr.String()),
		)
	}
	if stdout.Len() == 0 || stdout.Len() > 1<<20 || stderr.Len() != 0 {
		return fmt.Errorf("%s protocol %s returned unsafe output", binaryPath, verb)
	}
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode %s protocol output: %w", verb, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s protocol output has trailing JSON", verb)
	}
	return nil
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
	systemInitial, systemSecond, err := exerciseSafeSystemBackend(
		ctx,
		platform,
		root,
	)
	if err != nil {
		return Report{}, err
	}
	dependencyProbesPassed, err := probeRuntimeDependencies(ctx)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		SchemaVersion:            "1",
		Platform:                 platform,
		InitialApplyStatus:       initial.Status,
		InitialApplyChanges:      len(initial.Changes),
		SecondApplyStatus:        second.Status,
		SecondApplyChanges:       len(second.Changes),
		DriftRepairStatus:        drift.Status,
		DriftRepairChanges:       len(drift.Changes),
		UpgradeStatus:            upgrade.Status,
		UpgradeChanges:           len(upgrade.Changes),
		SecondUpgradeStatus:      secondUpgrade.Status,
		SecondUpgradeChanges:     len(secondUpgrade.Changes),
		ExactUpgradeApplied:      backend.exactUpgradeApplied && safeSystemUpgrade,
		SystemApplyStatus:        systemInitial.Status,
		SystemApplyChanges:       len(systemInitial.Changes),
		SystemSecondApplyStatus:  systemSecond.Status,
		SystemSecondApplyChanges: len(systemSecond.Changes),
		DependencyProbesPassed:   dependencyProbesPassed,
	}
	if err := ValidateReport(report, platform.ID, platform.Version); err != nil {
		return Report{}, err
	}
	return report, nil
}

func exerciseSafeSystemBackend(
	ctx context.Context,
	platform serversetup.Platform,
	root string,
) (protocol.Result, protocol.Result, error) {
	systemRoot := filepath.Join(root, "system-apply-backend")
	if err := os.Mkdir(systemRoot, 0o700); err != nil {
		return protocol.Result{}, protocol.Result{}, err
	}
	if platform.ID == "debian" && platform.RequiresExtendedSupport {
		source := filepath.Join(
			systemRoot,
			"etc",
			"apt",
			"sources.list.d",
			"extended-lts.list",
		)
		if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
			return protocol.Result{}, protocol.Result{}, err
		}
		if err := os.WriteFile(
			source,
			[]byte("deb https://deb.freexian.com/extended-lts buster-lts main\n"),
			0o644,
		); err != nil {
			return protocol.Result{}, protocol.Result{}, err
		}
	}
	configPath := filepath.Join(systemRoot, "server-setup-base.yaml")
	if err := os.WriteFile(configPath, []byte(
		"enabled_items: [packages, shell-history]\n"+
			"packages: []\n"+
			"administrators: []\n"+
			"ssh_port: 22\n"+
			"manage_firewall: false\n",
	), 0o600); err != nil {
		return protocol.Result{}, protocol.Result{}, err
	}
	backend := serversetup.SystemBackend{
		Root: systemRoot,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch spec.Program {
			case "dpkg-query":
				return execx.Output{
					Stdout: []byte("sudo\tinstall ok installed\n"),
				}, nil
			case "pro":
				return execx.Output{Stdout: []byte(`{"attached":true}`)}, nil
			default:
				return execx.Output{}, fmt.Errorf(
					"unexpected safe system backend command %s",
					spec.Program,
				)
			}
		}),
	}
	definition := serversetup.NewDefinition(serversetup.Options{
		Version: "1.0.0", ConfigPath: configPath,
		Platform: &platform, Backend: backend, Host: "readiness-system-backend",
	})
	invocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "readiness-system-apply",
		CommandPath:     []string{"setup", "apply"},
		Arguments:       []string{"shell-history"},
		Options:         map[string]any{},
	}
	initial, err := planAndExecute(ctx, definition, invocation)
	if err != nil {
		return protocol.Result{}, protocol.Result{}, err
	}
	invocation.RequestID = "readiness-system-apply-second"
	second, err := planAndExecute(ctx, definition, invocation)
	if err != nil {
		return protocol.Result{}, protocol.Result{}, err
	}
	if _, err := os.Stat(filepath.Join(
		systemRoot,
		"etc",
		"profile.d",
		"ohtools-server-setup-history.sh",
	)); err != nil {
		return protocol.Result{}, protocol.Result{}, err
	}
	return initial, second, nil
}

func probeRuntimeDependencies(ctx context.Context) (bool, error) {
	if runtime.GOOS != "linux" {
		return true, nil
	}
	probes := []struct {
		path      string
		arguments []string
		required  bool
	}{
		{path: "/bin/sh", required: true},
		{path: "/usr/bin/apt-get", arguments: []string{"--version"}, required: true},
		{path: "/usr/bin/dpkg-query", arguments: []string{"--version"}, required: true},
		{path: "/usr/bin/systemctl", arguments: []string{"--version"}},
		{path: "/usr/sbin/sshd", arguments: []string{"-V"}},
		{path: "/usr/sbin/nft", arguments: []string{"--version"}},
		{path: "/usr/bin/nft", arguments: []string{"--version"}},
	}
	for _, probe := range probes {
		info, err := os.Stat(probe.path)
		if errors.Is(err, os.ErrNotExist) && !probe.required {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("probe runtime dependency %s: %w", probe.path, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return false, fmt.Errorf("runtime dependency %s is not executable", probe.path)
		}
		if len(probe.arguments) == 0 {
			continue
		}
		// #nosec G204 -- readiness probes fixed absolute system dependency paths.
		command := exec.CommandContext(ctx, probe.path, probe.arguments...)
		if output, err := command.CombinedOutput(); err != nil {
			return false, fmt.Errorf(
				"run dependency probe %s: %w: %s",
				probe.path,
				err,
				strings.TrimSpace(string(output)),
			)
		}
	}
	return true, nil
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
	for index, call := range calls {
		if call.Program != "apt-get" {
			return false, errors.New("system upgrade fixture invoked a foreign program")
		}
		if index == 0 {
			if !contains(call.Arguments, "--simulate") ||
				!contains(call.Arguments, "Debug::NoLocking=true") ||
				hasIsolatedAPTOptions(call.Arguments) {
				return false, errors.New("system upgrade planning was not read-only")
			}
			continue
		}
		if !hasIsolatedAPTOptions(call.Arguments) {
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
		"initial apply":       report.InitialApplyStatus,
		"second apply":        report.SecondApplyStatus,
		"drift repair":        report.DriftRepairStatus,
		"upgrade":             report.UpgradeStatus,
		"second upgrade":      report.SecondUpgradeStatus,
		"system apply":        report.SystemApplyStatus,
		"system second apply": report.SystemSecondApplyStatus,
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
		!report.ExactUpgradeApplied ||
		report.SystemApplyChanges == 0 ||
		report.SystemSecondApplyChanges != 0 ||
		!report.DependencyProbesPassed {
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
