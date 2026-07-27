package serversetup

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/ohtoe02/ohtools-plugins/internal/platform"
)

type GroupSpec struct {
	Name   string
	System bool
}

type UserSpec struct {
	Name         string
	PrimaryGroup string
	Home         string
	Shell        string
	System       bool
}

type DirectorySpec struct {
	Path  string
	Mode  uint32
	Owner string
	Group string
}

type ManagedFileSpec struct {
	Path    string
	Mode    uint32
	Owner   string
	Group   string
	Content []byte
	SHA256  string
}

type SysctlSpec struct {
	Key   string
	Value string
}

type Profile struct {
	ID           string
	PlatformID   string
	VersionID    string
	Packages     []string
	Groups       []GroupSpec
	Users        []UserSpec
	Directories  []DirectorySpec
	ManagedFiles []ManagedFileSpec
	Sysctls      []SysctlSpec
	Units        []string
}

var compiledProfiles = buildProfiles()

func ProfileFor(info platform.Info) (Profile, error) {
	if !info.Supported {
		return Profile{}, fmt.Errorf(
			"unsupported platform %s %s; use an exact Debian 10-13 or Ubuntu 20.04/22.04/24.04 profile",
			info.ID, info.VersionID,
		)
	}
	return ProfileByID(info.ID + "-" + info.VersionID)
}

func ProfileByID(id string) (Profile, error) {
	profile, ok := compiledProfiles[id]
	if !ok {
		return Profile{}, fmt.Errorf("unknown compiled setup profile %q", id)
	}
	return cloneProfile(profile), nil
}

func buildProfiles() map[string]Profile {
	platforms := []struct {
		id        string
		versionID string
	}{
		{id: "debian", versionID: "10"},
		{id: "debian", versionID: "11"},
		{id: "debian", versionID: "12"},
		{id: "debian", versionID: "13"},
		{id: "ubuntu", versionID: "20.04"},
		{id: "ubuntu", versionID: "22.04"},
		{id: "ubuntu", versionID: "24.04"},
	}
	profiles := make(map[string]Profile, len(platforms))
	for _, target := range platforms {
		id := target.id + "-" + target.versionID
		content := []byte("schema_version: 1\nprofile: " + id + "\n")
		digest := sha256.Sum256(content)
		profiles[id] = Profile{
			ID: id, PlatformID: target.id, VersionID: target.versionID,
			Packages: []string{"ca-certificates", "curl", "jq"},
			Groups:   []GroupSpec{{Name: "ohtools", System: true}},
			Users: []UserSpec{{
				Name: "ohtools", PrimaryGroup: "ohtools",
				Home: "/var/lib/ohtools", Shell: "/usr/sbin/nologin", System: true,
			}},
			Directories: []DirectorySpec{
				{Path: "/etc/ohtools", Mode: 0o755, Owner: "root", Group: "root"},
				{Path: "/etc/ohtools/plugins", Mode: 0o755, Owner: "root", Group: "root"},
				{Path: "/var/lib/ohtools", Mode: 0o750, Owner: "ohtools", Group: "ohtools"},
				{Path: "/var/log/ohtools", Mode: 0o750, Owner: "root", Group: "ohtools"},
			},
			ManagedFiles: []ManagedFileSpec{{
				Path: "/etc/ohtools/setup-state.yaml", Mode: 0o644,
				Owner: "root", Group: "root", Content: content,
				SHA256: hex.EncodeToString(digest[:]),
			}},
			Sysctls: []SysctlSpec{
				{Key: "fs.protected_hardlinks", Value: "1"},
				{Key: "fs.protected_symlinks", Value: "1"},
			},
			Units: []string{"systemd-timesyncd.service"},
		}
	}
	return profiles
}

func cloneProfile(input Profile) Profile {
	output := input
	output.Packages = append([]string(nil), input.Packages...)
	output.Groups = append([]GroupSpec(nil), input.Groups...)
	output.Users = append([]UserSpec(nil), input.Users...)
	output.Directories = append([]DirectorySpec(nil), input.Directories...)
	output.ManagedFiles = append([]ManagedFileSpec(nil), input.ManagedFiles...)
	for index := range output.ManagedFiles {
		output.ManagedFiles[index].Content = append([]byte(nil), input.ManagedFiles[index].Content...)
	}
	output.Sysctls = append([]SysctlSpec(nil), input.Sysctls...)
	output.Units = append([]string(nil), input.Units...)
	return output
}
