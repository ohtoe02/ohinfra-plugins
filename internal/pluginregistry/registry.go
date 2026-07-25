package pluginregistry

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/strictjson"
)

var (
	pluginName = regexp.MustCompile(`^[a-z][a-z0-9-]*-base$`)
	semver     = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	jobName    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

type Registry struct {
	SchemaVersion string  `json:"schema_version"`
	Plugins       []Entry `json:"plugins"`
}

type Entry struct {
	Name                  string `json:"name"`
	Command               string `json:"command"`
	Description           string `json:"description"`
	Homepage              string `json:"homepage"`
	MinimumOhtoolsVersion string `json:"minimum_ohtools_version"`
	ReleaseEnabled        bool   `json:"release_enabled"`
	ReadinessJob          string `json:"readiness_job,omitempty"`
}

type ReleaseMetadata struct {
	SchemaVersion         string            `json:"schema_version"`
	Name                  string            `json:"name"`
	Description           string            `json:"description,omitempty"`
	Homepage              string            `json:"homepage,omitempty"`
	Version               string            `json:"version"`
	MinimumOhtoolsVersion string            `json:"minimum_ohtools_version"`
	PublishedAt           time.Time         `json:"published_at"`
	Asset                 ReleaseAsset      `json:"asset"`
	Manifest              protocol.Manifest `json:"manifest"`
}

type ReleaseAsset struct {
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
}

func Load(root string) (Registry, error) {
	encoded, err := os.ReadFile(filepath.Join(root, "plugins.json"))
	if err != nil {
		return Registry{}, fmt.Errorf("read plugin registry: %w", err)
	}
	var registry Registry
	if err := strictjson.Decode(encoded, &registry); err != nil {
		return Registry{}, fmt.Errorf("decode plugin registry: %w", err)
	}
	if err := registry.validate(root); err != nil {
		return Registry{}, err
	}
	return registry, nil
}

func (registry Registry) ReleaseMatrix() []string {
	names := make([]string, 0, len(registry.Plugins))
	for _, plugin := range registry.Plugins {
		if plugin.ReleaseEnabled {
			names = append(names, plugin.Name)
		}
	}
	slices.Sort(names)
	return names
}

func (registry Registry) ResolveTag(tag string) (Entry, string, error) {
	for _, plugin := range registry.Plugins {
		prefix := plugin.Name + "-v"
		if !strings.HasPrefix(tag, prefix) {
			continue
		}
		version := strings.TrimPrefix(tag, prefix)
		if !plugin.ReleaseEnabled {
			return Entry{}, "", fmt.Errorf("plugin %q is not release-enabled", plugin.Name)
		}
		if !semver.MatchString(version) {
			return Entry{}, "", fmt.Errorf("tag %q does not contain a stable SemVer version", tag)
		}
		return plugin, version, nil
	}
	return Entry{}, "", fmt.Errorf("tag %q does not identify a registered plugin", tag)
}

func (registry Registry) CheckManifest(name string, manifest protocol.Manifest) error {
	var entry Entry
	found := false
	for _, candidate := range registry.Plugins {
		if candidate.Name == name {
			entry = candidate
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("plugin %q is not registered", name)
	}
	if err := protocol.ValidateManifest(manifest); err != nil {
		return err
	}
	if manifest.Name != entry.Name || manifest.Description != entry.Description {
		return errors.New("plugin manifest identity or description does not match plugins.json")
	}
	return nil
}

func BuildReleaseMetadata(
	entry Entry,
	version string,
	manifest protocol.Manifest,
	binaryPath string,
	assetURL string,
	publishedAt time.Time,
) (ReleaseMetadata, error) {
	if !entry.ReleaseEnabled {
		return ReleaseMetadata{}, fmt.Errorf("plugin %q is not release-enabled", entry.Name)
	}
	if !semver.MatchString(version) {
		return ReleaseMetadata{}, fmt.Errorf("invalid release version %q", version)
	}
	if err := protocol.ValidateManifest(manifest); err != nil {
		return ReleaseMetadata{}, fmt.Errorf("validate release manifest: %w", err)
	}
	if manifest.Name != entry.Name || manifest.Version != version ||
		manifest.Description != entry.Description {
		return ReleaseMetadata{}, errors.New("release manifest identity, version, or description does not match registry")
	}
	parsedURL, err := url.Parse(assetURL)
	if err != nil || parsedURL.Scheme != "https" || parsedURL.Host == "" ||
		parsedURL.User != nil || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return ReleaseMetadata{}, errors.New("release asset URL must be HTTPS without credentials, query, or fragment")
	}
	expectedAssetURL := fmt.Sprintf(
		"https://github.com/ohtoe02/ohtools-plugins/releases/download/%s-v%s/%s_linux_amd64",
		entry.Name,
		version,
		entry.Name,
	)
	if assetURL != expectedAssetURL {
		return ReleaseMetadata{}, fmt.Errorf(
			"release asset URL must be the immutable first-party URL %q",
			expectedAssetURL,
		)
	}
	if publishedAt.IsZero() {
		return ReleaseMetadata{}, errors.New("published_at must not be zero")
	}

	info, err := os.Lstat(binaryPath)
	if err != nil {
		return ReleaseMetadata{}, fmt.Errorf("inspect release binary: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() <= 0 {
		return ReleaseMetadata{}, errors.New("release binary must be a non-empty regular non-symlink file")
	}
	file, err := os.Open(binaryPath)
	if err != nil {
		return ReleaseMetadata{}, fmt.Errorf("open release binary: %w", err)
	}
	defer file.Close()
	hasher := sha256.New()
	written, err := io.Copy(hasher, file)
	if err != nil {
		return ReleaseMetadata{}, fmt.Errorf("hash release binary: %w", err)
	}
	if written != info.Size() {
		return ReleaseMetadata{}, errors.New("release binary changed while it was being hashed")
	}

	return ReleaseMetadata{
		SchemaVersion:         "1",
		Name:                  entry.Name,
		Description:           entry.Description,
		Homepage:              entry.Homepage,
		Version:               version,
		MinimumOhtoolsVersion: entry.MinimumOhtoolsVersion,
		PublishedAt:           publishedAt.UTC(),
		Asset: ReleaseAsset{
			OS: "linux", Arch: "amd64", URL: assetURL,
			SHA256: hex.EncodeToString(hasher.Sum(nil)), SizeBytes: info.Size(),
		},
		Manifest: manifest,
	}, nil
}

func (registry Registry) validate(root string) error {
	if registry.SchemaVersion != "1" {
		return errors.New("plugin registry schema_version must be 1")
	}
	if len(registry.Plugins) == 0 || len(registry.Plugins) > 128 {
		return errors.New("plugin registry must contain 1..128 entries")
	}
	seen := make(map[string]struct{}, len(registry.Plugins))
	for _, plugin := range registry.Plugins {
		if err := validateEntry(root, plugin); err != nil {
			return err
		}
		if _, duplicate := seen[plugin.Name]; duplicate {
			return fmt.Errorf("duplicate plugin registry entry %q", plugin.Name)
		}
		seen[plugin.Name] = struct{}{}
	}

	commandRoot := filepath.Join(root, "cmd")
	entries, err := os.ReadDir(commandRoot)
	if err != nil {
		return fmt.Errorf("read plugin command directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !pluginName.MatchString(entry.Name()) {
			continue
		}
		if _, registered := seen[entry.Name()]; !registered {
			return fmt.Errorf("command directory %q is not registered in plugins.json", entry.Name())
		}
	}
	return nil
}

func validateEntry(root string, entry Entry) error {
	if !pluginName.MatchString(entry.Name) {
		return fmt.Errorf("invalid plugin registry name %q", entry.Name)
	}
	if entry.Command != "./cmd/"+entry.Name {
		return fmt.Errorf("plugin %q command must be ./cmd/%s", entry.Name, entry.Name)
	}
	if err := protocol.ValidateDescription(entry.Description); err != nil || entry.Description == "" {
		return fmt.Errorf("plugin %q has an invalid description", entry.Name)
	}
	homepage, err := url.Parse(entry.Homepage)
	if err != nil || homepage.Scheme != "https" || homepage.Host == "" ||
		homepage.User != nil || homepage.RawQuery != "" || homepage.Fragment != "" {
		return fmt.Errorf("plugin %q has an invalid homepage", entry.Name)
	}
	if !semver.MatchString(entry.MinimumOhtoolsVersion) {
		return fmt.Errorf("plugin %q has an invalid minimum_ohtools_version", entry.Name)
	}
	commandPath := filepath.Join(root, "cmd", entry.Name)
	info, statErr := os.Stat(commandPath)
	if entry.ReleaseEnabled {
		if statErr != nil || !info.IsDir() {
			return fmt.Errorf("release-enabled plugin %q does not have a command directory", entry.Name)
		}
		if entry.ReadinessJob != "" && !jobName.MatchString(entry.ReadinessJob) {
			return fmt.Errorf("release-enabled plugin %q has an invalid readiness_job", entry.Name)
		}
	} else if !jobName.MatchString(entry.ReadinessJob) {
		return fmt.Errorf("disabled plugin %q must declare a readiness_job", entry.Name)
	}
	if entry.Name == "server-setup-base" &&
		entry.ReadinessJob != "server-setup-readiness" {
		return errors.New("server-setup-base must retain the mandatory server-setup-readiness job")
	}
	return nil
}
