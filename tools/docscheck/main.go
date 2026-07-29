package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	plugindocs "github.com/ohtoe02/ohtools-plugins/internal/documentation"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func main() {
	root := flag.String("root", ".", "repository root")
	selected := flag.String("plugins", "", "comma-separated plugin IDs; defaults to every cmd/*-base")
	verifyReleaseTags := flag.Bool("verify-release-tags", false, "require every documented version to have an immutable plugin release tag")
	flag.Parse()

	pluginIDs, err := discoverPlugins(*root, *selected)
	if err != nil {
		exit(err)
	}
	documents, err := plugindocs.LoadDirectory(filepath.Join(*root, "docs", "plugins"))
	if err != nil {
		exit(err)
	}

	wanted := make(map[string]bool, len(pluginIDs))
	for _, pluginID := range pluginIDs {
		wanted[pluginID] = true
	}
	filtered := documents[:0]
	for _, document := range documents {
		if wanted[document.PluginID] {
			filtered = append(filtered, document)
		}
	}

	manifests := make(map[string][]string, len(pluginIDs))
	for _, pluginID := range pluginIDs {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		command := exec.CommandContext(ctx, "go", "run", "./cmd/"+pluginID, "manifest", "--protocol=1")
		command.Dir = *root
		command.Stderr = os.Stderr
		output, runErr := command.Output()
		cancel()
		if runErr != nil {
			exit(fmt.Errorf("read %s manifest: %w", pluginID, runErr))
		}
		var manifest protocol.Manifest
		decoder := json.NewDecoder(strings.NewReader(string(output)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&manifest); err != nil {
			exit(fmt.Errorf("decode %s manifest: %w", pluginID, err))
		}
		if err := protocol.ValidateManifest(manifest); err != nil {
			exit(fmt.Errorf("validate %s manifest: %w", pluginID, err))
		}
		for _, command := range manifest.Commands {
			manifests[pluginID] = append(manifests[pluginID], strings.Join(command.Path, " "))
		}
	}

	if err := plugindocs.ValidateSet(filtered, manifests); err != nil {
		exit(err)
	}
	if *verifyReleaseTags {
		command := exec.Command("git", "tag", "--list")
		command.Dir = *root
		output, err := command.Output()
		if err != nil {
			exit(fmt.Errorf("list plugin release tags: %w", err))
		}
		if err := plugindocs.ValidatePublishedVersions(filtered, strings.Fields(string(output))); err != nil {
			exit(err)
		}
	}
	fmt.Printf("validated %d localized documents for %d plugins\n", len(filtered), len(pluginIDs))
}

func discoverPlugins(root, selected string) ([]string, error) {
	if selected != "" {
		values := strings.Split(selected, ",")
		seen := make(map[string]bool, len(values))
		for index := range values {
			values[index] = strings.TrimSpace(values[index])
			if values[index] == "" || seen[values[index]] {
				return nil, fmt.Errorf("invalid duplicate or empty plugin selection")
			}
			seen[values[index]] = true
			if info, err := os.Stat(filepath.Join(root, "cmd", values[index])); err != nil || !info.IsDir() {
				return nil, fmt.Errorf("selected plugin %q does not exist", values[index])
			}
		}
		sort.Strings(values)
		return values, nil
	}

	entries, err := os.ReadDir(filepath.Join(root, "cmd"))
	if err != nil {
		return nil, fmt.Errorf("read plugin commands: %w", err)
	}
	var values []string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), "-base") {
			values = append(values, entry.Name())
		}
	}
	sort.Strings(values)
	return values, nil
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
