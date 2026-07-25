//go:build !linux

package serversetup

import "os"

func openTrustedKeySource(path string) (*os.File, error) {
	return os.Open(path)
}
