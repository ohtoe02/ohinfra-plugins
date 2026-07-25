package contractbundle

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyAcceptsLockedBundleAndRejectsTampering(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	bundle := filepath.Join(root, "protocol-v1")
	if err := os.MkdirAll(filepath.Join(bundle, "conformance"), 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"schema":"fixture"}` + "\n")
	target := filepath.Join(bundle, "conformance", "fixture.json")
	if err := os.WriteFile(target, content, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	sums := []byte(hex.EncodeToString(sum[:]) + "  conformance/fixture.json\n")
	if err := os.WriteFile(filepath.Join(bundle, "SHA256SUMS"), sums, 0o600); err != nil {
		t.Fatal(err)
	}
	bundleSum := sha256.Sum256(sums)
	lock := []byte(`{
		"schema_version":"1",
		"source_repository":"ohtoe02/ohtools-plugin-catalog",
		"source_commit":"0123456789abcdef0123456789abcdef01234567",
		"bundle_sha256":"` + hex.EncodeToString(bundleSum[:]) + `"
	}`)
	lockPath := filepath.Join(root, "protocol-v1.lock.json")
	if err := os.WriteFile(lockPath, lock, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(bundle, lockPath); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(target, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Verify(bundle, lockPath); err == nil {
		t.Fatal("tampered contract bundle accepted")
	}
}

func TestVerifyRejectsTraversalAndUnlistedFiles(t *testing.T) {
	t.Parallel()

	for _, sums := range []string{
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  ../outside.json\n",
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa  listed.json\n",
	} {
		root := t.TempDir()
		bundle := filepath.Join(root, "protocol-v1")
		if err := os.MkdirAll(bundle, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bundle, "SHA256SUMS"), []byte(sums), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bundle, "unlisted.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(sums))
		lock := []byte(`{
			"schema_version":"1",
			"source_repository":"ohtoe02/ohtools-plugin-catalog",
			"source_commit":"0123456789abcdef0123456789abcdef01234567",
			"bundle_sha256":"` + hex.EncodeToString(sum[:]) + `"
		}`)
		lockPath := filepath.Join(root, "protocol-v1.lock.json")
		if err := os.WriteFile(lockPath, lock, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := Verify(bundle, lockPath); err == nil {
			t.Fatalf("unsafe sums accepted: %q", sums)
		}
	}
}
