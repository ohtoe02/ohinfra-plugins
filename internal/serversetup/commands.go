package serversetup

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
)

type MutationUndo func(context.Context) error

type MutationCommandAdapter interface {
	Package(context.Context, string, bool) (MutationUndo, error)
	Group(context.Context, GroupSpec) (MutationUndo, error)
	User(context.Context, UserSpec) (MutationUndo, error)
	Sysctl(context.Context, SysctlSpec) (MutationUndo, error)
	Unit(context.Context, string) (MutationUndo, error)
}

type localMutationCommands struct {
	Runner execx.Runner
}

var (
	compiledName    = regexp.MustCompile(`^[a-z][a-z0-9+.-]{0,127}$`)
	compiledUnit    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9@_.:-]{0,254}$`)
	compiledSysctl  = regexp.MustCompile(`^[a-z][a-z0-9_.]{0,254}$`)
	compiledVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.+:~_-]{0,254}$`)
)

func (commands localMutationCommands) Package(
	ctx context.Context,
	name string,
	upgrade bool,
) (MutationUndo, error) {
	if !compiledName.MatchString(name) {
		return nil, errors.New("invalid compiled package name")
	}
	if !upgrade {
		if err := commands.run(ctx, "apt-get",
			"--no-download", "--yes", "install", "--", name); err != nil {
			return nil, err
		}
		return func(undoContext context.Context) error {
			return commands.run(undoContext, "apt-get", "--yes", "remove", "--", name)
		}, nil
	}

	versionOutput, err := commands.output(ctx, "dpkg-query",
		"-W", "-f=${Version}\n", "--", name)
	if err != nil {
		return nil, err
	}
	version := strings.TrimSpace(string(versionOutput.Stdout))
	if !compiledVersion.MatchString(version) {
		return nil, errors.New("installed package version is unavailable for rollback")
	}
	if err := commands.run(ctx, "apt-get",
		"--no-download", "--yes", "--only-upgrade", "install", "--", name); err != nil {
		return nil, err
	}
	return func(undoContext context.Context) error {
		return commands.run(
			undoContext, "apt-get", "--no-download", "--yes", "install", "--", name+"="+version,
		)
	}, nil
}

func (commands localMutationCommands) Group(
	ctx context.Context,
	spec GroupSpec,
) (MutationUndo, error) {
	if !compiledName.MatchString(spec.Name) || !spec.System {
		return nil, errors.New("invalid compiled group")
	}
	if err := commands.run(ctx, "groupadd", "--system", "--", spec.Name); err != nil {
		return nil, err
	}
	return func(undoContext context.Context) error {
		return commands.run(undoContext, "groupdel", "--", spec.Name)
	}, nil
}

func (commands localMutationCommands) User(
	ctx context.Context,
	spec UserSpec,
) (MutationUndo, error) {
	if !compiledName.MatchString(spec.Name) || !compiledName.MatchString(spec.PrimaryGroup) ||
		!spec.System || !strings.HasPrefix(spec.Home, "/") || !strings.HasPrefix(spec.Shell, "/") {
		return nil, errors.New("invalid compiled user")
	}
	if err := commands.run(
		ctx, "useradd", "--system", "--gid", spec.PrimaryGroup,
		"--home-dir", spec.Home, "--shell", spec.Shell, "--no-create-home", "--", spec.Name,
	); err != nil {
		return nil, err
	}
	return func(undoContext context.Context) error {
		return commands.run(undoContext, "userdel", "--", spec.Name)
	}, nil
}

func (commands localMutationCommands) Sysctl(
	ctx context.Context,
	spec SysctlSpec,
) (MutationUndo, error) {
	if !compiledSysctl.MatchString(spec.Key) || strings.ContainsAny(spec.Value, " \t\r\n=") {
		return nil, errors.New("invalid compiled sysctl")
	}
	oldOutput, err := commands.output(ctx, "sysctl", "-n", "--", spec.Key)
	if err != nil {
		return nil, err
	}
	old := strings.TrimSpace(string(oldOutput.Stdout))
	if strings.ContainsAny(old, "\r\n=") {
		return nil, errors.New("current sysctl value is unsafe for rollback")
	}
	if err := commands.run(ctx, "sysctl", "-w", "--", spec.Key+"="+spec.Value); err != nil {
		return nil, err
	}
	return func(undoContext context.Context) error {
		return commands.run(undoContext, "sysctl", "-w", "--", spec.Key+"="+old)
	}, nil
}

func (commands localMutationCommands) Unit(
	ctx context.Context,
	unit string,
) (MutationUndo, error) {
	if !compiledUnit.MatchString(unit) {
		return nil, errors.New("invalid compiled unit")
	}
	stateOutput, err := commands.output(ctx, "systemctl", "is-enabled", "--", unit)
	if err != nil {
		return nil, err
	}
	wasEnabled := strings.TrimSpace(string(stateOutput.Stdout)) == "enabled"
	if err := commands.run(ctx, "systemctl", "enable", "--", unit); err != nil {
		return nil, err
	}
	return func(undoContext context.Context) error {
		if wasEnabled {
			return nil
		}
		return commands.run(undoContext, "systemctl", "disable", "--", unit)
	}, nil
}

func (commands localMutationCommands) run(
	ctx context.Context,
	program string,
	arguments ...string,
) error {
	_, err := commands.output(ctx, program, arguments...)
	return err
}

func (commands localMutationCommands) output(
	ctx context.Context,
	program string,
	arguments ...string,
) (execx.Output, error) {
	output, err := (probe.Local{Runner: commands.Runner}).Run(ctx, probe.Command{
		Program: program, Arguments: arguments,
		StdoutLimit: 64 << 10, StderrLimit: 64 << 10,
	})
	if err != nil {
		return output, err
	}
	if output.ExitCode != 0 {
		return output, fmt.Errorf("%s failed with exit code %d", program, output.ExitCode)
	}
	return output, nil
}
