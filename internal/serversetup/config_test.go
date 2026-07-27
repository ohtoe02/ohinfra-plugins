package serversetup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohtoe02/ohtools-plugins/internal/platform"
)

func TestResolveProfileUsesDetectedPlatformOrExactConfiguredProfile(t *testing.T) {
	t.Parallel()

	detected := platform.Info{ID: "debian", VersionID: "12", Supported: true}
	for _, profileID := range []string{"auto", "debian-12"} {
		settings := DefaultConfig()
		settings.Profile = profileID
		profile, err := ResolveProfile(settings, detected)
		if err != nil {
			t.Fatalf("ResolveProfile(%q) error = %v", profileID, err)
		}
		if profile.ID != "debian-12" {
			t.Fatalf("profile ID = %q", profile.ID)
		}
	}
}

func TestResolveProfileRejectsUnknownAndPlatformMismatch(t *testing.T) {
	t.Parallel()

	detected := platform.Info{ID: "debian", VersionID: "12", Supported: true}
	for _, profileID := range []string{"debian-13", "ubuntu-24.04", "custom", ""} {
		settings := DefaultConfig()
		settings.Profile = profileID
		if _, err := ResolveProfile(settings, detected); err == nil {
			t.Fatalf("ResolveProfile(%q) error = nil", profileID)
		}
	}
}

func TestValidateConfigRejectsUnsafeInspectionLimits(t *testing.T) {
	t.Parallel()

	tests := []Config{
		{Profile: "auto", ManagedFileLimitBytes: 0, AccountFileLimitBytes: 64 << 10},
		{Profile: "auto", ManagedFileLimitBytes: 1 << 20, AccountFileLimitBytes: -1},
		{Profile: "auto", ManagedFileLimitBytes: 9 << 20, AccountFileLimitBytes: 64 << 10},
		{Profile: "auto", ManagedFileLimitBytes: 1 << 20, AccountFileLimitBytes: 2 << 20},
	}
	for _, settings := range tests {
		if err := ValidateConfig(settings); err == nil {
			t.Fatalf("ValidateConfig(%#v) error = nil", settings)
		}
	}
}

func TestLoadConfigUsesStrictYAMLWithoutExtensibleOperations(t *testing.T) {
	t.Parallel()

	for _, body := range []string{
		"packages: [attacker]\n",
		"commands: [sh]\n",
		"file_destinations: [/tmp/payload]\n",
		"url: https://example.invalid/payload\n",
		"inline_content: payload\n",
		"profile: debian-12\nprofile: debian-13\n",
	} {
		path := filepath.Join(t.TempDir(), "server-setup-base.yaml")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil ||
			!strings.Contains(err.Error(), "config") {
			t.Fatalf("LoadConfig() body %q error = %v", body, err)
		}
	}
}
