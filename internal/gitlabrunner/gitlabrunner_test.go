package gitlabrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

var fixedNow = time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)

func TestDefinitionExposesOnlyApprovedReadOnlyCommands(t *testing.T) {
	definition := NewDefinition(Options{Version: "1.0.0"})
	got := make([][]string, 0, len(definition.Manifest.Commands))
	for _, command := range definition.Manifest.Commands {
		got = append(got, command.Path)
		if command.Category != protocol.CategoryDiagnostic ||
			command.RequiresRoot || command.RequiresForce ||
			command.SupportsDryRun || command.RequiresConfirmation {
			t.Fatalf("command is not read-only: %#v", command)
		}
	}
	want := [][]string{
		{"gitlab-runner", "status"},
		{"gitlab-runner", "config"},
		{"gitlab-runner", "executors"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
	if err := protocol.ValidateManifest(definition.Manifest); err != nil {
		t.Fatalf("invalid manifest: %v", err)
	}
	if definition.Plan == nil || definition.Execute == nil {
		t.Fatal("definition is missing plan or execute")
	}
}

func TestDefinitionRejectsUnknownArgumentsAndOptions(t *testing.T) {
	definition := NewDefinition(Options{Version: "1.0.0"})
	cases := []protocol.Invocation{
		invoke([]string{"gitlab-runner", "status"}, []string{"extra"}, nil),
		invoke([]string{"gitlab-runner", "config"}, nil, map[string]any{"file": "x"}),
		invoke([]string{"gitlab-runner", "executors"}, nil, map[string]any{"all": true}),
		invoke([]string{"gitlab-runner", "verify"}, nil, nil),
		invoke([]string{"gitlab-runner", "register"}, nil, nil),
		invoke([]string{"gitlab-runner", "run"}, nil, nil),
		invoke([]string{"gitlab-runner", "exec"}, nil, nil),
		invoke([]string{"gitlab-runner", "job"}, nil, nil),
	}
	for _, invocation := range cases {
		if _, err := definition.Plan(context.Background(), invocation); exitCode(err) != protocol.ExitArguments {
			t.Fatalf("plan path=%v args=%v options=%v: error = %v",
				invocation.CommandPath, invocation.Arguments, invocation.Options, err)
		}
		if _, err := definition.Execute(context.Background(), invocation); exitCode(err) != protocol.ExitArguments {
			t.Fatalf("execute path=%v args=%v options=%v: error = %v",
				invocation.CommandPath, invocation.Arguments, invocation.Options, err)
		}
	}
}

func TestStatusUsesOnlyFixedLocalVersionAndUnitProbes(t *testing.T) {
	t.Setenv("CONFIG_FILE", "/tmp/poison.toml")
	t.Setenv("CI_SERVER_URL", "https://user:pass@example.invalid")
	root := t.TempDir()
	writeFixture(t, root, "proc/42/comm", "gitlab-runner\n")
	writeFixture(t, root, "proc/42/status",
		"Name:\tgitlab-runner\nUid:\t998\t998\t998\t998\n")
	var calls []execx.Spec
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		calls = append(calls, spec)
		switch spec.Program {
		case "gitlab-runner":
			return execx.Output{Stdout: []byte(
				"Version:      17.9.1\nGit revision: deadbeef\nGO version: go1.24\n",
			)}, nil
		case "systemctl":
			return execx.Output{Stdout: []byte(
				"LoadState=loaded\nActiveState=active\nSubState=running\n",
			)}, nil
		default:
			t.Fatalf("unexpected command: %#v", spec)
			return execx.Output{}, nil
		}
	})
	got, err := NewDefinition(Options{
		Version: "1.0.0", Host: "server01", Root: root,
		Runner: runner, Now: func() time.Time { return fixedNow },
	}).Execute(context.Background(), invoke([]string{"gitlab-runner", "status"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []execx.Spec{
		{Program: "gitlab-runner", Arguments: []string{"--version"},
			StdoutLimit: 64 << 10, StderrLimit: 16 << 10},
		{Program: "systemctl", Arguments: []string{
			"show", "gitlab-runner.service",
			"--property=LoadState,ActiveState,SubState", "--no-pager",
		}, StdoutLimit: 32 << 10, StderrLimit: 16 << 10},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	status, ok := got.Data["status"].(StatusInfo)
	if !ok {
		t.Fatalf("status = %#v", got.Data["status"])
	}
	if status.Version != "17.9.1" || status.Unit.Active != "active" ||
		len(status.Processes) != 1 || status.Processes[0].PID != 42 ||
		status.Processes[0].UID != "998" {
		t.Fatalf("status = %#v", status)
	}
	if got.Command != "gitlab-runner status" ||
		got.Status != protocol.StatusPass {
		t.Fatalf("result = %#v", got)
	}
	for _, forbidden := range []string{"verify", "register", "run", "exec"} {
		if strings.Contains(fmt.Sprintf("%#v", calls), forbidden+" ") {
			t.Fatalf("forbidden operation %q was invoked: %#v", forbidden, calls)
		}
	}
}

func TestStatusDoesNotLeakMalformedProbeOutput(t *testing.T) {
	const secret = "registration_token=glrt-secret"
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proc"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		if spec.Program == "gitlab-runner" {
			return execx.Output{Stdout: []byte(secret), StdoutTruncated: true}, nil
		}
		return execx.Output{ExitCode: 1, Stderr: []byte(secret)}, nil
	})
	got, err := NewDefinition(Options{
		Version: "1.0.0", Root: root, Runner: runner,
		Now: func() time.Time { return fixedNow },
	}).Execute(context.Background(), invoke([]string{"gitlab-runner", "status"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.StatusPartial || len(got.Errors) != 2 {
		t.Fatalf("result = %#v", got)
	}
	if strings.Contains(fmt.Sprintf("%#v", got), "glrt-secret") {
		t.Fatalf("probe output leaked: %#v", got)
	}
}

func TestStatusOmitsUnknownUnitStates(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proc"), 0o755); err != nil {
		t.Fatal(err)
	}
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		if spec.Program == "gitlab-runner" {
			return execx.Output{Stdout: []byte("Version: 17.9.1\n")}, nil
		}
		return execx.Output{Stdout: []byte(
			"LoadState=loaded\nActiveState=tokenleak\nSubState=running\n",
		)}, nil
	})
	got, err := NewDefinition(Options{
		Version: "1.0.0", Root: root, Runner: runner,
		Now: func() time.Time { return fixedNow },
	}).Execute(context.Background(),
		invoke([]string{"gitlab-runner", "status"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	status := got.Data["status"].(StatusInfo)
	if status.Unit.Active != "" ||
		strings.Contains(fmt.Sprintf("%#v", got), "tokenleak") {
		t.Fatalf("unknown unit state was published: %#v", got)
	}
}

func TestStatusRejectsUnsafeOrOversizedProcMetadata(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "symlinked proc root", setup: func(t *testing.T, root string) {
			outside := t.TempDir()
			writeFixture(t, outside, "42/comm", "gitlab-runner\n")
			writeFixture(t, outside, "42/status",
				"Name:\tgitlab-runner\nToken:\trunner-proc-secret\n")
			if err := os.Symlink(outside, filepath.Join(root, "proc")); err != nil {
				t.Skipf("symlink creation is unavailable: %v", err)
			}
		}},
		{name: "oversized comm", setup: func(t *testing.T, root string) {
			writeFixture(t, root, "proc/42/comm",
				"gitlab-runner "+strings.Repeat("runner-proc-secret", 5000))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root)
			runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
				if spec.Program == "gitlab-runner" {
					return execx.Output{Stdout: []byte("Version: 17.9.1\n")}, nil
				}
				return execx.Output{Stdout: []byte(
					"LoadState=loaded\nActiveState=active\nSubState=running\n",
				)}, nil
			})
			got, err := NewDefinition(Options{
				Version: "1.0.0", Root: root, Runner: runner,
				Now: func() time.Time { return fixedNow },
			}).Execute(context.Background(),
				invoke([]string{"gitlab-runner", "status"}, nil, nil))
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != protocol.StatusPartial ||
				len(got.Errors) != 1 ||
				got.Errors[0].Code != "processes_unavailable" {
				t.Fatalf("result = %#v", got)
			}
			if strings.Contains(fmt.Sprintf("%#v", got), "runner-proc-secret") {
				t.Fatalf("unsafe proc metadata leaked: %#v", got)
			}
		})
	}
}

func TestStatusPublishesOnlyNumericProcUID(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/42/comm", "gitlab-runner\n")
	writeFixture(t, root, "proc/42/status",
		"Name:\tgitlab-runner\nUid:\ttoken=runner-uid-secret\t0\t0\t0\n")
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		if spec.Program == "gitlab-runner" {
			return execx.Output{Stdout: []byte("Version: 17.9.1\n")}, nil
		}
		return execx.Output{Stdout: []byte(
			"LoadState=loaded\nActiveState=active\nSubState=running\n",
		)}, nil
	})
	got, err := NewDefinition(Options{
		Version: "1.0.0", Root: root, Runner: runner,
		Now: func() time.Time { return fixedNow },
	}).Execute(context.Background(),
		invoke([]string{"gitlab-runner", "status"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	status := got.Data["status"].(StatusInfo)
	if len(status.Processes) != 1 || status.Processes[0].UID != "" ||
		strings.Contains(fmt.Sprintf("%#v", got), "runner-uid-secret") {
		t.Fatalf("unsafe UID was published: %#v", got)
	}
}

func TestConfigReturnsOnlyBoundedSafeTOMLMetadata(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "etc/gitlab-runner/config.toml", `
concurrent = 4
check_interval = 3
log_level = "info"
shutdown_timeout = 30

[[runners]]
name = "build-runner"
url = "https://user:password@gitlab.example.invalid/"
token = "glrt-secret"
executor = "docker"
limit = 2
request_concurrency = 1
environment = ["API_TOKEN=env-secret", "SAFE=value"]
tls-key-file = "/etc/gitlab-runner/private-key.pem"

[runners.docker]
image = "alpine:3.20"
privileged = false

[runners.cache.s3]
ServerAddress = "s3.example.invalid"
AccessKey = "cache-user"
SecretKey = "cache-secret"
`)
	got, err := NewDefinition(Options{
		Version: "1.0.0", Host: "server01", Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			t.Fatal("config must not invoke an external command")
			return execx.Output{}, nil
		}),
		Now: func() time.Time { return fixedNow },
	}).Execute(context.Background(), invoke([]string{"gitlab-runner", "config"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	metadata, ok := got.Data["config"].(ConfigMetadata)
	if !ok {
		t.Fatalf("config = %#v", got.Data["config"])
	}
	if metadata.Path != "/etc/gitlab-runner/config.toml" ||
		metadata.RunnerCount != 1 ||
		metadata.Settings.Concurrent != 4 ||
		metadata.Settings.CheckInterval != 3 ||
		metadata.Settings.LogLevel != "info" ||
		metadata.Settings.ShutdownTimeout != 30 ||
		len(metadata.Runners) != 1 {
		t.Fatalf("metadata = %#v", metadata)
	}
	runner := metadata.Runners[0]
	if runner.Executor != "docker" ||
		runner.Limit != 2 || runner.RequestConcurrency != 1 {
		t.Fatalf("runner = %#v", runner)
	}
	serialized := fmt.Sprintf("%#v", got)
	for _, secret := range []string{
		"password", "glrt-secret", "env-secret", "private-key.pem",
		"cache-user", "cache-secret", "user:password",
	} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("secret %q leaked in %#v", secret, got)
		}
	}
	if got.Status != protocol.StatusPass || got.Command != "gitlab-runner config" {
		t.Fatalf("result = %#v", got)
	}
}

func TestConfigNeverPublishesSecretBearingRunnerName(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "etc/gitlab-runner/config.toml", `
[[runners]]
name = "token=runner-name-secret"
executor = "shell"
`)
	got, err := NewDefinition(Options{
		Version: "1.0.0", Root: root,
		Now: func() time.Time { return fixedNow },
	}).Execute(context.Background(), invoke([]string{"gitlab-runner", "config"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	serialized := fmt.Sprintf("%#v", got)
	if strings.Contains(serialized, "runner-name-secret") ||
		strings.Contains(serialized, "token=") {
		t.Fatalf("runner name leaked: %#v", got)
	}
}

func TestConfigRejectsUnsafeOrMalformedTOML(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{name: "malformed", setup: func(t *testing.T, root string) {
			writeFixture(t, root, "etc/gitlab-runner/config.toml",
				"[[runners]\nexecutor = \"shell\"\n")
		}},
		{name: "malformed inline table", setup: func(t *testing.T, root string) {
			writeFixture(t, root, "etc/gitlab-runner/config.toml",
				"cache = { token = \"secret\" trailing }\n")
		}},
		{name: "non TOML string escape", setup: func(t *testing.T, root string) {
			writeFixture(t, root, "etc/gitlab-runner/config.toml",
				"[[runners]]\nname = \"bad\\x41escape\"\nexecutor = \"shell\"\n")
		}},
		{name: "duplicate", setup: func(t *testing.T, root string) {
			writeFixture(t, root, "etc/gitlab-runner/config.toml",
				"concurrent = 1\nconcurrent = 2\n")
		}},
		{name: "runner name with URL userinfo", setup: func(t *testing.T, root string) {
			writeFixture(t, root, "etc/gitlab-runner/config.toml",
				"[[runners]]\nname = \"https://user:pass@example.invalid\"\nexecutor = \"shell\"\n")
		}},
		{name: "oversized", setup: func(t *testing.T, root string) {
			writeFixture(t, root, "etc/gitlab-runner/config.toml",
				strings.Repeat("#", configFileLimit+1))
		}},
		{name: "symlink", setup: func(t *testing.T, root string) {
			outside := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(outside, []byte("concurrent = 1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "etc", "gitlab-runner", "config.toml")
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, target); err != nil {
				t.Skipf("symlink creation is unavailable: %v", err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.setup(t, root)
			got, err := NewDefinition(Options{
				Version: "1.0.0", Root: root,
			}).Execute(context.Background(),
				invoke([]string{"gitlab-runner", "config"}, nil, nil))
			if exitCode(err) != protocol.ExitConfiguration ||
				!reflect.DeepEqual(got, protocol.Result{}) {
				t.Fatalf("result=%#v error=%v", got, err)
			}
		})
	}
}

func TestExecutorsReturnsDeterministicSafeNamesAndCounts(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "etc/gitlab-runner/config.toml", `
[[runners]]
name = "zeta"
executor = "shell"
token = "glrt-first-secret"
shell = "bash"

[[runners]]
name = "alpha"
executor = "docker"
token = "glrt-second-secret"

[[runners]]
name = "beta"
executor = "shell"
clone_url = "https://user:pass@example.invalid/repo.git"
`)
	definition := NewDefinition(Options{
		Version: "1.0.0", Host: "server01", Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			t.Fatal("executors must not invoke an external command")
			return execx.Output{}, nil
		}),
		Now: func() time.Time { return fixedNow },
	})
	invocation := invoke([]string{"gitlab-runner", "executors"}, nil, nil)
	first, err := definition.Execute(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	second, err := definition.Execute(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("results differ:\nfirst=%#v\nsecond=%#v", first, second)
	}
	executors, ok := first.Data["executors"].([]ExecutorInfo)
	if !ok {
		t.Fatalf("executors = %#v", first.Data["executors"])
	}
	want := []ExecutorInfo{
		{Name: "docker", RunnerCount: 1},
		{Name: "shell", RunnerCount: 2},
	}
	if !reflect.DeepEqual(executors, want) {
		t.Fatalf("executors = %#v, want %#v", executors, want)
	}
	serialized := fmt.Sprintf("%#v", first)
	for _, secret := range []string{
		"glrt-first-secret", "glrt-second-secret", "user:pass",
	} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("secret %q leaked in %#v", secret, first)
		}
	}
}

func TestCommandsPropagateCancellation(t *testing.T) {
	for _, path := range [][]string{
		{"gitlab-runner", "status"},
		{"gitlab-runner", "config"},
		{"gitlab-runner", "executors"},
	} {
		t.Run(strings.Join(path, "-"), func(t *testing.T) {
			root := t.TempDir()
			if path[1] != "status" {
				writeFixture(t, root, "etc/gitlab-runner/config.toml",
					"[[runners]]\nexecutor = \"shell\"\n")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			got, err := NewDefinition(Options{
				Version: "1.0.0", Root: root,
				Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
					t.Fatal("canceled command started a process")
					return execx.Output{}, nil
				}),
			}).Execute(ctx, invoke(path, nil, nil))
			if !errors.Is(err, context.Canceled) ||
				!reflect.DeepEqual(got, protocol.Result{}) {
				t.Fatalf("result=%#v error=%v", got, err)
			}
		})
	}

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proc"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := NewDefinition(Options{
		Version: "1.0.0", Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{}, context.Canceled
		}),
	}).Execute(context.Background(),
		invoke([]string{"gitlab-runner", "status"}, nil, nil))
	if !errors.Is(err, context.Canceled) ||
		!reflect.DeepEqual(got, protocol.Result{}) {
		t.Fatalf("runner cancellation result=%#v error=%v", got, err)
	}
}

func invoke(path []string, arguments []string, options map[string]any) protocol.Invocation {
	if arguments == nil {
		arguments = []string{}
	}
	if options == nil {
		options = map[string]any{}
	}
	return protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     append([]string(nil), path...),
		Arguments:       append([]string(nil), arguments...),
		Options:         options,
	}
}

func exitCode(err error) int {
	var failure protocol.ExitError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return -1
}

func writeFixture(t *testing.T, root, relative, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
