//go:build !windows

package serversetup

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

func validateManagedPath(
	path string,
	info os.FileInfo,
	directory bool,
	allowCurrentOwner bool,
) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("managed path %s has unavailable ownership metadata", path)
	}
	if err := validateManagedMetadata(
		stat.Uid,
		info.Mode(),
		directory,
		allowCurrentOwner,
	); err != nil {
		return fmt.Errorf("managed path %s is unsafe: %w", path, err)
	}
	return nil
}

func validateManagedMetadata(
	uid uint32,
	mode os.FileMode,
	directory bool,
	allowCurrentOwner bool,
) error {
	ownerAllowed := uid == 0
	if allowCurrentOwner {
		ownerAllowed = ownerAllowed || uid == uint32(os.Geteuid())
	}
	if !ownerAllowed {
		return errors.New("owner is not trusted")
	}
	if mode.Perm()&0o022 != 0 {
		if !(directory && mode&os.ModeSticky != 0 && uid == 0 && allowCurrentOwner) {
			return errors.New("path is group/world-writable")
		}
	}
	return nil
}

func validateAdministratorPath(
	path string,
	info os.FileInfo,
	expectedUID uint32,
	_ bool,
	allowCurrentOwner bool,
) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("administrator path %s has unavailable ownership metadata", path)
	}
	if !administratorOwnerAllowed(
		stat.Uid,
		expectedUID,
		uint32(os.Geteuid()),
		allowCurrentOwner,
	) {
		return fmt.Errorf("administrator path %s has an unexpected owner", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("administrator path %s is group/world-writable", path)
	}
	return nil
}

func administratorOwnerAllowed(
	actualUID uint32,
	expectedUID uint32,
	currentUID uint32,
	allowCurrentOwner bool,
) bool {
	if allowCurrentOwner {
		return actualUID == currentUID
	}
	return actualUID == expectedUID
}

func setAdministratorOwner(
	path string,
	uid uint32,
	gid uint32,
	allowCurrentOwner bool,
) error {
	if allowCurrentOwner {
		return nil
	}
	return os.Chown(path, int(uid), int(gid))
}

func setOpenFileAdministratorOwner(
	file *os.File,
	uid uint32,
	gid uint32,
	allowCurrentOwner bool,
) error {
	if allowCurrentOwner {
		return nil
	}
	return file.Chown(int(uid), int(gid))
}
