//go:build linux

package serversetup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrustedKeySourceRejectsNonRootOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operator.pub")
	if err := os.WriteFile(path, []byte("ssh-ed25519 AAAA fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(path, 65534, 65534); err != nil {
		t.Skipf("cannot create non-root-owned fixture: %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTrustedKeySource(path, info); err == nil {
		t.Fatal("non-root-owned authorized key source was accepted")
	}
}
