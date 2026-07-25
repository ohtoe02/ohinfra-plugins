package protocol_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
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
	if manifest.Name != "protocol-fixture" || len(manifest.Commands) != 2 {
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
	if exit != 0 || len(stderr) != 0 || len(largeOutput) <= 1<<20 {
		t.Fatalf("large output fixture exit=%d stdout=%d stderr=%q", exit, len(largeOutput), stderr)
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
	command := exec.Command(binary, verb, "--protocol=1")
	command.Stdin = bytes.NewReader(stdin)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
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
