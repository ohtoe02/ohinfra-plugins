package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

var ErrNotFound = errors.New("trusted executable not found")

var programName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

type Resolver struct {
	Directories             []string
	RequireTrustedOwnership bool
}

func SystemResolver() Resolver {
	return Resolver{
		Directories:             []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"},
		RequireTrustedOwnership: true,
	}
}

func (resolver Resolver) Resolve(program string) (string, error) {
	if !programName.MatchString(program) || strings.HasPrefix(program, "-") ||
		strings.ContainsAny(program, `/\`) {
		return "", fmt.Errorf("invalid executable name %q", program)
	}
	if runtime.GOOS == "windows" && filepath.Ext(program) == "" {
		program += ".exe"
	}
	for _, directory := range resolver.Directories {
		absoluteDirectory, err := filepath.Abs(directory)
		if err != nil {
			continue
		}
		candidate := filepath.Join(absoluteDirectory, program)
		info, err := os.Stat(candidate)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
			continue
		}
		resolved := candidate
		if runtime.GOOS != "windows" {
			resolved, err = filepath.EvalSymlinks(candidate)
			if err != nil || !withinAnyDirectory(resolved, resolver.Directories) {
				continue
			}
		}
		if resolver.RequireTrustedOwnership {
			if err := validateTrustedExecutable(resolved); err != nil {
				continue
			}
		}
		return resolved, nil
	}
	return "", fmt.Errorf("%w: %s", ErrNotFound, program)
}

type Spec struct {
	Program     string
	Arguments   []string
	Environment map[string]string
	Stdin       []byte
	StdoutLimit int64
	StderrLimit int64
}

type Output struct {
	Path            string
	Stdout          []byte
	Stderr          []byte
	ExitCode        int
	Duration        time.Duration
	TimedOut        bool
	StdoutTruncated bool
	StderrTruncated bool
}

type Runner interface {
	Run(context.Context, Spec) (Output, error)
}

type RunnerFunc func(context.Context, Spec) (Output, error)

func (function RunnerFunc) Run(ctx context.Context, spec Spec) (Output, error) {
	return function(ctx, spec)
}

type OSRunner struct {
	Resolver Resolver
}

func (runner OSRunner) Run(ctx context.Context, spec Spec) (Output, error) {
	path, err := runner.Resolver.Resolve(spec.Program)
	if err != nil {
		return Output{}, err
	}
	stdout := newLimitedBuffer(spec.StdoutLimit)
	stderr := newLimitedBuffer(spec.StderrLimit)
	// #nosec G204 -- path is resolved from trusted directories and argv is direct.
	//nolint:noctx // cancellation kills the complete process group below.
	command := exec.Command(path, spec.Arguments...)
	command.Stdin = bytes.NewReader(spec.Stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = stableEnvironment(spec.Environment)
	prepareCommand(command)

	started := time.Now()
	if err := command.Start(); err != nil {
		return Output{Path: path}, fmt.Errorf("start %s: %w", path, err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()

	select {
	case waitErr := <-done:
		output := buildOutput(path, command, stdout, stderr, time.Since(started))
		var exitError *exec.ExitError
		if waitErr != nil && !errors.As(waitErr, &exitError) {
			return output, fmt.Errorf("wait for %s: %w", path, waitErr)
		}
		return output, nil
	case <-ctx.Done():
		_ = killCommand(command)
		<-done
		output := buildOutput(path, command, stdout, stderr, time.Since(started))
		output.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		return output, ctx.Err()
	}
}

func buildOutput(
	path string,
	command *exec.Cmd,
	stdout *limitedBuffer,
	stderr *limitedBuffer,
	duration time.Duration,
) Output {
	exitCode := -1
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	}
	return Output{
		Path: path, ExitCode: exitCode, Duration: duration,
		Stdout:          append([]byte(nil), stdout.Bytes()...),
		Stderr:          append([]byte(nil), stderr.Bytes()...),
		StdoutTruncated: stdout.Truncated(),
		StderrTruncated: stderr.Truncated(),
	}
}

func stableEnvironment(extra map[string]string) []string {
	values := map[string]string{
		"LANG": "C", "LC_ALL": "C", "PATH": "/usr/sbin:/usr/bin:/sbin:/bin",
	}
	for key, value := range extra {
		if validEnvironmentName(key) {
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	output := make([]string, 0, len(keys))
	for _, key := range keys {
		output = append(output, key+"="+values[key])
	}
	return output
}

func validEnvironmentName(value string) bool {
	if value == "" || strings.Contains(value, "=") {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func withinAnyDirectory(path string, directories []string) bool {
	for _, directory := range directories {
		absolute, err := filepath.Abs(directory)
		if err != nil {
			continue
		}
		relative, err := filepath.Rel(absolute, path)
		if err == nil && relative != ".." &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	limit     int64
	truncated bool
}

func newLimitedBuffer(limit int64) *limitedBuffer {
	if limit <= 0 {
		limit = 1 << 20
	}
	return &limitedBuffer{limit: limit}
}

func (buffer *limitedBuffer) Write(input []byte) (int, error) {
	originalLength := len(input)
	remaining := buffer.limit - int64(buffer.buffer.Len())
	if remaining <= 0 {
		buffer.truncated = true
		return originalLength, nil
	}
	if int64(len(input)) > remaining {
		input = input[:remaining]
		buffer.truncated = true
	}
	_, _ = buffer.buffer.Write(input)
	return originalLength, nil
}

func (buffer *limitedBuffer) Bytes() []byte {
	return buffer.buffer.Bytes()
}

func (buffer *limitedBuffer) Truncated() bool {
	return buffer.truncated
}
