//go:build !linux

package runbook

import "os"

func validateTrustedPath(_ string, _ os.FileInfo) error {
	return nil
}
