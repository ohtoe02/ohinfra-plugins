package serversetup

import (
	"strings"
	"testing"

	"github.com/ohtoe02/ohtools-plugins/internal/platform"
)

func TestProfileForSelectsOnlyExactSupportedPlatforms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id        string
		versionID string
		wantID    string
	}{
		{id: "debian", versionID: "10", wantID: "debian-10"},
		{id: "debian", versionID: "11", wantID: "debian-11"},
		{id: "debian", versionID: "12", wantID: "debian-12"},
		{id: "debian", versionID: "13", wantID: "debian-13"},
		{id: "ubuntu", versionID: "20.04", wantID: "ubuntu-20.04"},
		{id: "ubuntu", versionID: "22.04", wantID: "ubuntu-22.04"},
		{id: "ubuntu", versionID: "24.04", wantID: "ubuntu-24.04"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.wantID, func(t *testing.T) {
			t.Parallel()

			profile, err := ProfileFor(platform.Info{
				ID: test.id, VersionID: test.versionID, Supported: true,
			})
			if err != nil {
				t.Fatalf("ProfileFor() error = %v", err)
			}
			if profile.ID != test.wantID {
				t.Fatalf("profile ID = %q, want %q", profile.ID, test.wantID)
			}
			assertCompleteProfile(t, profile)
		})
	}
}

func TestProfileForRejectsUnsupportedWithoutNearMatch(t *testing.T) {
	t.Parallel()

	for _, info := range []platform.Info{
		{ID: "debian", VersionID: "14"},
		{ID: "ubuntu", VersionID: "24.10"},
		{ID: "fedora", VersionID: "42"},
		{ID: "debian", VersionID: "12", Supported: false},
	} {
		if _, err := ProfileFor(info); err == nil ||
			!strings.Contains(err.Error(), info.ID+" "+info.VersionID) {
			t.Fatalf("ProfileFor(%#v) error = %v", info, err)
		}
	}
}

func TestProfileByIDReturnsIndependentCompiledData(t *testing.T) {
	t.Parallel()

	first, err := ProfileByID("debian-12")
	if err != nil {
		t.Fatal(err)
	}
	first.Packages[0] = "attacker-package"
	first.ManagedFiles[0].Content[0] = 'X'
	first.Sysctls[0].Value = "0"

	second, err := ProfileByID("debian-12")
	if err != nil {
		t.Fatal(err)
	}
	if second.Packages[0] == "attacker-package" ||
		second.ManagedFiles[0].Content[0] == 'X' ||
		second.Sysctls[0].Value == "0" {
		t.Fatalf("compiled profile aliases caller data: %#v", second)
	}
}

func assertCompleteProfile(t *testing.T, profile Profile) {
	t.Helper()
	if len(profile.Packages) == 0 || len(profile.Groups) == 0 ||
		len(profile.Users) == 0 || len(profile.Directories) == 0 ||
		len(profile.ManagedFiles) == 0 || len(profile.Sysctls) == 0 ||
		len(profile.Units) == 0 {
		t.Fatalf("profile is incomplete: %#v", profile)
	}
	for _, file := range profile.ManagedFiles {
		if file.Path == "" || len(file.Content) == 0 || file.SHA256 == "" {
			t.Fatalf("managed file is incomplete: %#v", file)
		}
	}
}
