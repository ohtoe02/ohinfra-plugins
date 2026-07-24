//go:build !linux

package config

import "os"

func validateTrustedFile(_ string, _ os.FileInfo) error {
	return nil
}
