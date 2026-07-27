package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectSupportedPlatforms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		id        string
		versionID string
	}{
		{name: "debian-10", id: "debian", versionID: "10"},
		{name: "debian-11", id: "debian", versionID: "11"},
		{name: "debian-12", id: "debian", versionID: "12"},
		{name: "debian-13", id: "debian", versionID: "13"},
		{name: "ubuntu-20.04", id: "ubuntu", versionID: "20.04"},
		{name: "ubuntu-22.04", id: "ubuntu", versionID: "22.04"},
		{name: "ubuntu-24.04", id: "ubuntu", versionID: "24.04"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			info, err := Detect(fixtureRoot(t, test.name))
			if err != nil {
				t.Fatalf("Detect() error = %v", err)
			}
			want := Info{ID: test.id, VersionID: test.versionID, Supported: true}
			if info != want {
				t.Fatalf("Detect() = %#v, want %#v", info, want)
			}
		})
	}
}

func TestDetectReportsUnsupportedDistribution(t *testing.T) {
	t.Parallel()

	root := writeRelease(t, "ID=fedora\nVERSION_ID=42\n")
	info, err := Detect(root)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	want := Info{ID: "fedora", VersionID: "42", Supported: false}
	if info != want {
		t.Fatalf("Detect() = %#v, want %#v", info, want)
	}
}

func TestDetectRejectsInvalidReleaseFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "missing id", content: "VERSION_ID=12\n"},
		{name: "missing version", content: "ID=debian\n"},
		{name: "duplicate key", content: "ID=debian\nID=ubuntu\nVERSION_ID=12\n"},
		{name: "missing equals", content: "ID=debian\nVERSION_ID\n"},
		{name: "invalid key", content: "ID=debian\nversion-id=12\n"},
		{name: "unterminated double quote", content: "ID=debian\nVERSION_ID=\"12\n"},
		{name: "unterminated single quote", content: "ID=debian\nVERSION_ID='12\n"},
		{name: "trailing quoted content", content: "ID=debian\nVERSION_ID=\"12\"oops\n"},
		{name: "unquoted whitespace", content: "ID=debian linux\nVERSION_ID=12\n"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := Detect(writeRelease(t, test.content)); err == nil {
				t.Fatal("Detect() error = nil, want validation error")
			}
		})
	}
}

func TestDetectRejectsMissingAndOversizedReleaseFiles(t *testing.T) {
	t.Parallel()

	t.Run("missing", func(t *testing.T) {
		t.Parallel()
		if _, err := Detect(t.TempDir()); err == nil {
			t.Fatal("Detect() error = nil, want missing file error")
		}
	})

	t.Run("oversized", func(t *testing.T) {
		t.Parallel()
		content := "ID=debian\nVERSION_ID=12\nCOMMENT=" + strings.Repeat("x", 64*1024)
		if _, err := Detect(writeRelease(t, content)); err == nil {
			t.Fatal("Detect() error = nil, want size error")
		}
	})
}

func TestDetectRejectsUnsafeFixtureRoots(t *testing.T) {
	t.Parallel()

	t.Run("relative root", func(t *testing.T) {
		t.Parallel()
		if _, err := Detect(filepath.Join("testdata", "debian-12")); err == nil {
			t.Fatal("Detect() error = nil, want absolute root error")
		}
	})

	t.Run("symlinked os-release", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		target := filepath.Join(t.TempDir(), "os-release")
		if err := os.WriteFile(target, []byte("ID=debian\nVERSION_ID=12\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "etc"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, "etc", "os-release")); err != nil {
			t.Skipf("symlink is unavailable: %v", err)
		}

		if _, err := Detect(root); err == nil {
			t.Fatal("Detect() error = nil, want symlink error")
		}
	})
}

func TestValidateReleaseTargetAllowsOnlyConfinedProductionSymlink(t *testing.T) {
	t.Parallel()

	fixtureRoot := filepath.Join(string(filepath.Separator), "fixture")
	fixtureTarget := filepath.Join(fixtureRoot, "usr", "lib", "os-release")
	if err := validateReleaseTarget(fixtureRoot, fixtureTarget, true, 0); err == nil {
		t.Fatal("validateReleaseTarget() error = nil for fixture symlink")
	}

	productionRoot := filepath.VolumeName(t.TempDir()) + string(filepath.Separator)
	productionTarget := filepath.Join(productionRoot, "usr", "lib", "os-release")
	if err := validateReleaseTarget(productionRoot, productionTarget, true, 0); err != nil {
		t.Fatalf("validateReleaseTarget() rejected confined production symlink: %v", err)
	}

	fixtureOutsideTarget := filepath.Join(string(filepath.Separator), "outside", "os-release")
	if err := validateReleaseTarget(fixtureRoot, fixtureOutsideTarget, false, 0); err == nil {
		t.Fatal("validateReleaseTarget() error = nil for target outside fixture root")
	}
	if filepath.VolumeName(productionRoot) != "" {
		if err := validateReleaseTarget(productionRoot, `Z:\outside\os-release`, true, 0); err == nil {
			t.Fatal("validateReleaseTarget() error = nil for target outside production volume")
		}
	}

	if err := validateReleaseTarget(productionRoot, productionTarget, true, os.ModeDir); err == nil {
		t.Fatal("validateReleaseTarget() error = nil for non-regular target")
	}
}

func fixtureRoot(t *testing.T, name string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func writeRelease(t *testing.T, content string) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "etc", "os-release")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}
