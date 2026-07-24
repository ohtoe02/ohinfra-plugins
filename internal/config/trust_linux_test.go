//go:build linux

package config

import (
	"os"
	"strings"
	"testing"
)

func TestTrustedMetadataRequiresRootAndSafeMode(t *testing.T) {
	t.Parallel()

	if err := validateTrustedMetadata(0, 0o600); err != nil {
		t.Fatalf("safe metadata rejected: %v", err)
	}
	if err := validateTrustedMetadata(1000, 0o600); err == nil ||
		!strings.Contains(err.Error(), "root-owned") {
		t.Fatalf("owner error = %v", err)
	}
	for _, mode := range []os.FileMode{0o620, 0o602, 0o666} {
		if err := validateTrustedMetadata(0, mode); err == nil ||
			!strings.Contains(err.Error(), "writable") {
			t.Fatalf("mode %o error = %v", mode, err)
		}
	}
}
