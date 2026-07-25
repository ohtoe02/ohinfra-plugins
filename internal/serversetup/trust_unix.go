//go:build !windows

package serversetup

import (
	"fmt"
	"os"
	"syscall"
)

func validateTrustedKeySource(path string, info os.FileInfo) error {
	metadata, ok := info.Sys().(*syscall.Stat_t)
	if !ok || metadata.Uid != 0 {
		return fmt.Errorf("authorized key source %s must be owned by root", path)
	}
	return nil
}
