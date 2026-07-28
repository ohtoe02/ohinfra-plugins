package serversetup

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
)

type CachedUpgradeProbe interface {
	CachedUpgrades(context.Context, []string) ([]string, error)
}

type CachedUpgradeProbeFunc func(context.Context, []string) ([]string, error)

func (function CachedUpgradeProbeFunc) CachedUpgrades(
	ctx context.Context,
	packages []string,
) ([]string, error) {
	return function(ctx, packages)
}

type localCachedUpgradeProbe struct {
	Runner execx.Runner
}

func (upgradeProbe localCachedUpgradeProbe) CachedUpgrades(
	ctx context.Context,
	packages []string,
) ([]string, error) {
	approved := make(map[string]struct{}, len(packages))
	for _, name := range packages {
		if !compiledName.MatchString(name) {
			return nil, errors.New("invalid compiled package name")
		}
		approved[name] = struct{}{}
	}
	arguments := []string{
		"--no-download", "--simulate", "--only-upgrade", "install", "--",
	}
	arguments = append(arguments, packages...)
	output, err := (probe.Local{Runner: upgradeProbe.Runner}).Run(ctx, probe.Command{
		Program: "apt-get", Arguments: arguments,
		StdoutLimit: 1 << 20, StderrLimit: 64 << 10,
	})
	if err != nil {
		return nil, err
	}
	if output.ExitCode != 0 {
		return nil, fmt.Errorf("apt-get cached-upgrade probe failed with exit code %d", output.ExitCode)
	}
	found := map[string]struct{}{}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "Inst" {
			continue
		}
		if _, ok := approved[fields[1]]; ok {
			found[fields[1]] = struct{}{}
		}
	}
	upgrades := make([]string, 0, len(found))
	for _, name := range packages {
		if _, ok := found[name]; ok {
			upgrades = append(upgrades, name)
		}
	}
	return upgrades, nil
}
