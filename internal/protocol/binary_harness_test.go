package protocol_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	harnessOutputLimitExit = -100
	harnessTimeoutExit     = -101
)

func TestTrustedProtocolFixtureRealBinary(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	binary := filepath.Join(t.TempDir(), "protocol-fixture")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "./cmd/protocol-fixture")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v\n%s", err, output)
	}

	manifestJSON, stderr, exit := runFixture(t, binary, "manifest", nil)
	if exit != 0 || len(stderr) != 0 {
		t.Fatalf("manifest exit=%d stderr=%q", exit, stderr)
	}
	var manifest protocol.Manifest
	if err := json.Unmarshal(manifestJSON, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "protocol-fixture" || len(manifest.Commands) != 3 {
		t.Fatalf("manifest = %#v", manifest)
	}

	_, _, exit = runFixture(t, binary, "plan", []byte(`{"protocol_version":1,"unknown":true}`))
	if exit != protocol.ExitArguments {
		t.Fatalf("malformed invocation exit=%d", exit)
	}
	_, _, exit = runFixture(t, binary, "plan", bytes.Repeat([]byte("x"), (1<<20)+1))
	if exit != protocol.ExitArguments {
		t.Fatalf("oversized invocation exit=%d", exit)
	}

	outputInvocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "fixture-output-1",
		CommandPath:     []string{"fixture", "output"},
		Arguments:       []string{},
		Options:         map[string]any{"bytes": float64((1 << 20) + 1024)},
		Deadline:        time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
	}
	encodedOutput, err := json.Marshal(outputInvocation)
	if err != nil {
		t.Fatal(err)
	}
	largeOutput, stderr, exit := runFixture(t, binary, "execute", encodedOutput)
	if exit != harnessOutputLimitExit || len(largeOutput) > 1<<20 {
		t.Fatalf("output limit exit=%d stdout=%d stderr=%q", exit, len(largeOutput), stderr)
	}

	hangMarker := filepath.Join(t.TempDir(), "hang.lock")
	hangInvocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "fixture-hang-1",
		CommandPath:     []string{"fixture", "hang"},
		Arguments:       []string{hangMarker},
		Options:         map[string]any{},
		Deadline:        time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
	}
	encodedHang, err := json.Marshal(hangInvocation)
	if err != nil {
		t.Fatal(err)
	}
	_, _, exit = runFixtureWithLimits(
		t, binary, "execute", encodedHang, time.Second, 1<<20, 1<<20,
	)
	if exit != harnessTimeoutExit {
		t.Fatalf("hanging fixture exit=%d, want harness timeout", exit)
	}
	if err := os.Remove(hangMarker); err != nil {
		t.Fatalf("hanging fixture resource was not released after termination: %v", err)
	}

	statePath := filepath.Join(t.TempDir(), "state")
	invocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "fixture-request-1",
		CommandPath:     []string{"fixture", "apply"},
		Arguments:       []string{statePath},
		Options:         map[string]any{},
		Deadline:        time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(invocation)
	if err != nil {
		t.Fatal(err)
	}
	planJSON, stderr, exit := runFixture(t, binary, "plan", encoded)
	if exit != 0 || len(stderr) != 0 {
		t.Fatalf("plan exit=%d stderr=%q", exit, stderr)
	}
	var plan protocol.Plan
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 1 || !plan.RequiresConfirmation {
		t.Fatalf("initial plan = %#v", plan)
	}
	invocation.PlanDigest, err = protocol.PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ = json.Marshal(invocation)
	resultJSON, stderr, exit := runFixture(t, binary, "execute", encoded)
	if exit != 0 || len(stderr) != 0 {
		t.Fatalf("execute exit=%d stderr=%q", exit, stderr)
	}
	var result protocol.Result
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPass || len(result.Changes) != 1 {
		t.Fatalf("first result = %#v", result)
	}

	invocation.PlanDigest = ""
	encoded, _ = json.Marshal(invocation)
	planJSON, stderr, exit = runFixture(t, binary, "plan", encoded)
	if exit != 0 || len(stderr) != 0 {
		t.Fatalf("second plan exit=%d stderr=%q", exit, stderr)
	}
	if err := json.Unmarshal(planJSON, &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("second plan = %#v", plan)
	}
	invocation.PlanDigest, _ = protocol.PlanDigest(plan)
	encoded, _ = json.Marshal(invocation)
	resultJSON, stderr, exit = runFixture(t, binary, "execute", encoded)
	if exit != 0 || len(stderr) != 0 {
		t.Fatalf("second execute exit=%d stderr=%q", exit, stderr)
	}
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Changes) != 0 {
		t.Fatalf("second result = %#v", result)
	}

	invocation.PlanDigest = ""
	invocation.Deadline = time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano)
	encoded, _ = json.Marshal(invocation)
	_, _, exit = runFixture(t, binary, "plan", encoded)
	if exit != protocol.ExitTimeout {
		t.Fatalf("expired deadline exit=%d", exit)
	}
}

func runFixture(t *testing.T, binary, verb string, stdin []byte) ([]byte, []byte, int) {
	t.Helper()
	return runFixtureWithLimits(t, binary, verb, stdin, 5*time.Second, 1<<20, 1<<20)
}

func runFixtureWithLimits(
	t *testing.T,
	binary string,
	verb string,
	stdin []byte,
	timeout time.Duration,
	stdoutLimit int,
	stderrLimit int,
) ([]byte, []byte, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, binary, verb, "--protocol=1")
	command.Stdin = bytes.NewReader(stdin)
	stdout := boundedBuffer{limit: stdoutLimit, cancel: cancel}
	stderr := boundedBuffer{limit: stderrLimit, cancel: cancel}
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.WaitDelay = 500 * time.Millisecond
	err := command.Run()
	if stdout.exceeded || stderr.exceeded {
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			t.Fatal("output-limited fixture process was not reaped")
		}
		return stdout.Bytes(), stderr.Bytes(), harnessOutputLimitExit
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			t.Fatal("timed-out fixture process was not reaped")
		}
		return stdout.Bytes(), stderr.Bytes(), harnessTimeoutExit
	}
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), 0
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("run fixture: %v", err)
	}
	if strings.TrimSpace(stderr.String()) == "" {
		t.Fatalf("fixture failed without diagnostic: %v", err)
	}
	return stdout.Bytes(), stderr.Bytes(), exitError.ExitCode()
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
	cancel   context.CancelFunc
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining < 0 {
		remaining = 0
	}
	if len(value) > remaining {
		if remaining != 0 {
			_, _ = buffer.buffer.Write(value[:remaining])
		}
		buffer.exceeded = true
		buffer.cancel()
		return len(value), nil
	}
	return buffer.buffer.Write(value)
}

func (buffer *boundedBuffer) Bytes() []byte {
	return buffer.buffer.Bytes()
}

func (buffer *boundedBuffer) String() string {
	return buffer.buffer.String()
}
