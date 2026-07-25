//go:build !windows

package serversetup

import "os"

func syncDirectory(path string) error {
	directory, err := os.Open(path) // #nosec G304 -- caller passes a validated managed directory.
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
