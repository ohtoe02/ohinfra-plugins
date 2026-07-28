package probe

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
)

const (
	DefaultCommandOutputLimit int64 = 1 << 20
	MaxCommandOutputLimit     int64 = 1 << 20
)

var (
	ErrInvalidPath    = errors.New("invalid probe path")
	ErrInvalidRoot    = errors.New("probe root must be an absolute clean path")
	ErrInvalidLimit   = errors.New("invalid probe limit")
	ErrInvalidProgram = errors.New("invalid probe program")
	ErrNotRegular     = errors.New("probe path is not a regular file")
	ErrSymlink        = errors.New("probe path contains a symbolic link")
	ErrTooLarge       = errors.New("probe input exceeds limit")

	programName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)
)

type Local struct {
	Root   string
	Runner execx.Runner
}

type Command struct {
	Program     string
	Arguments   []string
	StdoutLimit int64
	StderrLimit int64
}

func (local Local) Read(relative string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, ErrInvalidLimit
	}
	path, info, err := local.confinedPath(relative)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrNotRegular, relative)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%w: %s", ErrTooLarge, relative)
	}

	file, err := os.Open(path) // #nosec G304 -- confinedPath rejects traversal and symlinks.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("%w: %s", ErrNotRegular, relative)
	}

	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%w: %s", ErrTooLarge, relative)
	}
	return data, nil
}

func (local Local) Lines(relative string, limit int64) ([]string, error) {
	data, err := local.Read(relative, limit)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return []string{}, nil
	}
	text := strings.TrimSuffix(string(data), "\n")
	lines := strings.Split(text, "\n")
	for index := range lines {
		lines[index] = strings.TrimSuffix(lines[index], "\r")
	}
	return lines, nil
}

func (local Local) Run(ctx context.Context, command Command) (execx.Output, error) {
	if err := ctx.Err(); err != nil {
		return execx.Output{}, err
	}
	if !programName.MatchString(command.Program) || strings.HasPrefix(command.Program, "-") ||
		strings.ContainsAny(command.Program, `/\`) {
		return execx.Output{}, fmt.Errorf("%w: %q", ErrInvalidProgram, command.Program)
	}
	stdoutLimit, err := commandOutputLimit(command.StdoutLimit)
	if err != nil {
		return execx.Output{}, err
	}
	stderrLimit, err := commandOutputLimit(command.StderrLimit)
	if err != nil {
		return execx.Output{}, err
	}

	runner := local.Runner
	if runner == nil {
		runner = execx.OSRunner{Resolver: execx.SystemResolver()}
	}
	return runner.Run(ctx, execx.Spec{
		Program:     command.Program,
		Arguments:   append([]string(nil), command.Arguments...),
		StdoutLimit: stdoutLimit,
		StderrLimit: stderrLimit,
	})
}

func (local Local) Exists(relative string) (bool, error) {
	_, _, err := local.confinedPath(relative)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (local Local) confinedPath(relative string) (string, os.FileInfo, error) {
	clean, err := cleanRelativePath(relative)
	if err != nil {
		return "", nil, err
	}
	if !filepath.IsAbs(local.Root) || filepath.Clean(local.Root) != local.Root {
		return "", nil, ErrInvalidRoot
	}
	root := local.Root
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return "", nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("%w: root", ErrSymlink)
	}
	if !rootInfo.IsDir() {
		return "", nil, fmt.Errorf("%w: root is not a directory", ErrInvalidPath)
	}

	current := root
	components := strings.Split(clean, string(filepath.Separator))
	var info os.FileInfo
	for _, component := range components {
		current = filepath.Join(current, component)
		info, err = os.Lstat(current)
		if err != nil {
			return "", nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", nil, fmt.Errorf("%w: %s", ErrSymlink, relative)
		}
	}
	return current, info, nil
}

func cleanRelativePath(relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.VolumeName(relative) != "" {
		return "", ErrInvalidPath
	}
	for _, component := range strings.FieldsFunc(relative, func(character rune) bool {
		return character == '/' || character == '\\'
	}) {
		if component == ".." {
			return "", ErrInvalidPath
		}
	}
	clean := filepath.Clean(relative)
	if clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrInvalidPath
	}
	return clean, nil
}

func commandOutputLimit(limit int64) (int64, error) {
	switch {
	case limit == 0:
		return DefaultCommandOutputLimit, nil
	case limit < 0 || limit > MaxCommandOutputLimit:
		return 0, ErrInvalidLimit
	default:
		return limit, nil
	}
}
