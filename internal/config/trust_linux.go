//go:build linux

package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func validateTrustedFile(path string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("plugin config must be root-owned")
	}
	if stat.Uid != 0 {
		clean := filepath.Clean(path)
		if strings.HasPrefix(clean, "/etc/ohtools/") ||
			stat.Uid != uint32(os.Geteuid()) {
			return errors.New("plugin config must be root-owned")
		}
	}
	return validateTrustedMetadata(0, info.Mode())
}

func validateTrustedMetadata(uid uint32, mode os.FileMode) error {
	if uid != 0 {
		return errors.New("plugin config must be root-owned")
	}
	if mode.Perm()&0o022 != 0 {
		return errors.New("plugin config must not be group/world-writable")
	}
	return nil
}
