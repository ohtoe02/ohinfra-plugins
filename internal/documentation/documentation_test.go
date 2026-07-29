package plugindocs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDocumentAcceptsStrictEnglishContract(t *testing.T) {
	document, err := ParseDocument("server-setup-base.md", []byte(validEnglishDocument()))
	if err != nil {
		t.Fatalf("ParseDocument() error = %v", err)
	}

	if document.PluginID != "server-setup-base" {
		t.Fatalf("PluginID = %q, want server-setup-base", document.PluginID)
	}
	if len(document.CommandPaths) != 3 {
		t.Fatalf("CommandPaths = %v, want three commands", document.CommandPaths)
	}
	if !document.LocalOnly {
		t.Fatal("LocalOnly = false, want true")
	}
}

func TestParseDocumentRejectsUnknownFrontmatterFields(t *testing.T) {
	input := strings.Replace(validEnglishDocument(), "schema_version: 1", "schema_version: 1\nunknown: value", 1)

	_, err := ParseDocument("server-setup-base.md", []byte(input))
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("ParseDocument() error = %v, want unknown field rejection", err)
	}
}

func TestParseDocumentRequiresExplicitLocalOnlyClassification(t *testing.T) {
	input := strings.Replace(validEnglishDocument(), "local_only: true\n", "", 1)

	_, err := ParseDocument("server-setup-base.md", []byte(input))
	if err == nil || !strings.Contains(err.Error(), "local_only") {
		t.Fatalf("ParseDocument() error = %v, want missing local_only rejection", err)
	}
}

func TestParseDocumentRejectsUnsafeMarkdown(t *testing.T) {
	inputs := map[string]string{
		"raw HTML": strings.Replace(validEnglishDocument(), "Inspect the local host.", "<script>alert(1)</script>", 1),
		"MDX":      strings.Replace(validEnglishDocument(), "Inspect the local host.", "import Thing from './thing.astro'", 1),
		"URL":      strings.Replace(validEnglishDocument(), "Inspect the local host.", "[bad](http://example.com)", 1),
	}

	for name, input := range inputs {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseDocument("server-setup-base.md", []byte(input)); err == nil {
				t.Fatal("ParseDocument() error = nil, want unsafe Markdown rejection")
			}
		})
	}
}

func TestValidateSetRequiresLocaleParityAndManifestCoverage(t *testing.T) {
	english, err := ParseDocument("server-setup-base.md", []byte(validEnglishDocument()))
	if err != nil {
		t.Fatal(err)
	}

	err = ValidateSet([]Document{english}, map[string][]string{
		"server-setup-base": {"setup check", "setup apply", "setup upgrade"},
	})
	if err == nil || !strings.Contains(err.Error(), "ru") {
		t.Fatalf("ValidateSet() error = %v, want missing Russian locale", err)
	}

	russianInput := strings.ReplaceAll(validEnglishDocument(), "locale: en", "locale: ru")
	for englishHeading, russianHeading := range russianHeadings {
		russianInput = strings.ReplaceAll(russianInput, "## "+englishHeading, "## "+russianHeading)
	}
	russian, err := ParseDocument("server-setup-base.md", []byte(russianInput))
	if err != nil {
		t.Fatal(err)
	}

	if err := ValidateSet([]Document{english, russian}, map[string][]string{
		"server-setup-base": {"setup check", "setup apply", "setup upgrade"},
	}); err != nil {
		t.Fatalf("ValidateSet() error = %v", err)
	}
}

func TestValidateSetEnforcesPublishedLocalOnlyClassification(t *testing.T) {
	input := strings.ReplaceAll(validEnglishDocument(), "server-setup-base", "system-base")
	input = strings.ReplaceAll(input, "setup check", "system info")
	input = strings.ReplaceAll(input, "  - setup apply\n", "")
	input = strings.ReplaceAll(input, "  - setup upgrade\n", "")
	english, err := ParseDocument("system-base.md", []byte(input))
	if err != nil {
		t.Fatal(err)
	}
	russianInput := strings.ReplaceAll(input, "locale: en", "locale: ru")
	for englishHeading, russianHeading := range russianHeadings {
		russianInput = strings.ReplaceAll(russianInput, "## "+englishHeading, "## "+russianHeading)
	}
	russian, err := ParseDocument("system-base.md", []byte(russianInput))
	if err != nil {
		t.Fatal(err)
	}

	err = ValidateSet([]Document{english, russian}, map[string][]string{
		"system-base": {"system info"},
	})
	if err == nil || !strings.Contains(err.Error(), "local_only") {
		t.Fatalf("ValidateSet() error = %v, want local_only classification rejection", err)
	}
}

func TestValidatePublishedVersionsRejectsMissingPluginReleaseTag(t *testing.T) {
	english, err := ParseDocument("server-setup-base.md", []byte(validEnglishDocument()))
	if err != nil {
		t.Fatal(err)
	}

	err = ValidatePublishedVersions([]Document{english}, []string{"server-setup-base-v0.9.0"})
	if err == nil || !strings.Contains(err.Error(), "server-setup-base-v1.0.0") {
		t.Fatalf("ValidatePublishedVersions() error = %v, want missing release tag", err)
	}
}

func TestLoadDirectoryAndBuildBundleAreDeterministic(t *testing.T) {
	root := t.TempDir()
	for _, locale := range []string{"en", "ru"} {
		directory := filepath.Join(root, locale)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		input := validEnglishDocument()
		if locale == "ru" {
			input = strings.ReplaceAll(input, "locale: en", "locale: ru")
			for englishHeading, russianHeading := range russianHeadings {
				input = strings.ReplaceAll(input, "## "+englishHeading, "## "+russianHeading)
			}
		}
		if err := os.WriteFile(filepath.Join(directory, "server-setup-base.md"), []byte(input), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	documents, err := LoadDirectory(root)
	if err != nil {
		t.Fatalf("LoadDirectory() error = %v", err)
	}
	first, err := BuildBundle(documents, strings.Repeat("a", 40))
	if err != nil {
		t.Fatalf("BuildBundle() error = %v", err)
	}
	second, err := BuildBundle(documents, strings.Repeat("a", 40))
	if err != nil {
		t.Fatalf("BuildBundle() second error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatal("BuildBundle() is not deterministic")
	}

	var decoded struct {
		SchemaVersion int `json:"schema_version"`
		Plugins       []struct {
			PluginID string `json:"plugin_id"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatalf("bundle JSON error = %v", err)
	}
	if decoded.SchemaVersion != 1 || len(decoded.Plugins) != 1 ||
		decoded.Plugins[0].PluginID != "server-setup-base" {
		t.Fatalf("unexpected bundle: %+v", decoded)
	}
}

func validEnglishDocument() string {
	return `---
schema_version: 1
plugin_id: server-setup-base
locale: en
documented_version: 1.0.0
title: Server setup
summary: Inspect and converge a local server profile.
command_paths:
  - setup check
  - setup apply
  - setup upgrade
local_only: true
---
# Server setup

## Purpose and supported scenarios

Inspect the local host.

## Quick start

Run the check command.

## Commands

The manifest exposes three commands.

## How it works

The plugin reads bounded local state.

## Data access

Only local files and executables are accessed.

## Results and exit behavior

The plugin emits Result schema v1.

## Configuration

Configuration is strict YAML.

## What can be changed

Operators can select a supported profile.

## Fixed behavior

Protocol semantics require a new release to change.

## Safety

Mutations require host confirmation.

## Troubleshooting

Configuration errors use exit code 10.

## Limitations and TODO

Remote execution is deferred.

## Compatibility and source

See [the source](https://github.com/ohtoe02/ohtools-plugins).
`
}
