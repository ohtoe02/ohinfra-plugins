package serversetup

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
)

type PathConfinement interface {
	Inspect(root string, absolute string) (string, os.FileInfo, error)
}

type PathConfinementFunc func(string, string) (string, os.FileInfo, error)

func (function PathConfinementFunc) Inspect(
	root string,
	absolute string,
) (string, os.FileInfo, error) {
	return function(root, absolute)
}

type localPathConfinement struct{}

func (localPathConfinement) Inspect(
	root string,
	absolute string,
) (string, os.FileInfo, error) {
	path, err := rootedPath(root, absolute)
	if err != nil {
		return "", nil, err
	}
	relative := filepath.FromSlash(strings.TrimPrefix(absolute, "/"))
	exists, err := (probe.Local{Root: root}).Exists(relative)
	if err != nil {
		return "", nil, err
	}
	if !exists {
		return "", nil, os.ErrNotExist
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, err
	}
	return path, info, nil
}
