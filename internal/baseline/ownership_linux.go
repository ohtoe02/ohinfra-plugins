//go:build linux

package baseline

import (
	"os"
	"path/filepath"
	"syscall"
)

func trustedAccountFile(root string, info os.FileInfo) bool {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return false
	}
	if filepath.Clean(root) != string(filepath.Separator) {
		return true
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0
}
