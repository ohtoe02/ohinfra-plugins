//go:build windows

package serversetup

import "os"

func validateTrustedKeySource(string, os.FileInfo) error {
	return nil
}
