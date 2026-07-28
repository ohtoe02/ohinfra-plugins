//go:build !linux

package baseline

import "os"

func trustedAccountFile(_ string, info os.FileInfo) bool {
	return info.Mode().IsRegular()
}
