package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	testRepository = "ohtoe02/ohtools-plugins"
	testTag        = "system-base-v1.1.0"
	testCommit     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
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
			Repository: testRepository,
			Tag:        testTag,
			Commit:     testCommit,
			Assets:     assets,
		},
	); err != nil {
		t.Fatal(err)
	}
	if github.draft || !github.releaseExists {
		t.Fatalf("release exists=%t draft=%t", github.releaseExists, github.draft)
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
			Repository: testRepository,
			Tag:        testTag,
			Commit:     testCommit,
			Assets:     assets,
		},
	); err != nil {
		t.Fatal(err)
	}
	if github.draft {
		t.Fatal("matching draft was not published")
	}
	wantMissing := []string{filepath.Base(assets[1])}
	if !equalStrings(github.uploadAssets, wantMissing) {
		t.Fatalf("uploaded assets=%v want=%v", github.uploadAssets, wantMissing)
	}
	if github.downloads[filepath.Base(assets[0])] == 0 {
		t.Fatal("existing draft asset was not downloaded for byte comparison")
	}
}

func TestPublishReleaseFailsClosedForPublishedRelease(t *testing.T) {
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
			Repository: testRepository,
			Tag:        testTag,
			Commit:     testCommit,
			Assets:     assets,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "published release already exists") {
		t.Fatalf("published release error=%v", err)
	}
	if len(github.uploadAssets) != 0 || github.editCalls != 0 {
		t.Fatal("published release was mutated")
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
			Repository: testRepository,
			Tag:        testTag,
			Commit:     testCommit,
			Assets:     assets,
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
			Repository: testRepository,
			Tag:        testTag,
			Commit:     testCommit,
			Assets:     assets,
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
			Repository: testRepository,
			Tag:        testTag,
			Commit:     testCommit,
			Assets:     assets,
		},
	)
	if err == nil || !strings.Contains(err.Error(), "unexpected asset") {
		t.Fatalf("unexpected asset error=%v", err)
	}
	if len(github.uploadAssets) != 0 || github.editCalls != 0 {
		t.Fatal("draft with unexpected asset was mutated")
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
			Repository: testRepository,
			Tag:        testTag,
			Commit:     testCommit,
			Assets:     assets,
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
	assets := []string{
		filepath.Join(root, "system-base_linux_amd64"),
		filepath.Join(root, "system-base_manifest-v1.json"),
	}
	for index, path := range assets {
		if err := os.WriteFile(path, []byte{byte(index + 1)}, 0o600); err != nil {
			t.Fatal(err)
		}
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
	remoteCommit    string
	releaseExists   bool
	draft           bool
	tag             string
	targetCommit    string
	assets          map[string][]byte
	createArguments []string
	createAssets    []string
	uploadAssets    []string
	downloads       map[string]int
	createCalls     int
	editCalls       int
}

func (github *fakeGitHub) Run(_ context.Context, arguments ...string) ([]byte, error) {
	if github.downloads == nil {
		github.downloads = map[string]int{}
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
		github.draft = false
		return nil, nil
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
