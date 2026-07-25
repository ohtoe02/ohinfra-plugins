package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/pluginregistry"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	testRegistryRoot = "../.."
	testRepository   = "ohtoe02/ohtools-plugins"
	testTag          = "system-base-v1.1.0"
	testCommit       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testDescription  = "Operating system information and health diagnostics for ohtools."
)

func TestPublishReleaseCreatesDraftWithAllAssetsBeforePublishing(t *testing.T) {
	assets := writeTestAssets(t)
	github := &fakeGitHub{
		remoteCommit: testCommit,
		assets:       map[string][]byte{},
	}

	if err := publishRelease(
		context.Background(),
		github,
		publishOptions{
			RegistryRoot: testRegistryRoot,
			Repository:   testRepository,
			Tag:          testTag,
			Commit:       testCommit,
			Assets:       assets,
		},
	); err != nil {
		t.Fatal(err)
	}
	if github.draft || !github.releaseExists {
		t.Fatalf("release exists=%t draft=%t", github.releaseExists, github.draft)
	}
	for _, asset := range assets {
		if github.publishedDownloads[filepath.Base(asset)] == 0 {
			t.Fatalf("published asset %q was not byte-verified", filepath.Base(asset))
		}
	}
	if got, want := github.createAssets, assetNames(assets); !equalStrings(got, want) {
		t.Fatalf("create assets=%v want=%v", got, want)
	}
	for _, required := range []string{
		"--verify-tag",
		"--target",
		testCommit,
		"--draft",
		"--generate-notes",
	} {
		if !containsArgument(github.createArguments, required) {
			t.Fatalf("create command lacks %q: %v", required, github.createArguments)
		}
	}
	if len(github.uploadAssets) != 0 {
		t.Fatalf("new release used a follow-up upload: %v", github.uploadAssets)
	}
}

func TestPublishReleaseAcceptsExactReleaseAfterPublishTransportAmbiguity(t *testing.T) {
	assets := writeTestAssets(t)
	github := &fakeGitHub{
		remoteCommit:      testCommit,
		assets:            map[string][]byte{},
		editResultErr:     errors.New("transport closed after publish"),
		editResultApplied: true,
	}

	if err := publishRelease(
		context.Background(),
		github,
		publishOptions{
			RegistryRoot: testRegistryRoot,
			Repository:   testRepository,
			Tag:          testTag,
			Commit:       testCommit,
			Assets:       assets,
		},
	); err != nil {
		t.Fatal(err)
	}
	if github.draft || !github.releaseExists {
		t.Fatalf("release exists=%t draft=%t", github.releaseExists, github.draft)
	}
	for _, asset := range assets {
		if github.publishedDownloads[filepath.Base(asset)] == 0 {
			t.Fatalf(
				"published asset %q was not reverified after ambiguous publish",
				filepath.Base(asset),
			)
		}
	}
}

func TestPublishReleaseRejectsAssetChangedDuringAmbiguousPublish(t *testing.T) {
	assets := writeTestAssets(t)
	github := &fakeGitHub{
		remoteCommit:      testCommit,
		assets:            map[string][]byte{},
		editResultErr:     errors.New("transport closed after publish"),
		editResultApplied: true,
		publishedMutations: map[string][]byte{
			"system-base_linux_amd64": []byte("different published bytes"),
		},
	}

	err := publishRelease(
		context.Background(),
		github,
		publishOptions{
			RegistryRoot: testRegistryRoot,
			Repository:   testRepository,
			Tag:          testTag,
			Commit:       testCommit,
			Assets:       assets,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "does not match local asset") {
		t.Fatalf("ambiguous mismatched publish error=%v", err)
	}
}

func TestPublishReleaseResumesMatchingDraftAndUploadsOnlyMissingAssets(t *testing.T) {
	assets := writeTestAssets(t)
	existing, err := os.ReadFile(assets[0])
	if err != nil {
		t.Fatal(err)
	}
	github := &fakeGitHub{
		remoteCommit:  testCommit,
		releaseExists: true,
		draft:         true,
		tag:           testTag,
		targetCommit:  testCommit,
		assets: map[string][]byte{
			filepath.Base(assets[0]): existing,
		},
	}

	if err := publishRelease(
		context.Background(),
		github,
		publishOptions{
			RegistryRoot: testRegistryRoot,
			Repository:   testRepository,
			Tag:          testTag,
			Commit:       testCommit,
			Assets:       assets,
		},
	); err != nil {
		t.Fatal(err)
	}
	if github.draft {
		t.Fatal("matching draft was not published")
	}
	wantMissing := assetNames(assets[1:])
	if !equalStrings(github.uploadAssets, wantMissing) {
		t.Fatalf("uploaded assets=%v want=%v", github.uploadAssets, wantMissing)
	}
	if github.downloads[filepath.Base(assets[0])] == 0 {
		t.Fatal("existing draft asset was not downloaded for byte comparison")
	}
}

func TestPublishReleaseAcceptsExactAlreadyPublishedRelease(t *testing.T) {
	assets := writeTestAssets(t)
	remoteAssets := map[string][]byte{}
	for _, asset := range assets {
		content, err := os.ReadFile(asset)
		if err != nil {
			t.Fatal(err)
		}
		remoteAssets[filepath.Base(asset)] = content
	}
	github := &fakeGitHub{
		remoteCommit:  testCommit,
		releaseExists: true,
		draft:         false,
		tag:           testTag,
		targetCommit:  testCommit,
		assets:        remoteAssets,
	}

	if err := publishRelease(
		context.Background(),
		github,
		publishOptions{
			RegistryRoot: testRegistryRoot,
			Repository:   testRepository,
			Tag:          testTag,
			Commit:       testCommit,
			Assets:       assets,
		},
	); err != nil {
		t.Fatal(err)
	}
	if github.editCalls != 0 || len(github.uploadAssets) != 0 {
		t.Fatal("already-published release was mutated")
	}
	for _, asset := range assets {
		if github.publishedDownloads[filepath.Base(asset)] == 0 {
			t.Fatalf("published asset %q was not byte-verified", filepath.Base(asset))
		}
	}
}

func TestPublishReleaseFailsClosedForIncompletePublishedRelease(t *testing.T) {
	assets := writeTestAssets(t)
	github := &fakeGitHub{
		remoteCommit:  testCommit,
		releaseExists: true,
		draft:         false,
		tag:           testTag,
		targetCommit:  testCommit,
		assets:        map[string][]byte{},
	}

	err := publishRelease(
		context.Background(),
		github,
		publishOptions{
			RegistryRoot: testRegistryRoot,
			Repository:   testRepository,
			Tag:          testTag,
			Commit:       testCommit,
			Assets:       assets,
		},
	)
	if err == nil {
		t.Fatal("incomplete published release was accepted")
	}
	if len(github.uploadAssets) != 0 || github.editCalls != 0 {
		t.Fatal("published release was mutated")
	}
}

func TestPublishReleaseFailsClosedForPublishedReleaseMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(map[string][]byte, *fakeGitHub)
	}{
		{
			name: "target commit",
			mutate: func(_ map[string][]byte, github *fakeGitHub) {
				github.targetCommit = strings.Repeat("b", 40)
			},
		},
		{
			name: "asset bytes",
			mutate: func(remote map[string][]byte, _ *fakeGitHub) {
				remote["system-base_linux_amd64"] = []byte("different bytes")
			},
		},
		{
			name: "unexpected asset",
			mutate: func(remote map[string][]byte, _ *fakeGitHub) {
				remote["unexpected.txt"] = []byte("unexpected")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			assets := writeTestAssets(t)
			remoteAssets := readTestAssets(t, assets)
			github := &fakeGitHub{
				remoteCommit:  testCommit,
				releaseExists: true,
				draft:         false,
				tag:           testTag,
				targetCommit:  testCommit,
				assets:        remoteAssets,
			}
			test.mutate(remoteAssets, github)

			err := publishRelease(
				context.Background(),
				github,
				publishOptions{
					RegistryRoot: testRegistryRoot,
					Repository:   testRepository,
					Tag:          testTag,
					Commit:       testCommit,
					Assets:       assets,
				},
			)
			if err == nil {
				t.Fatal("mismatched published release was accepted")
			}
			if github.editCalls != 0 || len(github.uploadAssets) != 0 {
				t.Fatal("mismatched published release was mutated")
			}
		})
	}
}

func TestPublishReleaseFailsClosedForMismatchedDraftAsset(t *testing.T) {
	assets := writeTestAssets(t)
	github := &fakeGitHub{
		remoteCommit:  testCommit,
		releaseExists: true,
		draft:         true,
		tag:           testTag,
		targetCommit:  testCommit,
		assets: map[string][]byte{
			filepath.Base(assets[0]): []byte("different bytes"),
		},
	}

	err := publishRelease(
		context.Background(),
		github,
		publishOptions{
			RegistryRoot: testRegistryRoot,
			Repository:   testRepository,
			Tag:          testTag,
			Commit:       testCommit,
			Assets:       assets,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "does not match local asset") {
		t.Fatalf("mismatched asset error=%v", err)
	}
	if len(github.uploadAssets) != 0 || github.editCalls != 0 {
		t.Fatal("mismatched draft was mutated")
	}
}

func TestPublishReleaseFailsClosedForMismatchedDraftTarget(t *testing.T) {
	assets := writeTestAssets(t)
	github := &fakeGitHub{
		remoteCommit:  testCommit,
		releaseExists: true,
		draft:         true,
		tag:           testTag,
		targetCommit:  strings.Repeat("b", 40),
		assets:        map[string][]byte{},
	}

	err := publishRelease(
		context.Background(),
		github,
		publishOptions{
			RegistryRoot: testRegistryRoot,
			Repository:   testRepository,
			Tag:          testTag,
			Commit:       testCommit,
			Assets:       assets,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "targets commit") {
		t.Fatalf("draft target error=%v", err)
	}
	if len(github.uploadAssets) != 0 || github.editCalls != 0 {
		t.Fatal("mismatched draft target was mutated")
	}
}

func TestPublishReleaseFailsClosedForUnexpectedDraftAsset(t *testing.T) {
	assets := writeTestAssets(t)
	github := &fakeGitHub{
		remoteCommit:  testCommit,
		releaseExists: true,
		draft:         true,
		tag:           testTag,
		targetCommit:  testCommit,
		assets: map[string][]byte{
			"unexpected.txt": []byte("unexpected"),
		},
	}

	err := publishRelease(
		context.Background(),
		github,
		publishOptions{
			RegistryRoot: testRegistryRoot,
			Repository:   testRepository,
			Tag:          testTag,
			Commit:       testCommit,
			Assets:       assets,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "unexpected asset") {
		t.Fatalf("unexpected asset error=%v", err)
	}
	if len(github.uploadAssets) != 0 || github.editCalls != 0 {
		t.Fatal("draft with unexpected asset was mutated")
	}
}

func TestPublishReleaseRejectsInconsistentStagedArtifactSetBeforeGitHubMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, []string)
	}{
		{
			name: "checksum sidecar",
			mutate: func(t *testing.T, assets []string) {
				t.Helper()
				writeAsset(t, assets[1], strings.Repeat("0", 64)+"  system-base_linux_amd64\n")
			},
		},
		{
			name: "sbom namespace",
			mutate: func(t *testing.T, assets []string) {
				t.Helper()
				var document map[string]any
				decodeTestJSON(t, assets[2], &document)
				document["documentNamespace"] = "https://example.invalid/wrong"
				writeJSONAsset(t, assets[2], document)
			},
		},
		{
			name: "manifest identity",
			mutate: func(t *testing.T, assets []string) {
				t.Helper()
				var manifest protocol.Manifest
				decodeTestJSON(t, assets[3], &manifest)
				manifest.Version = "9.9.9"
				writeJSONAsset(t, assets[3], manifest)
			},
		},
		{
			name: "metadata digest",
			mutate: func(t *testing.T, assets []string) {
				t.Helper()
				var metadata pluginregistry.ReleaseMetadata
				decodeTestJSON(t, assets[4], &metadata)
				metadata.Asset.SHA256 = strings.Repeat("f", 64)
				writeJSONAsset(t, assets[4], metadata)
			},
		},
		{
			name: "metadata manifest",
			mutate: func(t *testing.T, assets []string) {
				t.Helper()
				var metadata pluginregistry.ReleaseMetadata
				decodeTestJSON(t, assets[4], &metadata)
				metadata.Manifest.Commands[0].Short = "Different manifest"
				writeJSONAsset(t, assets[4], metadata)
			},
		},
		{
			name: "metadata homepage",
			mutate: func(t *testing.T, assets []string) {
				t.Helper()
				var metadata pluginregistry.ReleaseMetadata
				decodeTestJSON(t, assets[4], &metadata)
				metadata.Homepage = "https://example.invalid/wrong"
				writeJSONAsset(t, assets[4], metadata)
			},
		},
		{
			name: "metadata minimum host version",
			mutate: func(t *testing.T, assets []string) {
				t.Helper()
				var metadata pluginregistry.ReleaseMetadata
				decodeTestJSON(t, assets[4], &metadata)
				metadata.MinimumOhtoolsVersion = "9.9.9"
				writeJSONAsset(t, assets[4], metadata)
			},
		},
		{
			name: "raw invalid UTF-8 metadata",
			mutate: func(t *testing.T, assets []string) {
				t.Helper()
				encoded, err := os.ReadFile(assets[4])
				if err != nil {
					t.Fatal(err)
				}
				offset := bytes.Index(encoded, []byte(testDescription))
				if offset < 0 {
					t.Fatal("metadata fixture does not contain description")
				}
				encoded[offset] = 0xff
				if err := os.WriteFile(assets[4], encoded, 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			assets := writeTestAssets(t)
			test.mutate(t, assets)
			github := &fakeGitHub{
				remoteCommit: testCommit,
				assets:       map[string][]byte{},
			}

			err := publishRelease(
				context.Background(),
				github,
				publishOptions{
					RegistryRoot: testRegistryRoot,
					Repository:   testRepository,
					Tag:          testTag,
					Commit:       testCommit,
					Assets:       assets,
				},
			)
			if err == nil {
				t.Fatal("inconsistent artifact set was accepted")
			}
			if github.createCalls != 0 || github.editCalls != 0 || len(github.uploadAssets) != 0 {
				t.Fatal("GitHub release was mutated for an inconsistent artifact set")
			}
		})
	}
}

func TestStageReleaseAssetsRejectsParentTraversal(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "system-base_linux_amd64")
	writeAsset(t, binary, "binary")
	traversal := filepath.Join(root, "nested") +
		string(filepath.Separator) + ".." +
		string(filepath.Separator) + "system-base_linux_amd64"

	_, err := stageReleaseAssets([]string{traversal}, filepath.Join(t.TempDir(), "staged"))
	if err == nil || !strings.Contains(err.Error(), "traversal") {
		t.Fatalf("traversal error=%v", err)
	}
}

func TestStageReleaseAssetsRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "system-base_linux_amd64")
	writeAsset(t, target, "binary")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	_, err := stageReleaseAssets([]string{link}, filepath.Join(t.TempDir(), "staged"))
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "symlink") {
		t.Fatalf("symlink error=%v", err)
	}
}

func TestOpenRegularNonSymlinkRejectsDescriptorSubstitution(t *testing.T) {
	root := t.TempDir()
	expected := filepath.Join(root, "expected")
	replacement := filepath.Join(root, "replacement")
	writeAsset(t, expected, "expected")
	writeAsset(t, replacement, "replaced")

	file, _, err := openRegularNonSymlinkWithOpener(
		expected,
		func(string) (*os.File, error) {
			return os.Open(replacement)
		},
	)
	if file != nil {
		_ = file.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "changed while") {
		t.Fatalf("descriptor substitution error=%v", err)
	}
}

func TestPublishReleaseFailsClosedForRemoteTagCommitMismatch(t *testing.T) {
	assets := writeTestAssets(t)
	github := &fakeGitHub{
		remoteCommit: strings.Repeat("b", 40),
		assets:       map[string][]byte{},
	}

	err := publishRelease(
		context.Background(),
		github,
		publishOptions{
			RegistryRoot: testRegistryRoot,
			Repository:   testRepository,
			Tag:          testTag,
			Commit:       testCommit,
			Assets:       assets,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "remote tag commit") {
		t.Fatalf("tag commit error=%v", err)
	}
	if github.createCalls != 0 {
		t.Fatal("release creation attempted for mismatched tag commit")
	}
}

func writeTestAssets(t *testing.T) []string {
	t.Helper()
	root := t.TempDir()
	binary := []byte("immutable binary")
	digest := sha256.Sum256(binary)
	sha := hex.EncodeToString(digest[:])
	published := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	manifest := protocol.Manifest{
		ProtocolVersion: protocol.ProtocolVersion,
		Name:            "system-base",
		Version:         "1.1.0",
		Description:     testDescription,
		Commands: []protocol.Command{{
			Path: []string{"system", "info"}, Use: "info", Short: "Show system information",
			Category: protocol.CategoryDiagnostic, Arguments: []protocol.Argument{}, Flags: []protocol.Flag{},
		}},
	}
	metadata := pluginregistry.ReleaseMetadata{
		SchemaVersion:         "1",
		Name:                  "system-base",
		Description:           testDescription,
		Homepage:              "https://github.com/ohtoe02/ohtools-plugins",
		Version:               "1.1.0",
		MinimumOhtoolsVersion: "0.3.2",
		PublishedAt:           published,
		Asset: pluginregistry.ReleaseAsset{
			OS:        "linux",
			Arch:      "amd64",
			URL:       "https://github.com/ohtoe02/ohtools-plugins/releases/download/system-base-v1.1.0/system-base_linux_amd64",
			SHA256:    sha,
			SizeBytes: int64(len(binary)),
		},
		Manifest: manifest,
	}
	sbom := map[string]any{
		"spdxVersion": "SPDX-2.3",
		"documentNamespace": fmt.Sprintf(
			"https://github.com/%s/releases/tag/%s/sbom/%s",
			testRepository,
			testTag,
			sha,
		),
		"creationInfo": map[string]any{
			"created": published.Format(time.RFC3339),
		},
	}
	assets := []string{
		filepath.Join(root, "system-base_linux_amd64"),
		filepath.Join(root, "system-base_linux_amd64.sha256"),
		filepath.Join(root, "system-base_linux_amd64.spdx.json"),
		filepath.Join(root, "system-base_manifest-v1.json"),
		filepath.Join(root, "system-base_release-metadata-v1.json"),
	}
	writeAsset(t, assets[0], string(binary))
	writeAsset(t, assets[1], fmt.Sprintf("%s  system-base_linux_amd64\n", sha))
	writeJSONAsset(t, assets[2], sbom)
	writeJSONAsset(t, assets[3], manifest)
	writeJSONAsset(t, assets[4], metadata)
	return assets
}

func writeAsset(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeJSONAsset(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func decodeTestJSON(t *testing.T, path string, target any) {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		t.Fatal(err)
	}
}

func readTestAssets(t *testing.T, paths []string) map[string][]byte {
	t.Helper()
	assets := make(map[string][]byte, len(paths))
	for _, path := range paths {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		assets[filepath.Base(path)] = content
	}
	return assets
}

func assetNames(paths []string) []string {
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, filepath.Base(path))
	}
	return names
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

type fakeGitHub struct {
	remoteCommit       string
	releaseExists      bool
	draft              bool
	tag                string
	targetCommit       string
	assets             map[string][]byte
	createArguments    []string
	createAssets       []string
	uploadAssets       []string
	downloads          map[string]int
	publishedDownloads map[string]int
	createCalls        int
	editCalls          int
	editResultErr      error
	editResultApplied  bool
	publishedMutations map[string][]byte
}

func (github *fakeGitHub) Run(_ context.Context, arguments ...string) ([]byte, error) {
	if github.downloads == nil {
		github.downloads = map[string]int{}
	}
	if github.publishedDownloads == nil {
		github.publishedDownloads = map[string]int{}
	}
	switch {
	case len(arguments) >= 2 && arguments[0] == "api":
		return []byte(github.remoteCommit + "\n"), nil
	case commandMatches(arguments, "release", "create"):
		github.createCalls++
		github.createArguments = append([]string(nil), arguments...)
		if github.releaseExists {
			return nil, errors.New("release already exists")
		}
		github.releaseExists = true
		github.draft = true
		github.tag = arguments[2]
		github.targetCommit = argumentValue(arguments, "--target")
		for _, path := range argumentsAfterSeparator(arguments) {
			content, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			name := filepath.Base(path)
			github.assets[name] = content
			github.createAssets = append(github.createAssets, name)
		}
		return nil, nil
	case commandMatches(arguments, "release", "view"):
		if !github.releaseExists {
			return nil, errors.New("release not found")
		}
		if containsArgument(arguments, "tagName,isDraft,targetCommitish") {
			return []byte(
				github.tag + "\t" + boolText(github.draft) + "\t" + github.targetCommit + "\n",
			), nil
		}
		names := make([]string, 0, len(github.assets))
		for name := range github.assets {
			names = append(names, name)
		}
		sort.Strings(names)
		return []byte(strings.Join(names, "\n") + "\n"), nil
	case commandMatches(arguments, "release", "download"):
		name := argumentValue(arguments, "--pattern")
		directory := argumentValue(arguments, "--dir")
		content, exists := github.assets[name]
		if !exists {
			return nil, errors.New("asset not found")
		}
		if err := os.WriteFile(filepath.Join(directory, name), content, 0o600); err != nil {
			return nil, err
		}
		github.downloads[name]++
		if !github.draft {
			github.publishedDownloads[name]++
		}
		return nil, nil
	case commandMatches(arguments, "release", "upload"):
		if containsArgument(arguments, "--clobber") {
			return nil, errors.New("clobber is forbidden")
		}
		for _, path := range argumentsAfterSeparator(arguments) {
			name := filepath.Base(path)
			if _, exists := github.assets[name]; exists {
				return nil, errors.New("asset already exists")
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			github.assets[name] = content
			github.uploadAssets = append(github.uploadAssets, name)
		}
		return nil, nil
	case commandMatches(arguments, "release", "edit"):
		github.editCalls++
		if github.editResultErr == nil || github.editResultApplied {
			github.draft = false
			for name, content := range github.publishedMutations {
				github.assets[name] = content
			}
		}
		return nil, github.editResultErr
	default:
		return nil, errors.New("unexpected gh command: " + strings.Join(arguments, " "))
	}
}

func commandMatches(arguments []string, first, second string) bool {
	return len(arguments) >= 2 && arguments[0] == first && arguments[1] == second
}

func containsArgument(arguments []string, value string) bool {
	for _, argument := range arguments {
		if argument == value {
			return true
		}
	}
	return false
}

func argumentValue(arguments []string, name string) string {
	for index := range len(arguments) - 1 {
		if arguments[index] == name {
			return arguments[index+1]
		}
	}
	return ""
}

func argumentsAfterSeparator(arguments []string) []string {
	for index, argument := range arguments {
		if argument == "--" {
			return arguments[index+1:]
		}
	}
	return nil
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
