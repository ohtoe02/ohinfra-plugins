//go:build !windows

package serversetup

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
