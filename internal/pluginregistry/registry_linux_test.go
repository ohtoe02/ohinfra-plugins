//go:build linux

package pluginregistry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsSymlinkedReleaseCommandOnLinux(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "system-base")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "cmd", "system-base")); err != nil {
		t.Fatal(err)
	}
	writeRegistry(t, root, `{
		"schema_version":"1",
		"plugins":[{
			"name":"system-base",
			"command":"./cmd/system-base",
			"description":"System diagnostics",
			"homepage":"https://github.com/ohtoe02/ohtools-plugins",
			"minimum_ohtools_version":"0.3.2",
			"release_enabled":true
		}]
	}`)

	if _, err := Load(root); err == nil {
		t.Fatal("symlinked release command accepted")
	} else if !strings.Contains(strings.ToLower(err.Error()), "symlink") {
		t.Fatalf("symlink rejected for the wrong reason: %v", err)
	}
}
