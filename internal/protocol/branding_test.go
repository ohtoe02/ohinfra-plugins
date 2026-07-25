package protocol

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryContainsNoLegacyBrand(t *testing.T) {
	assertNoLegacyBrand(t, filepath.Clean("../.."))
}

func assertNoLegacyBrand(t *testing.T, root string) {
	t.Helper()
	legacy := []byte("oh" + "infra")
	ignored := map[string]struct{}{
		".git": {}, ".cache": {}, "bin": {}, "dist": {}, "verification": {},
	}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != root {
			if _, skip := ignored[entry.Name()]; skip {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if bytes.Contains(bytes.ToLower([]byte(relative)), legacy) {
			t.Errorf("legacy brand remains in path %s", relative)
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path) // #nosec G304 -- paths are discovered inside the test checkout.
		if err != nil {
			return err
		}
		if bytes.Contains(bytes.ToLower(content), legacy) {
			t.Errorf("legacy brand remains in %s", strings.TrimPrefix(relative, "."+string(filepath.Separator)))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
