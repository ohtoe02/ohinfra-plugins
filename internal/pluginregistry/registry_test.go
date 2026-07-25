package pluginregistry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestLoadValidatesCommandsAndProducesReleaseMatrix(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, name := range []string{"system-base", "storage-base"} {
		if err := os.MkdirAll(filepath.Join(root, "cmd", name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeRegistry(t, root, `{
		"schema_version":"1",
		"plugins":[
			{
				"name":"system-base",
				"command":"./cmd/system-base",
				"description":"System diagnostics",
				"homepage":"https://github.com/ohtoe02/ohtools-plugins",
				"minimum_ohtools_version":"0.3.2",
				"release_enabled":true
			},
			{
				"name":"storage-base",
				"command":"./cmd/storage-base",
				"description":"Storage diagnostics",
				"homepage":"https://github.com/ohtoe02/ohtools-plugins",
				"minimum_ohtools_version":"0.3.2",
				"release_enabled":true
			},
			{
				"name":"server-setup-base",
				"command":"./cmd/server-setup-base",
				"description":"Server setup",
				"homepage":"https://github.com/ohtoe02/ohtools-plugins",
				"minimum_ohtools_version":"0.3.3",
				"release_enabled":false,
				"readiness_job":"server-setup-readiness"
			}
		]
	}`)

	registry, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	got := registry.ReleaseMatrix()
	if len(got) != 2 || got[0] != "storage-base" || got[1] != "system-base" {
		t.Fatalf("matrix = %#v", got)
	}
	plugin, version, err := registry.ResolveTag("storage-base-v1.0.2")
	if err != nil || plugin.Name != "storage-base" || version != "1.0.2" {
		t.Fatalf("resolved plugin=%#v version=%q err=%v", plugin, version, err)
	}
	if _, _, err := registry.ResolveTag("server-setup-base-v1.0.0"); err == nil {
		t.Fatal("disabled plugin tag accepted")
	}
}

func TestReleaseEnabledServerSetupRetainsMandatoryReadinessJob(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "server-setup-base"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeRegistry(t, root, `{
		"schema_version":"1",
		"plugins":[{
			"name":"server-setup-base",
			"command":"./cmd/server-setup-base",
			"description":"Server setup",
			"homepage":"https://github.com/ohtoe02/ohtools-plugins",
			"minimum_ohtools_version":"0.3.3",
			"release_enabled":true,
			"readiness_job":"server-setup-readiness"
		}]
	}`)

	registry, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := registry.ReleaseMatrix(); len(got) != 1 || got[0] != "server-setup-base" {
		t.Fatalf("release matrix = %#v", got)
	}
	if _, version, err := registry.ResolveTag("server-setup-base-v1.0.0"); err != nil ||
		version != "1.0.0" {
		t.Fatalf("ResolveTag() version=%q err=%v", version, err)
	}
}

func TestLoadRejectsUnregisteredCommandAndUnknownJSON(t *testing.T) {
	t.Parallel()

	for _, content := range []string{
		`{"schema_version":"1","plugins":[]}`,
		`{"schema_version":"1","plugins":[],"unknown":true}`,
	} {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "cmd", "extra-base"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeRegistry(t, root, content)
		if _, err := Load(root); err == nil {
			t.Fatalf("invalid registry accepted: %s", content)
		}
	}
}

func TestBuildReleaseMetadataBindsBinaryAndManifest(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	binary := filepath.Join(root, "system-base_linux_amd64")
	if err := os.WriteFile(binary, []byte("immutable binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	entry := Entry{
		Name:                  "system-base",
		Description:           "System diagnostics",
		Homepage:              "https://github.com/ohtoe02/ohtools-plugins",
		MinimumOhtoolsVersion: "0.3.2",
		ReleaseEnabled:        true,
	}
	manifest := protocol.Manifest{
		ProtocolVersion: protocol.ProtocolVersion,
		Name:            "system-base",
		Version:         "1.1.0",
		Description:     "System diagnostics",
		Commands: []protocol.Command{{
			Path: []string{"system", "info"}, Use: "info", Short: "Show system information",
			Category: protocol.CategoryDiagnostic, Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
		}},
	}
	published := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	metadata, err := BuildReleaseMetadata(
		entry,
		"1.1.0",
		manifest,
		binary,
		"https://github.com/ohtoe02/ohtools-plugins/releases/download/system-base-v1.1.0/system-base_linux_amd64",
		published,
	)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.SchemaVersion != "1" || metadata.Asset.OS != "linux" ||
		metadata.Asset.Arch != "amd64" || metadata.Asset.SizeBytes != int64(len("immutable binary")) ||
		len(metadata.Asset.SHA256) != 64 || metadata.PublishedAt != published {
		t.Fatalf("metadata = %#v", metadata)
	}

	manifest.Version = "different"
	if _, err := BuildReleaseMetadata(entry, "1.1.0", manifest, binary, metadata.Asset.URL, published); err == nil {
		t.Fatal("manifest version mismatch accepted")
	}
	manifest.Version = "1.1.0"
	if _, err := BuildReleaseMetadata(
		entry,
		"1.1.0",
		manifest,
		binary,
		"https://github.com/ohtoe02/ohtools-plugins/releases/latest/download/system-base_linux_amd64",
		published,
	); err == nil {
		t.Fatal("mutable release URL accepted")
	}
}

func TestLoadRejectsSymlinkedReleaseCommand(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd"), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "system-base")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "cmd", "system-base")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
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

func TestReleaseWorkflowRefusesToOverwritePublishedAssets(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(workflow)
	for _, required := range []string{
		`gh release create "${GITHUB_REF_NAME}"`,
		"--verify-tag",
		"--draft",
		`gh release upload "${GITHUB_REF_NAME}"`,
		`gh release edit "${GITHUB_REF_NAME}" --draft=false`,
	} {
		if !strings.Contains(content, required) {
			t.Errorf("release workflow lacks fail-closed immutability guard %q", required)
		}
	}
	for _, forbidden := range []string{
		`gh release view "${GITHUB_REF_NAME}"`,
		"softprops/action-gh-release",
		"--clobber",
	} {
		if strings.Contains(content, forbidden) {
			t.Errorf("release workflow retains non-atomic or clobbering path %q", forbidden)
		}
	}
}

func TestCIAndReleaseCompareVendoredContractsWithPinnedCanonicalCommit(t *testing.T) {
	var lock struct {
		SourceCommit string `json:"source_commit"`
	}
	encodedLock, err := os.ReadFile(filepath.Join("..", "..", "contracts", "protocol-v1.lock.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encodedLock, &lock); err != nil {
		t.Fatal(err)
	}
	if lock.SourceCommit == "" {
		t.Fatal("contract lock has no source commit")
	}
	for _, workflowName := range []string{"ci.yml", "release.yml"} {
		workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", workflowName))
		if err != nil {
			t.Fatal(err)
		}
		content := string(workflow)
		for _, required := range []string{
			"repository: ohtoe02/ohtools-plugin-catalog",
			"ref: " + lock.SourceCommit,
			"path: .catalog-contract-source",
			"go run ./tools/contractcheck --canonical .catalog-contract-source/contracts/protocol-v1",
		} {
			if !strings.Contains(content, required) {
				t.Errorf("%s lacks pinned canonical contract comparison %q", workflowName, required)
			}
		}
	}
}

func writeRegistry(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "plugins.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
