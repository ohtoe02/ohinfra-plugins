package network

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
		{"network", "overview"},
		{"network", "interfaces"},
		{"network", "routes"},
		{"network", "listeners"},
		{"tls", "inspect"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
	tlsCommand := definition.Manifest.Commands[4]
	if len(tlsCommand.Arguments) != 1 || !tlsCommand.Arguments[0].Required ||
		tlsCommand.Arguments[0].Variadic {
		t.Fatalf("tls arguments = %#v", tlsCommand.Arguments)
	}
	if err := protocol.ValidateManifest(definition.Manifest); err != nil {
		t.Fatalf("invalid manifest: %v", err)
	}
	if definition.Plan == nil {
		t.Fatal("read-only plan is missing")
	}
}

func TestDefinitionRejectsUnknownArgumentsAndOptions(t *testing.T) {
	definition := testDefinition(t, t.TempDir(), nil)
	cases := []protocol.Invocation{
		invoke([]string{"network", "overview"}, []string{"extra"}, nil),
		invoke([]string{"network", "interfaces"}, nil, map[string]any{"all": true}),
		invoke([]string{"network", "unknown"}, nil, nil),
		invoke([]string{"tls", "inspect"}, nil, nil),
		invoke([]string{"tls", "inspect"}, []string{"a", "b"}, nil),
	}
	for _, invocation := range cases {
		if _, err := definition.Execute(context.Background(), invocation); exitCode(err) != protocol.ExitArguments {
			t.Fatalf("%v args=%v options=%v: error = %v", invocation.CommandPath,
				invocation.Arguments, invocation.Options, err)
		}
		if _, err := definition.Plan(context.Background(), invocation); exitCode(err) != protocol.ExitArguments {
			t.Fatalf("plan %v: error = %v", invocation.CommandPath, err)
		}
	}
}

func TestAggregateCommandReturnsPartialWhenOptionalProbeIsMissing(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/net/dev", `Inter-|   Receive                                                |  Transmit
 face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed
  eth0: 100 1 0 0 0 0 0 0 200 2 0 0 0 0 0 0
`)
	writeFixture(t, root, "proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n"+
		"eth0\t00000000\t010200C0\t0003\t0\t0\t10\t00000000\n")
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		if spec.Program != "ss" || !reflect.DeepEqual(spec.Arguments, []string{"-H", "-l", "-n", "-t", "-u"}) {
			t.Fatalf("unexpected fallback: %#v", spec)
		}
		return execx.Output{}, execx.ErrNotFound
	})
	definition := testDefinition(t, root, runner)

	got, err := definition.Execute(context.Background(),
		invoke([]string{"network", "overview"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.StatusPartial {
		t.Fatalf("status = %s, result = %#v", got.Status, got)
	}
	if len(got.Errors) != 1 || got.Errors[0].Kind != protocol.ErrorDependency ||
		got.Errors[0].Dependency != "ss" {
		t.Fatalf("errors = %#v", got.Errors)
	}
	if got.Data["interfaces"] == nil || got.Data["routes"] == nil {
		t.Fatalf("usable evidence was discarded: %#v", got.Data)
	}
}

func TestNarrowCommandReturnsDependencyFailureWhenPrimaryToolIsMissing(t *testing.T) {
	root := t.TempDir()
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		if spec.Program != "ss" {
			t.Fatalf("program = %q", spec.Program)
		}
		return execx.Output{}, execx.ErrNotFound
	})
	definition := testDefinition(t, root, runner)
	_, err := definition.Execute(context.Background(),
		invoke([]string{"network", "listeners"}, nil, nil))
	if exitCode(err) != protocol.ExitDependency {
		t.Fatalf("error = %v", err)
	}
}

func TestNarrowCommandsPropagateCancellationAndDeadline(t *testing.T) {
	for _, path := range [][]string{
		{"network", "interfaces"},
		{"network", "routes"},
		{"network", "listeners"},
	} {
		for _, fatal := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(strings.Join(path, "-")+"/"+fatal.Error(), func(t *testing.T) {
				definition := testDefinition(t, t.TempDir(), execx.RunnerFunc(
					func(context.Context, execx.Spec) (execx.Output, error) {
						return execx.Output{}, fatal
					}))
				got, err := definition.Execute(context.Background(), invoke(path, nil, nil))
				if !errors.Is(err, fatal) {
					t.Fatalf("error = %v, want %v", err, fatal)
				}
				if exitCode(err) == protocol.ExitDependency {
					t.Fatalf("cancellation was reclassified as dependency: %v", err)
				}
				if !reflect.DeepEqual(got, protocol.Result{}) {
					t.Fatalf("fatal error returned a result: %#v", got)
				}
			})
		}
	}
}

func TestOverviewPropagatesCancellationAndDeadline(t *testing.T) {
	for _, fatal := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(fatal.Error(), func(t *testing.T) {
			definition := testDefinition(t, t.TempDir(), execx.RunnerFunc(
				func(context.Context, execx.Spec) (execx.Output, error) {
					return execx.Output{}, fatal
				}))
			got, err := definition.Execute(context.Background(),
				invoke([]string{"network", "overview"}, nil, nil))
			if !errors.Is(err, fatal) {
				t.Fatalf("error = %v, want %v", err, fatal)
			}
			if !reflect.DeepEqual(got, protocol.Result{}) {
				t.Fatalf("fatal error returned a partial result: %#v", got)
			}
		})
	}
}

func TestOverviewPropagatesListenerDeadline(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/net/dev", `Inter-| Receive | Transmit
 face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed
  eth0: 10 0 0 0 0 0 0 0 20 0 0 0 0 0 0 0
`)
	writeFixture(t, root, "proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n")
	definition := testDefinition(t, root, execx.RunnerFunc(
		func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program != "ss" {
				t.Fatalf("unexpected program: %q", spec.Program)
			}
			return execx.Output{}, context.DeadlineExceeded
		}))
	got, err := definition.Execute(context.Background(),
		invoke([]string{"network", "overview"}, nil, nil))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(got, protocol.Result{}) {
		t.Fatalf("fatal error returned a partial result: %#v", got)
	}
}

func TestCollectorsUseFixedLiteralArgvFallbacks(t *testing.T) {
	root := t.TempDir()
	var calls []execx.Spec
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		calls = append(calls, spec)
		switch spec.Program {
		case "ip":
			if reflect.DeepEqual(spec.Arguments, []string{"-j", "address", "show"}) {
				return execx.Output{Stdout: []byte(`[{"ifname":"eth0","operstate":"UP","address":"00:11:22:33:44:55","addr_info":[{"family":"inet","local":"192.0.2.10","prefixlen":24}]}]`)}, nil
			}
			return execx.Output{Stdout: []byte(`[{"dst":"default","gateway":"192.0.2.1","dev":"eth0","metric":10}]`)}, nil
		case "ss":
			return execx.Output{Stdout: []byte("tcp LISTEN 0 128 127.0.0.1:22 0.0.0.0:*\n")}, nil
		default:
			t.Fatalf("unexpected program %q", spec.Program)
			return execx.Output{}, nil
		}
	})
	definition := testDefinition(t, root, runner)
	for _, path := range [][]string{
		{"network", "interfaces"}, {"network", "routes"}, {"network", "listeners"},
	} {
		if _, err := definition.Execute(context.Background(), invoke(path, nil, nil)); err != nil {
			t.Fatalf("%v: %v", path, err)
		}
	}
	want := []execx.Spec{
		{Program: "ip", Arguments: []string{"-j", "address", "show"},
			StdoutLimit: commandOutputLimit, StderrLimit: commandErrorLimit},
		{Program: "ip", Arguments: []string{"-j", "route", "show", "table", "all"},
			StdoutLimit: commandOutputLimit, StderrLimit: commandErrorLimit},
		{Program: "ss", Arguments: []string{"-H", "-l", "-n", "-t", "-u"},
			StdoutLimit: commandOutputLimit, StderrLimit: commandErrorLimit},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestMalformedPrimaryOutputFallsBackWithoutPanicking(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/net/dev", "not a proc net dev row\n")
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		if spec.Program != "ip" {
			t.Fatalf("program = %q", spec.Program)
		}
		return execx.Output{Stdout: []byte(`[{"ifname":"lo","operstate":"UNKNOWN"}]`)}, nil
	})
	definition := testDefinition(t, root, runner)
	got, err := definition.Execute(context.Background(),
		invoke([]string{"network", "interfaces"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.StatusPass {
		t.Fatalf("result = %#v", got)
	}
}

func TestCollectorsReadProcPIDNetWhenProcNetIsUnavailable(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, fmt.Sprintf("proc/%d/net/dev", os.Getpid()), `Inter-| Receive | Transmit
 face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed
  eth0: 10 0 0 0 0 0 0 0 20 0 0 0 0 0 0 0
`)
	writeFixture(t, root, "sys/class/net/eth0/operstate", "up\n")
	definition := testDefinition(t, root, execx.RunnerFunc(
		func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			t.Fatalf("unexpected command fallback: %#v", spec)
			return execx.Output{}, nil
		}))
	got, err := definition.Execute(context.Background(),
		invoke([]string{"network", "interfaces"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	interfaces := got.Data["interfaces"].([]Interface)
	if len(interfaces) != 1 || interfaces[0].Name != "eth0" {
		t.Fatalf("interfaces = %#v", interfaces)
	}
}

func TestInterfacesUseBoundedSysAttributes(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/net/dev", `Inter-| Receive | Transmit
 face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed
  eth0: 10 0 0 0 0 0 0 0 20 0 0 0 0 0 0 0
`)
	writeFixture(t, root, "sys/class/net/eth0/operstate", "UP\n")
	writeFixture(t, root, "sys/class/net/eth0/address", "00:11:22:33:44:55\n")
	definition := testDefinition(t, root, nil)
	got, err := definition.Execute(context.Background(),
		invoke([]string{"network", "interfaces"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	interfaces := got.Data["interfaces"].([]Interface)
	if len(interfaces) != 1 || interfaces[0].State != "up" ||
		interfaces[0].HardwareAddr != "00:11:22:33:44:55" {
		t.Fatalf("interfaces = %#v", interfaces)
	}
}

func TestProcInterfacesRejectUnsafeNames(t *testing.T) {
	_, err := parseProcInterfaces([]byte(`Inter-| Receive | Transmit
 face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed
  ../../secret: 10 0 0 0 0 0 0 0 20 0 0 0 0 0 0 0
`))
	if err == nil {
		t.Fatal("accepted traversal-shaped interface name")
	}
}

func TestParsersRejectTrailingJSONValues(t *testing.T) {
	if _, err := parseIPInterfaces([]byte(
		`[{"ifname":"eth0","operstate":"UP"}] {"extra":true}`)); err == nil {
		t.Fatal("accepted trailing JSON value")
	}
	if _, err := parseIPRoutes([]byte(
		`[{"dst":"default","dev":"eth0"}] garbage`)); err == nil {
		t.Fatal("accepted trailing JSON garbage")
	}
}

func TestExternalValuesAreRedacted(t *testing.T) {
	root := t.TempDir()
	runner := execx.RunnerFunc(func(_ context.Context, _ execx.Spec) (execx.Output, error) {
		return execx.Output{Stdout: []byte(`[{"ifname":"eth0","operstate":"password=hunter2"}]`)}, nil
	})
	definition := testDefinition(t, root, runner)
	got, err := definition.Execute(context.Background(),
		invoke([]string{"network", "interfaces"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(render(got.Data)), "hunter2") {
		t.Fatalf("result leaked a secret: %#v", got.Data)
	}
}

func TestExternalStderrIsNeverReturnedInErrorDetails(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/net/dev", `Inter-| Receive | Transmit
 face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed
  eth0: 10 0 0 0 0 0 0 0 20 0 0 0 0 0 0 0
`)
	writeFixture(t, root, "proc/net/route", "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n")
	definition := testDefinition(t, root, execx.RunnerFunc(
		func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program != "ss" {
				t.Fatalf("unexpected program: %q", spec.Program)
			}
			return execx.Output{
				ExitCode: 1,
				Stderr:   []byte("password=hunter2 raw external stderr"),
			}, nil
		}))
	got, err := definition.Execute(context.Background(),
		invoke([]string{"network", "overview"}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ToLower(render(got))
	if strings.Contains(text, "hunter2") || strings.Contains(text, "raw external stderr") {
		t.Fatalf("result leaked raw stderr: %#v", got)
	}
}

func TestResultIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/net/dev", `Inter-| Receive | Transmit
 face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed
  zed0: 2 0 0 0 0 0 0 0 3 0 0 0 0 0 0 0
  alpha0: 1 0 0 0 0 0 0 0 4 0 0 0 0 0 0 0
`)
	definition := testDefinition(t, root, execx.RunnerFunc(
		func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{}, execx.ErrNotFound
		}))
	invocation := invoke([]string{"network", "interfaces"}, nil, nil)
	first, err := definition.Execute(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	second, err := definition.Execute(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("results differ:\n%#v\n%#v", first, second)
	}
	interfaces := first.Data["interfaces"].([]Interface)
	if !sort.SliceIsSorted(interfaces, func(i, j int) bool {
		return interfaces[i].Name < interfaces[j].Name
	}) {
		t.Fatalf("interfaces are not sorted: %#v", interfaces)
	}
}

func TestParseProcListenersNormalizesIPv4(t *testing.T) {
	got, err := parseProcSockets("tcp", []byte(
		"  sl  local_address rem_address   st\n"+
			"   0: 0100007F:0016 00000000:0000 0A\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].LocalAddress != "127.0.0.1" ||
		got[0].Port != 22 || got[0].State != "listen" {
		t.Fatalf("listeners = %#v", got)
	}
}

func testDefinition(t *testing.T, root string, runner execx.Runner) protocol.Definition {
	t.Helper()
	return NewDefinition(Options{
		Version: "1.0.0", Commit: "abc", BuildDate: "2026-07-27",
		Host: "server01", Root: root, Runner: runner, Now: func() time.Time { return fixedNow },
	})
}

func invoke(path, arguments []string, options map[string]any) protocol.Invocation {
	if arguments == nil {
		arguments = []string{}
	}
	if options == nil {
		options = map[string]any{}
	}
	return protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     path, Arguments: arguments, Options: options,
	}
}

func exitCode(err error) int {
	var failure protocol.ExitError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return 0
}

func writeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func render(value any) string {
	return fmt.Sprintf("%#v", value)
}
