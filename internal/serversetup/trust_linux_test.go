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

func TestManagedPathsRequireRootOwnershipInProduction(t *testing.T) {
	if err := validateManagedMetadata(65534, 0o644, false, false); err == nil {
		t.Fatal("non-root-owned managed file was accepted")
	}
	if err := validateManagedMetadata(0, 0o664, false, false); err == nil {
		t.Fatal("group-writable managed file was accepted")
	}
	if err := validateManagedMetadata(0, os.ModeDir|0o755, true, false); err != nil {
		t.Fatalf("root-owned protected directory was rejected: %v", err)
	}
	if !administratorOwnerAllowed(1000, 1000, 2000, false) {
		t.Fatal("administrator-owned path was rejected in production")
	}
	if administratorOwnerAllowed(0, 1000, 2000, false) {
		t.Fatal("root-owned administrator path was accepted in production")
	}
}
