package java

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

func TestDefinitionExposesOnlyApprovedCommands(t *testing.T) {
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
		{"java", "runtime"},
		{"java", "processes"},
		{"java", "inspect"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
	inspect := definition.Manifest.Commands[2]
	if len(inspect.Arguments) != 1 || inspect.Arguments[0].Name != "pid" ||
		!inspect.Arguments[0].Required || inspect.Arguments[0].Variadic {
		t.Fatalf("inspect arguments = %#v", inspect.Arguments)
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
		invoke([]string{"java", "runtime"}, []string{"extra"}, nil),
		invoke([]string{"java", "processes"}, nil, map[string]any{"all": true}),
		invoke([]string{"java", "unknown"}, nil, nil),
		invoke([]string{"java", "inspect"}, nil, nil),
		invoke([]string{"java", "inspect"}, []string{"1", "2"}, nil),
		invoke([]string{"java", "inspect"}, []string{"0"}, nil),
		invoke([]string{"java", "inspect"}, []string{"-1"}, nil),
		invoke([]string{"java", "inspect"}, []string{"+1"}, nil),
		invoke([]string{"java", "inspect"}, []string{" 1"}, nil),
		invoke([]string{"java", "inspect"}, []string{"1 "}, nil),
		invoke([]string{"java", "inspect"}, []string{"1e3"}, nil),
		invoke([]string{"java", "inspect"}, []string{"4194305"}, nil),
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

func TestNarrowCommandReturnsDependencyFailureWhenPrimaryToolIsMissing(t *testing.T) {
	definition := NewDefinition(Options{
		Version: "1.0.0", Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program != "jcmd" ||
				!reflect.DeepEqual(spec.Arguments, []string{"42", "VM.version"}) {
				t.Fatalf("unexpected missing-tool probe: %#v", spec)
			}
			return execx.Output{}, execx.ErrNotFound
		}),
	})
	_, err := definition.Execute(context.Background(),
		invoke([]string{"java", "inspect"}, []string{"42"}, nil))
	if exitCode(err) != protocol.ExitDependency ||
		!strings.Contains(err.Error(), "jcmd") {
		t.Fatalf("error = %v", err)
	}
}

func TestInspectReturnsPrivilegeFailureWithoutLeakingAttachDiagnostic(t *testing.T) {
	const secret = "password=hunter2"
	definition := NewDefinition(Options{
		Version: "1.0.0", Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{
				ExitCode: 1,
				Stderr:   []byte("Operation not permitted: " + secret),
			}, nil
		}),
	})
	_, err := definition.Execute(context.Background(),
		invoke([]string{"java", "inspect"}, []string{"42"}, nil))
	if exitCode(err) != protocol.ExitPrivilege {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("attach diagnostic leaked: %v", err)
	}
}

func TestRuntimeUsesFixedLocalVersionProbe(t *testing.T) {
	t.Setenv("JAVA_TOOL_OPTIONS", "-Dpassword=poisoned")
	t.Setenv("HTTPS_PROXY", "https://user:pass@proxy.invalid")
	var gotSpec execx.Spec
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		gotSpec = spec
		return execx.Output{Stderr: []byte(
			"openjdk version \"21.0.2\" 2024-01-16\n" +
				"OpenJDK Runtime Environment (build 21.0.2+13)\n" +
				"OpenJDK 64-Bit Server VM (build 21.0.2+13, mixed mode)\n",
		)}, nil
	})
	definition := NewDefinition(Options{
		Version: "1.0.0", Host: "server01", Root: t.TempDir(),
		Runner: runner, Now: func() time.Time { return fixedNow },
	})

	got, err := definition.Execute(context.Background(),
		invoke([]string{"java", "runtime"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	wantSpec := execx.Spec{
		Program: "java", Arguments: []string{"-version"},
		StdoutLimit: 64 << 10, StderrLimit: 64 << 10,
	}
	if !reflect.DeepEqual(gotSpec, wantSpec) {
		t.Fatalf("spec = %#v, want %#v", gotSpec, wantSpec)
	}
	runtimeInfo, ok := got.Data["runtime"].(RuntimeInfo)
	if !ok {
		t.Fatalf("runtime = %#v", got.Data["runtime"])
	}
	if runtimeInfo.Version != "21.0.2" ||
		!strings.Contains(runtimeInfo.Runtime, "OpenJDK Runtime Environment") ||
		!strings.Contains(runtimeInfo.VM, "OpenJDK 64-Bit Server VM") {
		t.Fatalf("runtime = %#v", runtimeInfo)
	}
	if got.Status != protocol.StatusPass || got.Command != "java runtime" {
		t.Fatalf("result = %#v", got)
	}
}

func TestRuntimeReturnsDependencyFailureForMissingOrInvalidJava(t *testing.T) {
	cases := []struct {
		name   string
		output execx.Output
		err    error
	}{
		{name: "missing", err: execx.ErrNotFound},
		{name: "malformed", output: execx.Output{Stderr: []byte("unexpected output\n")}},
		{name: "oversized", output: execx.Output{
			Stderr: []byte("openjdk version \"21\"\n"), StderrTruncated: true,
		}},
		{name: "nonzero", output: execx.Output{
			ExitCode: 1, Stderr: []byte("password=hunter2"),
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			definition := NewDefinition(Options{
				Version: "1.0.0", Root: t.TempDir(),
				Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
					return test.output, test.err
				}),
			})
			_, err := definition.Execute(context.Background(),
				invoke([]string{"java", "runtime"}, nil, nil))
			if exitCode(err) != protocol.ExitDependency {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("runtime diagnostic leaked: %v", err)
			}
		})
	}
}

func TestProcessesParsesOnlyLocalJavaProcesses(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/42/comm", "java\n")
	writeFixture(t, root, "proc/42/cmdline",
		"/usr/bin/java\x00-Dpassword=hunter2\x00-jar\x00/opt/app.jar\x00")
	writeFixture(t, root, "proc/7/comm", "sshd\n")
	writeFixture(t, root, "proc/7/cmdline", "/usr/sbin/sshd\x00-D\x00")
	definition := NewDefinition(Options{
		Version: "1.0.0", Host: "server01", Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			t.Fatal("java processes must not invoke an external command")
			return execx.Output{}, nil
		}),
		Now: func() time.Time { return fixedNow },
	})

	got, err := definition.Execute(context.Background(),
		invoke([]string{"java", "processes"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	processes, ok := got.Data["processes"].([]ProcessInfo)
	if !ok || len(processes) != 1 {
		t.Fatalf("processes = %#v", got.Data["processes"])
	}
	if processes[0].PID != 42 || processes[0].Executable != "java" {
		t.Fatalf("process = %#v", processes[0])
	}
	joined := strings.Join(processes[0].CommandLine, " ")
	if strings.Contains(joined, "hunter2") || !strings.Contains(joined, "********") {
		t.Fatalf("command line was not redacted: %q", joined)
	}
}

func TestProcessesReturnsInformationalResultWhenJavaIsAbsent(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proc"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := NewDefinition(Options{
		Version: "1.0.0", Root: root,
		Now: func() time.Time { return fixedNow },
	}).Execute(context.Background(), invoke([]string{"java", "processes"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.StatusPass || len(got.Checks) != 1 ||
		got.Checks[0].Status != protocol.StatusInfo ||
		len(got.Data["processes"].([]ProcessInfo)) != 0 {
		t.Fatalf("result = %#v", got)
	}
}

func TestAggregateCommandReturnsPartialWhenOptionalProbeIsMissing(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/42/comm", "java\n")
	writeFixture(t, root, "proc/42/cmdline", strings.Repeat("x", cmdlineLimit+1))
	definition := NewDefinition(Options{
		Version: "1.0.0", Root: root,
		Now: func() time.Time { return fixedNow },
	})

	got, err := definition.Execute(context.Background(),
		invoke([]string{"java", "processes"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.StatusPartial || len(got.Errors) != 1 ||
		got.Errors[0].Code != "process_metadata_unavailable" {
		t.Fatalf("result = %#v", got)
	}
	if processes := got.Data["processes"].([]ProcessInfo); len(processes) != 0 {
		t.Fatalf("oversized process metadata was accepted: %#v", processes)
	}
}

func TestProcessesRejectsSymlinkedProcEntryWithoutReadingTarget(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proc"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	writeFixture(t, outside, "42/comm", "java\n")
	writeFixture(t, outside, "42/cmdline", "/usr/bin/java\x00password=hunter2\x00")
	if err := os.Symlink(filepath.Join(outside, "42"), filepath.Join(root, "proc", "42")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	definition := NewDefinition(Options{
		Version: "1.0.0", Root: root,
		Now: func() time.Time { return fixedNow },
	})

	got, err := definition.Execute(context.Background(),
		invoke([]string{"java", "processes"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.StatusPartial || len(got.Errors) != 1 ||
		got.Errors[0].Code != "unsafe_proc_entry" {
		t.Fatalf("result = %#v", got)
	}
	if strings.Contains(fmt.Sprintf("%#v", got), "hunter2") {
		t.Fatalf("symlink target was read: %#v", got)
	}
}

func TestProcessesRejectsSymlinkedProcRoot(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "proc")); err != nil {
		t.Skipf("symlink creation is unavailable: %v", err)
	}
	definition := NewDefinition(Options{Version: "1.0.0", Root: root})
	_, err := definition.Execute(context.Background(),
		invoke([]string{"java", "processes"}, nil, nil))
	if exitCode(err) != protocol.ExitDependency {
		t.Fatalf("error = %v", err)
	}
}

func TestInspectUsesOnlyApprovedJcmdQueriesAndRedactsMetadata(t *testing.T) {
	var calls []execx.Spec
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		calls = append(calls, spec)
		switch spec.Arguments[1] {
		case "VM.version":
			return execx.Output{Stdout: []byte(
				"42:\nOpenJDK 64-Bit Server VM version 21.0.2+13\nJDK 21.0.2\n",
			)}, nil
		case "VM.command_line":
			return execx.Output{Stdout: []byte(
				"42:\nVM Arguments:\n" +
					"jvm_args: -Ddb.password=hunter2 -Xmx512m\n" +
					"java_command: app.jar --token=tok-secret\n",
			)}, nil
		case "VM.system_properties":
			return execx.Output{Stdout: []byte(
				"42:\n#Sun Jul 27 12:00:00 UTC 2026\n" +
					"java.home=/usr/lib/jvm/java-21\n" +
					"db.password=hunter2\n" +
					"service.url=https://user:pass@example.invalid/api\n",
			)}, nil
		default:
			t.Fatalf("unexpected jcmd operation: %#v", spec)
			return execx.Output{}, nil
		}
	})
	definition := NewDefinition(Options{
		Version: "1.0.0", Host: "server01", Root: t.TempDir(),
		Runner: runner, Now: func() time.Time { return fixedNow },
	})

	got, err := definition.Execute(context.Background(),
		invoke([]string{"java", "inspect"}, []string{"00042"}, nil))
	if err != nil {
		t.Fatal(err)
	}
	wantCalls := []execx.Spec{
		{Program: "jcmd", Arguments: []string{"42", "VM.version"},
			StdoutLimit: 256 << 10, StderrLimit: 64 << 10},
		{Program: "jcmd", Arguments: []string{"42", "VM.command_line"},
			StdoutLimit: 256 << 10, StderrLimit: 64 << 10},
		{Program: "jcmd", Arguments: []string{"42", "VM.system_properties"},
			StdoutLimit: 256 << 10, StderrLimit: 64 << 10},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
	info, ok := got.Data["process"].(InspectInfo)
	if !ok || info.PID != 42 || !strings.Contains(info.Version, "21.0.2") {
		t.Fatalf("process = %#v", got.Data["process"])
	}
	serialized := fmt.Sprintf("%#v", info)
	for _, secret := range []string{"hunter2", "tok-secret", "user:pass"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("secret %q leaked in %#v", secret, info)
		}
	}
	if !strings.Contains(serialized, "********") {
		t.Fatalf("redaction marker missing in %#v", info)
	}
}

func TestExternalValuesAreRedacted(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/9/comm", "java\n")
	writeFixture(t, root, "proc/9/cmdline",
		"/usr/bin/java\x00-Dapi.key=top-secret\x00--password\x00split-secret\x00"+
			"https://user:pass@example.invalid\x00")
	definition := NewDefinition(Options{
		Version: "1.0.0", Root: root,
		Now: func() time.Time { return fixedNow },
	})
	got, err := definition.Execute(context.Background(),
		invoke([]string{"java", "processes"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	serialized := fmt.Sprintf("%#v", got)
	for _, secret := range []string{"top-secret", "split-secret", "user:pass"} {
		if strings.Contains(serialized, secret) {
			t.Fatalf("secret %q leaked in %#v", secret, got)
		}
	}
}

func TestResultIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/20/comm", "java\n")
	writeFixture(t, root, "proc/20/cmdline", "/usr/bin/java\x00app20.jar\x00")
	writeFixture(t, root, "proc/3/comm", "java\n")
	writeFixture(t, root, "proc/3/cmdline", "/usr/bin/java\x00app3.jar\x00")
	definition := NewDefinition(Options{
		Version: "1.0.0", Host: "server01", Root: root,
		Now: func() time.Time { return fixedNow },
	})
	invocation := invoke([]string{"java", "processes"}, nil, nil)
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
	processes := first.Data["processes"].([]ProcessInfo)
	if len(processes) != 2 || processes[0].PID != 3 || processes[1].PID != 20 {
		t.Fatalf("process order = %#v", processes)
	}
}

func TestCommandsPropagateCancellation(t *testing.T) {
	for _, path := range [][]string{
		{"java", "runtime"},
		{"java", "inspect"},
	} {
		t.Run(strings.Join(path, "-"), func(t *testing.T) {
			definition := NewDefinition(Options{
				Version: "1.0.0", Root: t.TempDir(),
				Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
					return execx.Output{}, context.Canceled
				}),
			})
			arguments := []string(nil)
			if path[1] == "inspect" {
				arguments = []string{"42"}
			}
			got, err := definition.Execute(context.Background(), invoke(path, arguments, nil))
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
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, err := NewDefinition(Options{Version: "1.0.0", Root: root}).Execute(
		ctx, invoke([]string{"java", "processes"}, nil, nil),
	)
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, protocol.Result{}) {
		t.Fatalf("processes result=%#v error=%v", got, err)
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
