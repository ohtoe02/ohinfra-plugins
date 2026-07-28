package runbook

import (
	"errors"
	"os"
)

func validateTrustedMetadata(
	requireRoot bool,
	uid uint32,
	mode os.FileMode,
) error {
	if requireRoot && uid != 0 {
		return errors.New("runbook path must be root-owned")
	}
	if mode.Perm()&0o022 != 0 {
		return errors.New("runbook path must not be group/world-writable")
	}
	return nil
}
