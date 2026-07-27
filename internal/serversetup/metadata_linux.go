//go:build linux

package serversetup

import (
	"fmt"
	"os"
	"syscall"
)

func fileIdentity(info os.FileInfo) (FileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return FileIdentity{}, fmt.Errorf("%w: unexpected stat metadata", ErrFileIdentityUnavailable)
	}
	return FileIdentity{UID: stat.Uid, GID: stat.Gid}, nil
}
