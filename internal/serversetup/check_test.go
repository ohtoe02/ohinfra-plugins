package serversetup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestCheckCoversEveryCompiledResource(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	result, err := fixture.checker(t, healthyRunner(t)).Run(
		context.Background(), fixture.profile, DefaultConfig(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPass {
		t.Fatalf("status = %s, errors = %#v, checks = %#v", result.Status, result.Errors, result.Checks)
	}

	got := map[string]protocol.Status{}
	for _, check := range result.Checks {
		got[check.ID] = check.Status
	}
	for _, id := range []string{
		"package:ca-certificates", "package:curl", "package:jq",
		"group:ohtools", "user:ohtools",
		"directory:/etc/ohtools", "directory:/etc/ohtools/plugins",
		"directory:/var/lib/ohtools", "directory:/var/log/ohtools",
		"file:/etc/ohtools/setup-state.yaml",
		"sysctl:fs.protected_hardlinks", "sysctl:fs.protected_symlinks",
		"unit:systemd-timesyncd.service",
	} {
		if got[id] != protocol.StatusPass {
			t.Fatalf("check %q status = %q; all checks = %#v", id, got[id], result.Checks)
		}
	}
}

func TestCheckReportsResourceDrift(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	if err := os.WriteFile(fixture.path("etc/ohtools/setup-state.yaml"), []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.path("proc/sys/fs/protected_symlinks"), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(fixture.path("var/log/ohtools")); err != nil {
		t.Fatal(err)
	}

	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "dpkg-query":
			return execx.Output{
				Stdout:   []byte("ca-certificates\tinstalled\njq\tinstalled\n"),
				ExitCode: 1,
			}, nil
		case "systemctl":
			return execx.Output{Stdout: []byte("disabled\n"), ExitCode: 1}, nil
		default:
			t.Fatalf("unexpected program %q", spec.Program)
			return execx.Output{}, nil
		}
	})

	result, err := fixture.checker(t, runner).Run(context.Background(), fixture.profile, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusWarning {
		t.Fatalf("status = %s, checks = %#v", result.Status, result.Checks)
	}
	for _, id := range []string{
		"package:curl",
		"directory:/var/log/ohtools",
		"file:/etc/ohtools/setup-state.yaml",
		"sysctl:fs.protected_symlinks",
		"unit:systemd-timesyncd.service",
	} {
		if statusFor(result.Checks, id) != protocol.StatusWarning {
			t.Fatalf("check %q did not report drift: %#v", id, result.Checks)
		}
	}
}

func TestCheckReportsOwnerAndGroupDrift(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	checker := fixture.checker(t, healthyRunner(t))
	checker.Identity = FileIdentityReaderFunc(func(path string, _ os.FileInfo) (FileIdentity, error) {
		normalized := filepath.ToSlash(path)
		if strings.HasSuffix(normalized, "/var/lib/ohtools") {
			return FileIdentity{UID: 0, GID: 0}, nil
		}
		if strings.HasSuffix(normalized, "/etc/ohtools/setup-state.yaml") {
			return FileIdentity{UID: 0, GID: 991}, nil
		}
		return expectedFixtureIdentity(path), nil
	})

	result, err := checker.Run(context.Background(), fixture.profile, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{
		"directory:/var/lib/ohtools",
		"file:/etc/ohtools/setup-state.yaml",
	} {
		if statusFor(result.Checks, id) != protocol.StatusWarning {
			t.Fatalf("check %q did not report ownership drift: %#v", id, result.Checks)
		}
	}
}

func TestCheckRejectsSymlinkedIntermediateDirectoryComponent(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(filepath.Join(outside, "ohtools"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, fixture.path("opt")); err != nil {
		t.Skipf("symlink fixture is unavailable: %v", err)
	}
	fixture.profile.Directories = append(fixture.profile.Directories, DirectorySpec{
		Path: "/opt/ohtools", Mode: 0o755, Owner: "root", Group: "root",
	})

	result, err := fixture.checker(t, healthyRunner(t)).Run(
		context.Background(), fixture.profile, DefaultConfig(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if statusFor(result.Checks, "directory:/opt/ohtools") != protocol.StatusWarning {
		t.Fatalf("symlinked directory component was followed: %#v", result.Checks)
	}
}

func TestCheckTreatsConfinedPathSymlinkAsDrift(t *testing.T) {
	t.Parallel()

	checker := Checker{
		Root: t.TempDir(),
		Confinement: PathConfinementFunc(
			func(string, string) (string, os.FileInfo, error) {
				return "", nil, probe.ErrSymlink
			},
		),
	}
	check, failure := checker.checkDirectory(
		DirectorySpec{
			Path: "/opt/ohtools", Mode: 0o755, Owner: "root", Group: "root",
		},
		accountDatabase{
			groups: map[string]int{"root": 0},
			users:  map[string]accountEntry{"root": {id: 0}},
		},
		FileIdentityReaderFunc(func(string, os.FileInfo) (FileIdentity, error) {
			t.Fatal("identity must not be read through a symlinked component")
			return FileIdentity{}, nil
		}),
	)
	if failure != nil || check.Status != protocol.StatusWarning {
		t.Fatalf("check = %#v, failure = %#v", check, failure)
	}
}

func expectedFixtureIdentity(path string) FileIdentity {
	normalized := filepath.ToSlash(path)
	switch {
	case strings.HasSuffix(normalized, "/var/lib/ohtools"):
		return FileIdentity{UID: 991, GID: 991}
	case strings.HasSuffix(normalized, "/var/log/ohtools"):
		return FileIdentity{UID: 0, GID: 991}
	default:
		return FileIdentity{UID: 0, GID: 0}
	}
}

func TestCheckReturnsPartialForUnprivilegedInspection(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "dpkg-query":
			return healthyPackageOutput(), nil
		case "systemctl":
			return execx.Output{}, os.ErrPermission
		default:
			return execx.Output{}, errors.New("unexpected program")
		}
	})

	result, err := fixture.checker(t, runner).Run(context.Background(), fixture.profile, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPartial {
		t.Fatalf("status = %s, errors = %#v, checks = %#v", result.Status, result.Errors, result.Checks)
	}
	if statusFor(result.Checks, "unit:systemd-timesyncd.service") != protocol.StatusSkipped {
		t.Fatalf("unit check = %#v", result.Checks)
	}
	if len(result.Errors) != 1 || result.Errors[0].Kind != protocol.ErrorPrivilege {
		t.Fatalf("errors = %#v", result.Errors)
	}
}

func TestExternalValuesAreRedacted(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		if spec.Program == "dpkg-query" {
			return execx.Output{}, errors.New("password=package-secret")
		}
		return execx.Output{Stdout: []byte("enabled\n")}, nil
	})

	result, err := fixture.checker(t, runner).Run(context.Background(), fixture.profile, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	rendered := resultText(result)
	if strings.Contains(rendered, "package-secret") {
		t.Fatalf("result leaks external value: %s", rendered)
	}
}

func TestResultIsDeterministic(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	checker := fixture.checker(t, healthyRunner(t))
	first, err := checker.Run(context.Background(), fixture.profile, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	second, err := checker.Run(context.Background(), fixture.profile, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("results differ:\nfirst  %#v\nsecond %#v", first, second)
	}
}

func TestAggregateCommandReturnsPartialWhenOptionalProbeIsMissing(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		if spec.Program == "dpkg-query" {
			return execx.Output{}, execx.ErrNotFound
		}
		return execx.Output{Stdout: []byte("enabled\n")}, nil
	})

	result, err := fixture.checker(t, runner).Run(context.Background(), fixture.profile, DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPartial {
		t.Fatalf("status = %s, errors = %#v", result.Status, result.Errors)
	}
	if len(result.Errors) != 1 || result.Errors[0].Kind != protocol.ErrorDependency {
		t.Fatalf("errors = %#v", result.Errors)
	}
	for _, name := range fixture.profile.Packages {
		if statusFor(result.Checks, "package:"+name) != protocol.StatusSkipped {
			t.Fatalf("package checks = %#v", result.Checks)
		}
	}
}

type checkFixture struct {
	root    string
	profile Profile
	now     time.Time
}

func newCheckFixture(t *testing.T, profileID string) checkFixture {
	t.Helper()
	profile, err := ProfileByID(profileID)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeFixtureFile(t, root, "etc/os-release",
		"ID="+profile.PlatformID+"\nVERSION_ID=\""+profile.VersionID+"\"\n", 0o644)
	writeFixtureFile(t, root, "etc/passwd",
		"root:x:0:0:root:/root:/bin/bash\n"+
			"ohtools:x:991:991:ohtools:/var/lib/ohtools:/usr/sbin/nologin\n", 0o644)
	writeFixtureFile(t, root, "etc/group",
		"root:x:0:\nohtools:x:991:\n", 0o644)
	for _, directory := range profile.Directories {
		path := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(directory.Path, "/")))
		if err := os.MkdirAll(path, os.FileMode(directory.Mode)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, os.FileMode(directory.Mode)); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range profile.ManagedFiles {
		writeFixtureFile(t, root, strings.TrimPrefix(file.Path, "/"), string(file.Content), os.FileMode(file.Mode))
	}
	for _, sysctl := range profile.Sysctls {
		writeFixtureFile(t, root, "proc/sys/"+strings.ReplaceAll(sysctl.Key, ".", "/"), sysctl.Value+"\n", 0o600)
	}
	return checkFixture{root: root, profile: profile, now: time.Unix(100, 0).UTC()}
}

func (fixture checkFixture) checker(t *testing.T, runner execx.Runner) Checker {
	t.Helper()
	return Checker{
		Root: fixture.root, Runner: runner, Host: "server01",
		Now:  func() time.Time { return fixture.now },
		Tool: protocol.Tool{Name: Name, Version: "1.0.0"},
		Identity: FileIdentityReaderFunc(
			func(path string, _ os.FileInfo) (FileIdentity, error) {
				return expectedFixtureIdentity(path), nil
			},
		),
	}
}

func (fixture checkFixture) path(relative string) string {
	return filepath.Join(fixture.root, filepath.FromSlash(relative))
}

func writeFixtureFile(t *testing.T, root, relative, content string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func healthyRunner(t *testing.T) execx.Runner {
	t.Helper()
	return execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "dpkg-query":
			if len(spec.Arguments) < 4 || spec.Arguments[0] != "-W" ||
				spec.Arguments[2] != "--" {
				t.Fatalf("unsafe dpkg-query argv = %#v", spec.Arguments)
			}
			return healthyPackageOutput(), nil
		case "systemctl":
			if !reflect.DeepEqual(spec.Arguments,
				[]string{"is-enabled", "--", "systemd-timesyncd.service"}) {
				t.Fatalf("unsafe systemctl argv = %#v", spec.Arguments)
			}
			return execx.Output{Stdout: []byte("enabled\n"), ExitCode: 0}, nil
		default:
			t.Fatalf("unexpected program %q", spec.Program)
			return execx.Output{}, nil
		}
	})
}

func healthyPackageOutput() execx.Output {
	return execx.Output{Stdout: []byte(
		"ca-certificates\tinstalled\ncurl\tinstalled\njq\tinstalled\n",
	), ExitCode: 0}
}

func statusFor(checks []protocol.Check, id string) protocol.Status {
	for _, check := range checks {
		if check.ID == id {
			return check.Status
		}
	}
	return ""
}

func resultText(result protocol.Result) string {
	var output strings.Builder
	for _, check := range result.Checks {
		output.WriteString(check.Summary)
	}
	for _, failure := range result.Errors {
		output.WriteString(failure.Message)
	}
	return output.String()
}
