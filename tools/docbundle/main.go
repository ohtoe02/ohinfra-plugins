package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	plugindocs "github.com/ohtoe02/ohtools-plugins/internal/documentation"
)

const maxBundleBytes = 5 << 20

func main() {
	root := flag.String("root", ".", "repository root")
	sourceCommit := flag.String("source-commit", "", "lowercase 40-character source commit")
	output := flag.String("output", "dist/plugin-docs-v1.json", "bundle output path")
	flag.Parse()

	documents, err := plugindocs.LoadDirectory(filepath.Join(*root, "docs", "plugins"))
	if err != nil {
		exit(err)
	}
	bundle, err := plugindocs.BuildBundle(documents, *sourceCommit)
	if err != nil {
		exit(err)
	}
	if len(bundle) > maxBundleBytes {
		exit(fmt.Errorf("documentation bundle exceeds %d bytes", maxBundleBytes))
	}

	path := *output
	if !filepath.IsAbs(path) {
		path = filepath.Join(*root, path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		exit(fmt.Errorf("create bundle directory: %w", err))
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".plugin-docs-v1-*.json")
	if err != nil {
		exit(fmt.Errorf("create temporary bundle: %w", err))
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		exit(fmt.Errorf("set temporary bundle mode: %w", err))
	}
	if _, err := temporary.Write(bundle); err != nil {
		temporary.Close()
		exit(fmt.Errorf("write temporary bundle: %w", err))
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		exit(fmt.Errorf("sync temporary bundle: %w", err))
	}
	if err := temporary.Close(); err != nil {
		exit(fmt.Errorf("close temporary bundle: %w", err))
	}
	if err := os.Rename(temporaryName, path); err != nil {
		exit(fmt.Errorf("publish bundle: %w", err))
	}
	fmt.Printf("wrote %s with %d localized documents\n", path, len(documents))
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
