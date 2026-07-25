package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunMatrixAndResolveTag(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "cmd", "system-base"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := `{
		"schema_version":"1",
		"plugins":[{
			"name":"system-base",
			"command":"./cmd/system-base",
			"description":"System diagnostics",
			"homepage":"https://github.com/ohtoe02/ohtools-plugins",
			"minimum_ohtools_version":"0.3.2",
			"release_enabled":true
		}]
	}`
	if err := os.WriteFile(filepath.Join(root, "plugins.json"), []byte(registry), 0o600); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	if err := run(root, []string{"matrix"}, &output); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != `["system-base"]` {
		t.Fatalf("matrix output = %q", output.String())
	}
	output.Reset()
	if err := run(root, []string{"resolve-tag", "system-base-v1.1.0"}, &output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "system-base\t1.1.0\n" {
		t.Fatalf("resolve output = %q", output.String())
	}

	manifestPath := filepath.Join(root, "manifest.json")
	manifest := `{
		"protocol_version":1,
		"name":"system-base",
		"version":"dev",
		"description":"System diagnostics",
		"commands":[{
			"path":["system","info"],
			"use":"info",
			"short":"Show system information",
			"category":"diagnostic",
			"arguments":[],
			"flags":[],
			"requires_root":false,
			"requires_force":false,
			"supports_dry_run":false,
			"requires_confirmation":false
		}]
	}`
	if err := os.WriteFile(manifestPath, []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if err := run(root, []string{"check-manifest", "system-base", manifestPath}, &output); err != nil {
		t.Fatal(err)
	}

	badManifest := strings.Replace(manifest, "System diagnostics", "Different description", 1)
	if err := os.WriteFile(manifestPath, []byte(badManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(root, []string{"check-manifest", "system-base", manifestPath}, &output); err == nil {
		t.Fatal("registry/manifest description mismatch accepted")
	}
}
