package serversetup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

type PackageUpgrade struct {
	Name             string `json:"name"`
	CurrentVersion   string `json:"current_version"`
	CandidateVersion string `json:"candidate_version"`
}

type UpgradeBackend interface {
	PlanUpgrade(context.Context, Profile) ([]PackageUpgrade, error)
	ApplyUpgrade(context.Context, Profile, []PackageUpgrade) error
	VerifyUpgrades(context.Context, Profile, []PackageUpgrade) error
}

var aptUpgradeLine = regexp.MustCompile(
	`^Inst ([a-z0-9][a-z0-9.+-]*) \[([^]\s]+)\] \(([^)\s]+)`,
)

func (backend SystemBackend) PlanUpgrade(
	ctx context.Context,
	_ Profile,
) ([]PackageUpgrade, error) {
	var upgrades []PackageUpgrade
	err := backend.withIsolatedAPT(ctx, func(options []string) error {
		var err error
		upgrades, err = backend.planUpgradeWithAPTOptions(ctx, options)
		return err
	})
	return upgrades, err
}

func (backend SystemBackend) ApplyUpgrade(
	ctx context.Context,
	_ Profile,
	upgrades []PackageUpgrade,
) error {
	if len(upgrades) == 0 {
		return nil
	}
	for _, upgrade := range upgrades {
		if !packageName.MatchString(upgrade.Name) || upgrade.CandidateVersion == "" ||
			strings.ContainsAny(upgrade.CandidateVersion, "\x00\r\n\t ") {
			return errors.New("approved package upgrade is invalid")
		}
	}
	return backend.withIsolatedAPT(ctx, func(options []string) error {
		current, err := backend.planUpgradeWithAPTOptions(ctx, options)
		if err != nil {
			return err
		}
		if !slices.Equal(current, upgrades) {
			return errors.New("approved package upgrade candidates changed")
		}
		arguments := []string{"install", "-y", "--only-upgrade", "--no-remove"}
		arguments = append(arguments, options...)
		arguments = append(arguments, "--")
		for _, upgrade := range upgrades {
			arguments = append(arguments, upgrade.Name+"="+upgrade.CandidateVersion)
		}
		_, err = backend.run(ctx, execx.Spec{
			Program: "apt-get", Arguments: arguments,
			Environment: map[string]string{"DEBIAN_FRONTEND": "noninteractive"},
		})
		return err
	})
}

func (backend SystemBackend) planUpgradeWithAPTOptions(
	ctx context.Context,
	options []string,
) ([]PackageUpgrade, error) {
	environment := map[string]string{"DEBIAN_FRONTEND": "noninteractive"}
	update := append([]string{"update"}, options...)
	if _, err := backend.run(ctx, execx.Spec{
		Program: "apt-get", Arguments: update, Environment: environment,
	}); err != nil {
		return nil, err
	}
	simulate := []string{"--simulate", "upgrade", "--with-new-pkgs"}
	simulate = append(simulate, options...)
	output, err := backend.run(ctx, execx.Spec{
		Program: "apt-get", Arguments: simulate, Environment: environment,
	})
	if err != nil {
		return nil, err
	}
	return parsePackageUpgrades(output.Stdout)
}

func (backend SystemBackend) VerifyUpgrades(
	ctx context.Context,
	_ Profile,
	upgrades []PackageUpgrade,
) error {
	if len(upgrades) == 0 {
		return nil
	}
	arguments := []string{"--show", "--showformat=${binary:Package}\\t${Version}\\n", "--"}
	expected := make(map[string]string, len(upgrades))
	for _, upgrade := range upgrades {
		arguments = append(arguments, upgrade.Name)
		expected[upgrade.Name] = upgrade.CandidateVersion
	}
	output, err := backend.run(ctx, execx.Spec{
		Program: "dpkg-query", Arguments: arguments,
	})
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		name, version, found := strings.Cut(line, "\t")
		if found && expected[name] == strings.TrimSpace(version) {
			delete(expected, name)
		}
	}
	if len(expected) != 0 {
		return fmt.Errorf("%d approved package upgrade(s) were not installed exactly", len(expected))
	}
	return nil
}

func (backend SystemBackend) withIsolatedAPT(
	ctx context.Context,
	function func([]string) error,
) (returnErr error) {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	base := backend.Root
	if base == "" {
		base = "/tmp"
	}
	info, err := os.Lstat(base)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("trusted temporary directory is unsafe")
	}
	if err := validateManagedPath(base, info, true, backend.Root != ""); err != nil {
		return err
	}
	runDirectory, err := os.MkdirTemp(base, "ohtools-server-setup-apt-")
	if err != nil {
		return err
	}
	defer func() {
		cleanErr := os.RemoveAll(runDirectory)
		if cleanErr != nil {
			returnErr = errors.Join(returnErr, cleanErr)
		}
	}()
	lists := filepath.Join(runDirectory, "lists")
	archives := filepath.Join(runDirectory, "archives")
	for _, directory := range []string{lists, filepath.Join(lists, "partial"), archives, filepath.Join(archives, "partial")} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return err
		}
	}
	options := []string{
		"-o", "Dir::State::Lists=" + lists,
		"-o", "Dir::Cache::Archives=" + archives,
	}
	return function(options)
}

func parsePackageUpgrades(content []byte) ([]PackageUpgrade, error) {
	if len(content) > 10<<20 {
		return nil, errors.New("apt simulation output exceeds 10 MiB")
	}
	output := []PackageUpgrade{}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "Inst ") {
			continue
		}
		match := aptUpgradeLine.FindStringSubmatch(line)
		if len(match) != 4 {
			return nil, fmt.Errorf("invalid apt upgrade line %q", line)
		}
		if seen[match[1]] {
			return nil, fmt.Errorf("duplicate apt upgrade %q", match[1])
		}
		seen[match[1]] = true
		output = append(output, PackageUpgrade{
			Name: match[1], CurrentVersion: match[2], CandidateVersion: match[3],
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.Slice(output, func(first, second int) bool {
		return output[first].Name < output[second].Name
	})
	return output, nil
}

func (manager Manager) UpgradePlan(ctx context.Context) (protocol.Plan, error) {
	backend, ok := manager.Backend.(UpgradeBackend)
	if !ok {
		return protocol.Plan{}, protocol.ExitError{
			Code: protocol.ExitDependency, Err: errors.New("package upgrade backend is unavailable"),
		}
	}
	if manager.Platform.RequiresExtendedSupport {
		if err := manager.Backend.Entitled(ctx, manager.Platform); err != nil {
			return protocol.Plan{}, protocol.ExitError{Code: protocol.ExitDependency, Err: err}
		}
	}
	profile := Profile{
		Platform: manager.Platform,
		Config:   manager.Config,
		Items:    []Item{ItemPackages, ItemSecurityUpdates},
	}
	upgrades, err := backend.PlanUpgrade(ctx, profile)
	if err != nil {
		return protocol.Plan{}, err
	}
	sort.Slice(upgrades, func(first, second int) bool {
		return upgrades[first].Name < upgrades[second].Name
	})
	plan := protocol.Plan{
		Summary: "Install available ordinary package upgrades",
		Checks:  []protocol.Check{},
		Changes: []protocol.Change{},
		Risks: []string{
			"Package upgrades may restart services but never reboot the host",
		},
	}
	for _, upgrade := range upgrades {
		plan.Changes = append(plan.Changes, protocol.Change{
			Object: upgrade.Name, Action: "upgrade", Status: "planned",
			Details: map[string]any{
				"current_version":   upgrade.CurrentVersion,
				"candidate_version": upgrade.CandidateVersion,
			},
		})
	}
	return plan, nil
}

func (manager Manager) Upgrade(
	ctx context.Context,
	approved protocol.Plan,
) (protocol.Result, error) {
	started := manager.now()
	backend, ok := manager.Backend.(UpgradeBackend)
	if !ok {
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitDependency, Err: errors.New("package upgrade backend is unavailable"),
		}
	}
	upgrades := make([]PackageUpgrade, 0, len(approved.Changes))
	for _, change := range approved.Changes {
		if change.Action != "upgrade" || !packageName.MatchString(change.Object) {
			return protocol.Result{}, protocol.ExitError{
				Code: protocol.ExitArguments, Err: errors.New("approved upgrade plan is invalid"),
			}
		}
		current, currentOK := change.Details["current_version"].(string)
		candidate, candidateOK := change.Details["candidate_version"].(string)
		if !currentOK || !candidateOK || current == "" || candidate == "" {
			return protocol.Result{}, protocol.ExitError{
				Code: protocol.ExitArguments, Err: errors.New("approved upgrade versions are invalid"),
			}
		}
		upgrades = append(upgrades, PackageUpgrade{
			Name: change.Object, CurrentVersion: current, CandidateVersion: candidate,
		})
	}
	profile := Profile{
		Platform: manager.Platform,
		Config:   manager.Config,
		Items:    []Item{ItemPackages, ItemSecurityUpdates},
	}
	if len(upgrades) > 0 {
		if err := backend.ApplyUpgrade(ctx, profile, upgrades); err != nil {
			return normalizeResult(protocol.Result{
				Command: "setup upgrade", Status: protocol.StatusError,
				Timestamp:  manager.now().Format(time.RFC3339Nano),
				DurationMS: manager.now().Sub(started).Milliseconds(),
				Host:       manager.Host, Tool: manager.Tool,
				Errors: []protocol.StructuredError{{
					Kind: protocol.ErrorGeneral, Code: "setup_upgrade_failed", Message: err.Error(),
				}},
			}), nil
		}
	}
	if err := backend.VerifyUpgrades(ctx, profile, upgrades); err != nil {
		return normalizeResult(protocol.Result{
			Command: "setup upgrade", Status: protocol.StatusPartial,
			Timestamp:  manager.now().Format(time.RFC3339Nano),
			DurationMS: manager.now().Sub(started).Milliseconds(),
			Host:       manager.Host, Tool: manager.Tool,
			Errors: []protocol.StructuredError{{
				Kind: protocol.ErrorGeneral, Code: "setup_upgrade_verify_failed", Message: err.Error(),
			}},
		}), nil
	}
	changes := make([]protocol.Change, 0, len(upgrades))
	for _, upgrade := range upgrades {
		changes = append(changes, protocol.Change{
			Object: upgrade.Name, Action: "upgrade", Status: "completed",
			Details: map[string]any{
				"previous_version": upgrade.CurrentVersion,
				"current_version":  upgrade.CandidateVersion,
			},
		})
	}
	return normalizeResult(protocol.Result{
		Command: "setup upgrade", Status: protocol.StatusPass,
		Timestamp:  manager.now().Format(time.RFC3339Nano),
		DurationMS: manager.now().Sub(started).Milliseconds(),
		Host:       manager.Host, Tool: manager.Tool,
		Data: map[string]any{
			"operation": map[string]any{
				"changed": len(changes) > 0,
				"reason":  changeReason(len(changes)),
			},
		},
		Changes: changes,
	}), nil
}
