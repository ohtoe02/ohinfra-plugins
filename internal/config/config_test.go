package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fixture struct {
	Warning int `yaml:"warning"`
}

func TestLoadReturnsDefaultsWhenFileDoesNotExist(t *testing.T) {
	t.Parallel()

	defaults := fixture{Warning: 80}
	got, err := Load(filepath.Join(t.TempDir(), "missing.yaml"), defaults)
	if err != nil {
		t.Fatal(err)
	}
	if got != defaults {
		t.Fatalf("config = %#v, want %#v", got, defaults)
	}
}

func TestLoadUsesStrictSingleDocumentYAML(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"unknown: true\n",
		"warning: 80\nwarning: 90\n",
		"warning: 80\n---\nwarning: 90\n",
	} {
		path := filepath.Join(t.TempDir(), "plugin.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path, fixture{}); err == nil {
			t.Fatalf("Load accepted %q", body)
		}
	}
}

func TestLoadRejectsConfigLargerThanOneMiBBeforeReading(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "oversized.yaml")
	body := "warning: 80\n" + strings.Repeat("# padding\n", 1<<17)
	if len(body) <= 1<<20 {
		t.Fatalf("test fixture is only %d bytes", len(body))
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(path, fixture{})
	if err == nil || !strings.Contains(err.Error(), "exceeds 1048576-byte limit") {
		t.Fatalf("oversized config error = %v", err)
	}
}

func TestLoadRejectsSymlinkAndWritableFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	target := filepath.Join(dir, "target.yaml")
	if err := os.WriteFile(target, []byte("warning: 80\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.yaml")
	if err := os.Symlink(target, link); err == nil {
		if _, loadErr := Load(link, fixture{}); loadErr == nil ||
			!strings.Contains(loadErr.Error(), "symlink") {
			t.Fatalf("symlink error = %v", loadErr)
		}
	}

	if err := os.Chmod(target, 0o622); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if _, err := Load(target, fixture{}); err == nil ||
		(!strings.Contains(err.Error(), "writable") && !strings.Contains(err.Error(), "root-owned")) {
		t.Fatalf("writable file error = %v", err)
	}
}
