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

func TestVerifyAgainstCanonicalRequiresByteForByteMatch(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	vendored := filepath.Join(root, "vendored")
	canonical := filepath.Join(root, "canonical")
	for _, directory := range []string{vendored, canonical} {
		if err := os.MkdirAll(filepath.Join(directory, "conformance"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	content := []byte("{\"contract\":\"canonical\"}\n")
	sum := sha256.Sum256(content)
	sums := []byte(hex.EncodeToString(sum[:]) + "  conformance/fixture.json\n")
	for _, directory := range []string{vendored, canonical} {
		if err := os.WriteFile(filepath.Join(directory, "conformance", "fixture.json"), content, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "SHA256SUMS"), sums, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bundleSum := sha256.Sum256(sums)
	lockPath := filepath.Join(root, "protocol-v1.lock.json")
	lock := []byte(`{
		"schema_version":"1",
		"source_repository":"ohtoe02/ohtools-plugin-catalog",
		"source_commit":"0123456789abcdef0123456789abcdef01234567",
		"bundle_sha256":"` + hex.EncodeToString(bundleSum[:]) + `"
	}`)
	if err := os.WriteFile(lockPath, lock, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := VerifyAgainstCanonical(vendored, lockPath, canonical); err != nil {
		t.Fatalf("identical canonical bundle rejected: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(canonical, "conformance", "fixture.json"),
		[]byte("{\"contract\":\"different\"}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAgainstCanonical(vendored, lockPath, canonical); err == nil {
		t.Fatal("vendored bundle differing from canonical source was accepted")
	}
}
