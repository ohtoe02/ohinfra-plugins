//go:build linux

package runbook

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func validateTrustedPath(root string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("runbook path ownership is unavailable")
	}
	return validateTrustedMetadata(
		filepath.Clean(root) == string(os.PathSeparator),
		stat.Uid,
		info.Mode(),
	)
}
