package execx

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	switch os.Getenv("GO_WANT_EXECX_HELPER") {
	case "output":
		_, _ = os.Stdout.WriteString(strings.Repeat("o", 128))
		_, _ = os.Stderr.WriteString(strings.Repeat("e", 128))
		os.Exit(0)
	case "sleep":
		time.Sleep(5 * time.Second)
		os.Exit(0)
	case "exit":
		os.Exit(23)
	}
	os.Exit(m.Run())
}

func TestResolverIgnoresPoisonedPATHAndRejectsPathInput(t *testing.T) {
	trusted := t.TempDir()
	poisoned := t.TempDir()
	writeExecutable(t, filepath.Join(trusted, executableName("docker")))
	writeExecutable(t, filepath.Join(poisoned, executableName("docker")))
	t.Setenv("PATH", poisoned)

	resolver := Resolver{Directories: []string{trusted}}
	got, err := resolver.Resolve("docker")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(got) != trusted {
		t.Fatalf("resolved %q outside %q", got, trusted)
	}
	for _, input := range []string{"", "../docker", "/usr/bin/docker", `..\\docker`, "-docker", "bad name"} {
		if _, err := resolver.Resolve(input); err == nil {
			t.Fatalf("Resolve(%q) succeeded", input)
		}
	}
}

func TestRunnerCapsOutputAndKeepsArgvLiteral(t *testing.T) {
	program := copyTestExecutable(t)
	runner := OSRunner{Resolver: Resolver{Directories: []string{filepath.Dir(program)}}}
	output, err := runner.Run(context.Background(), Spec{
		Program:     filepath.Base(program),
		Arguments:   []string{"$(touch /tmp/not-executed)", "; rm -rf /"},
		Environment: map[string]string{"GO_WANT_EXECX_HELPER": "output"},
		StdoutLimit: 32,
		StderrLimit: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Stdout) != 32 || !output.StdoutTruncated ||
		len(output.Stderr) != 16 || !output.StderrTruncated {
		t.Fatalf("unexpected bounded output: %#v", output)
	}
}

func TestRunnerStopsAtDeadlineAndReportsNonZeroExit(t *testing.T) {
	program := copyTestExecutable(t)
	runner := OSRunner{Resolver: Resolver{Directories: []string{filepath.Dir(program)}}}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	output, err := runner.Run(ctx, Spec{
		Program: filepath.Base(program),
		Environment: map[string]string{
			"GO_WANT_EXECX_HELPER": "sleep",
		},
	})
	if !errors.Is(err, context.DeadlineExceeded) || !output.TimedOut {
		t.Fatalf("deadline error=%v output=%#v", err, output)
	}

	output, err = runner.Run(context.Background(), Spec{
		Program: filepath.Base(program),
		Environment: map[string]string{
			"GO_WANT_EXECX_HELPER": "exit",
		},
	})
	if err != nil || output.ExitCode != 23 {
		t.Fatalf("non-zero error=%v output=%#v", err, output)
	}
}

func TestResolverReportsMissingTrustedExecutable(t *testing.T) {
	t.Parallel()

	_, err := (Resolver{Directories: []string{t.TempDir()}}).Resolve("missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve error = %v", err)
	}
	if got := strings.Join(stableEnvironment(nil), "\n"); !strings.Contains(got, "PATH=/usr/sbin:/usr/bin:/sbin:/bin") {
		t.Fatalf("stable environment = %q", got)
	}
}

func copyTestExecutable(t *testing.T) string {
	t.Helper()
	source, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), executableName("execx-helper"))
	input, err := os.Open(source) // #nosec G304 -- returned by os.Executable.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700) // #nosec G304
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o700); err != nil {
		t.Fatal(err)
	}
	return target
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("test"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func executableName(name string) string {
	if filepath.Separator == '\\' {
		return name + ".exe"
	}
	return name
}
