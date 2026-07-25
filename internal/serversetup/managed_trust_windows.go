//go:build windows

package serversetup

import "os"

func validateManagedPath(
	string,
	os.FileInfo,
	bool,
	bool,
) error {
	return nil
}

func validateAdministratorPath(
	string,
	os.FileInfo,
	uint32,
	bool,
	bool,
) error {
	return nil
}

func setAdministratorOwner(string, uint32, uint32, bool) error {
	return nil
}

func setOpenFileAdministratorOwner(*os.File, uint32, uint32, bool) error {
	return nil
}
