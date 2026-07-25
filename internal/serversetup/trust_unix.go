//go:build !windows

package serversetup

import (
	"fmt"
	"os"
	"syscall"
)

func validateTrustedKeySource(
	path string,
	info os.FileInfo,
	allowCurrentOwner ...bool,
) error {
	metadata, ok := info.Sys().(*syscall.Stat_t)
	allowCurrent := len(allowCurrentOwner) > 0 && allowCurrentOwner[0]
	if !ok || metadata.Uid != 0 &&
		(!allowCurrent || metadata.Uid != uint32(os.Geteuid())) {
		return fmt.Errorf("authorized key source %s must be owned by root", path)
	}
	return nil
}
