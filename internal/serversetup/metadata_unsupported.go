//go:build !linux

package serversetup

import "os"

func fileIdentity(os.FileInfo) (FileIdentity, error) {
	return FileIdentity{}, ErrFileIdentityUnavailable
}
