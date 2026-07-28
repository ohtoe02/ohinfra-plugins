package probe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
)

func TestLocalReadAndLinesStayInsideRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "etc"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "etc", "example.conf"),
		[]byte("first\r\nsecond\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	local := Local{Root: root}

	got, err := local.Read(filepath.Join("etc", "example.conf"), 64)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "first\r\nsecond\n" {
		t.Fatalf("Read() = %q", got)
	}

	lines, err := local.Lines(filepath.Join("etc", "example.conf"), 64)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"first", "second"}; !reflect.DeepEqual(lines, want) {
		t.Fatalf("Lines() = %#v, want %#v", lines, want)
	}
}

func TestLocalReadRejectsUnsafeOrUnboundedInputs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	regular := filepath.Join(root, "regular")
	if err := os.WriteFile(regular, []byte("12345678"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "directory"), 0o700); err != nil {
		t.Fatal(err)
	}

	local := Local{Root: root}
	tests := []struct {
		name     string
		relative string
		limit    int64
		want     error
	}{
		{name: "empty path", relative: "", limit: 64, want: ErrInvalidPath},
		{name: "dot path", relative: ".", limit: 64, want: ErrInvalidPath},
		{name: "traversal", relative: filepath.Join("..", "outside"), limit: 64, want: ErrInvalidPath},
		{
			name:     "normalizing traversal",
			relative: "directory" + string(filepath.Separator) + ".." + string(filepath.Separator) + "regular",
			limit:    64,
			want:     ErrInvalidPath,
		},
		{name: "nested traversal", relative: filepath.Join("etc", "..", "..", "outside"), limit: 64, want: ErrInvalidPath},
		{name: "absolute", relative: filepath.Join(root, "regular"), limit: 64, want: ErrInvalidPath},
		{name: "zero limit", relative: "regular", limit: 0, want: ErrInvalidLimit},
		{name: "negative limit", relative: "regular", limit: -1, want: ErrInvalidLimit},
		{name: "directory", relative: "directory", limit: 64, want: ErrNotRegular},
		{name: "oversize", relative: "regular", limit: 7, want: ErrTooLarge},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := local.Read(test.relative, test.limit)
			if !errors.Is(err, test.want) {
				t.Fatalf("Read(%q, %d) error = %v, want %v", test.relative, test.limit, err, test.want)
			}
		})
	}
}

func TestLocalRejectsSymlinksAndMissingFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	local := Local{Root: root}
	if _, err := local.Read("missing", 64); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Read(missing) error = %v, want os.ErrNotExist", err)
	}
	exists, err := local.Exists("missing")
	if err != nil || exists {
		t.Fatalf("Exists(missing) = %v, %v", exists, err)
	}

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}

	if _, err := local.Read("link", 64); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Read(symlink) error = %v, want %v", err, ErrSymlink)
	}
}

func TestLocalRejectsSymlinkedPathComponent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}

	_, err := (Local{Root: root}).Read(filepath.Join("linked", "secret"), 64)
	if !errors.Is(err, ErrSymlink) {
		t.Fatalf("Read(path through symlink) error = %v, want %v", err, ErrSymlink)
	}
}

func TestLocalRunDelegatesLiteralArgvWithBoundedOutput(t *testing.T) {
	t.Parallel()

	var got execx.Spec
	local := Local{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			got = spec
			return execx.Output{Stdout: []byte("ok")}, nil
		}),
	}
	arguments := []string{"$(touch /tmp/not-executed)", "; rm -rf /"}
	output, err := local.Run(context.Background(), Command{
		Program:     "systemctl",
		Arguments:   arguments,
		StdoutLimit: 32,
		StderrLimit: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(output.Stdout) != "ok" {
		t.Fatalf("Run() output = %#v", output)
	}
	if got.Program != "systemctl" || !reflect.DeepEqual(got.Arguments, arguments) {
		t.Fatalf("delegated spec = %#v", got)
	}
	if got.StdoutLimit != 32 || got.StderrLimit != 16 {
		t.Fatalf("delegated limits = stdout %d stderr %d", got.StdoutLimit, got.StderrLimit)
	}
}

func TestLocalRunAppliesSafeDefaultsAndRejectsExecutablePaths(t *testing.T) {
	t.Parallel()

	calls := 0
	local := Local{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls++
			if spec.StdoutLimit != DefaultCommandOutputLimit ||
				spec.StderrLimit != DefaultCommandOutputLimit {
				t.Fatalf("default limits = stdout %d stderr %d", spec.StdoutLimit, spec.StderrLimit)
			}
			return execx.Output{}, nil
		}),
	}
	if _, err := local.Run(context.Background(), Command{Program: "systemctl"}); err != nil {
		t.Fatal(err)
	}
	for _, program := range []string{"", "../systemctl", "/usr/bin/systemctl", `..\\systemctl`, "-systemctl", "bad name"} {
		if _, err := local.Run(context.Background(), Command{Program: program}); !errors.Is(err, ErrInvalidProgram) {
			t.Fatalf("Run(%q) error = %v, want %v", program, err, ErrInvalidProgram)
		}
	}
	if calls != 1 {
		t.Fatalf("runner called %d times, want 1", calls)
	}
}

func TestLocalRunRejectsInvalidOutputLimits(t *testing.T) {
	t.Parallel()

	calls := 0
	local := Local{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			calls++
			return execx.Output{}, nil
		}),
	}
	tests := []Command{
		{Program: "systemctl", StdoutLimit: -1},
		{Program: "systemctl", StderrLimit: -1},
		{Program: "systemctl", StdoutLimit: MaxCommandOutputLimit + 1},
		{Program: "systemctl", StderrLimit: MaxCommandOutputLimit + 1},
	}
	for _, command := range tests {
		if _, err := local.Run(context.Background(), command); !errors.Is(err, ErrInvalidLimit) {
			t.Fatalf("Run(%#v) error = %v, want %v", command, err, ErrInvalidLimit)
		}
	}
	if calls != 0 {
		t.Fatalf("runner called %d times, want 0", calls)
	}
}

func TestLocalRunHonorsCanceledContextBeforeDelegation(t *testing.T) {
	t.Parallel()

	calls := 0
	local := Local{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, _ execx.Spec) (execx.Output, error) {
			calls++
			return execx.Output{}, nil
		}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := local.Run(ctx, Command{Program: "systemctl"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("runner called %d times, want 0", calls)
	}
}

func TestLocalExistsRejectsUnsafePaths(t *testing.T) {
	t.Parallel()

	local := Local{Root: t.TempDir()}
	for _, relative := range []string{"", ".", "..", filepath.Join("..", "outside")} {
		if _, err := local.Exists(relative); !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("Exists(%q) error = %v, want %v", relative, err, ErrInvalidPath)
		}
	}
}

func TestLocalRejectsRootsThatDependOnCurrentDirectory(t *testing.T) {
	t.Parallel()

	absolute := t.TempDir()
	if err := os.WriteFile(filepath.Join(absolute, "regular"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	nonClean := absolute + string(filepath.Separator) + "."
	tests := []struct {
		name string
		root string
	}{
		{name: "empty", root: ""},
		{name: "dot", root: "."},
		{name: "relative", root: "internal"},
		{name: "non-clean absolute", root: nonClean},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := (Local{Root: test.root}).Exists("regular")
			if !errors.Is(err, ErrInvalidRoot) {
				t.Fatalf("Local{Root: %q}.Exists() error = %v, want %v", test.root, err, ErrInvalidRoot)
			}
		})
	}
}

func TestLinesReturnsEmptySliceForEmptyFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "empty"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	lines, err := (Local{Root: root}).Lines("empty", 1)
	if err != nil {
		t.Fatal(err)
	}
	if lines == nil || len(lines) != 0 {
		t.Fatalf("Lines(empty) = %#v, want non-nil empty slice", lines)
	}
}
