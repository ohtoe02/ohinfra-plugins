//go:build linux

package config

import (
	"errors"
	"os"
	"syscall"
)

func validateTrustedFile(_ string, info os.FileInfo) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("plugin config must be root-owned")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("plugin config must not be group/world-writable")
	}
	return nil
}
