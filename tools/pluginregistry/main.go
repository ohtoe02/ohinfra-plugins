package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/pluginregistry"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/strictjson"
)

func main() {
	root, err := os.Getwd()
	if err == nil {
		err = run(root, os.Args[1:], os.Stdout)
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root string, args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: pluginregistry <validate|matrix|resolve-tag|check-manifest|release-metadata>")
	}
	registry, err := pluginregistry.Load(root)
	if err != nil {
		return err
	}
	switch args[0] {
	case "validate":
		if len(args) != 1 {
			return errors.New("validate accepts no arguments")
		}
		return nil
	case "matrix":
		if len(args) != 1 {
			return errors.New("matrix accepts no arguments")
		}
		return json.NewEncoder(output).Encode(registry.ReleaseMatrix())
	case "resolve-tag":
		if len(args) != 2 {
			return errors.New("resolve-tag requires exactly one tag")
		}
		plugin, version, err := registry.ResolveTag(args[1])
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "%s\t%s\n", plugin.Name, version)
		return err
	case "check-manifest":
		if len(args) != 3 {
			return errors.New("check-manifest requires a plugin name and manifest path")
		}
		manifestJSON, err := os.ReadFile(args[2])
		if err != nil {
			return fmt.Errorf("read plugin manifest: %w", err)
		}
		var manifest protocol.Manifest
		if err := strictjson.Decode(manifestJSON, &manifest); err != nil {
			return fmt.Errorf("decode plugin manifest: %w", err)
		}
		return registry.CheckManifest(args[1], manifest)
	case "release-metadata":
		return writeReleaseMetadata(registry, args[1:], output)
	default:
		return fmt.Errorf("unknown pluginregistry command %q", args[0])
	}
}

func writeReleaseMetadata(registry pluginregistry.Registry, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("release-metadata", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	tag := flags.String("tag", "", "release tag")
	binaryPath := flags.String("binary", "", "release binary path")
	manifestPath := flags.String("manifest", "", "release manifest path")
	assetURL := flags.String("asset-url", "", "immutable release asset URL")
	publishedText := flags.String("published-at", "", "RFC3339 publication time")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *tag == "" || *binaryPath == "" || *manifestPath == "" ||
		*assetURL == "" || *publishedText == "" {
		return errors.New("release-metadata requires --tag, --binary, --manifest, --asset-url, and --published-at")
	}
	entry, version, err := registry.ResolveTag(*tag)
	if err != nil {
		return err
	}
	manifestJSON, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fmt.Errorf("read release manifest: %w", err)
	}
	var manifest protocol.Manifest
	if err := strictjson.Decode(manifestJSON, &manifest); err != nil {
		return fmt.Errorf("decode release manifest: %w", err)
	}
	publishedAt, err := time.Parse(time.RFC3339, *publishedText)
	if err != nil {
		return fmt.Errorf("parse published_at: %w", err)
	}
	metadata, err := pluginregistry.BuildReleaseMetadata(
		entry, version, manifest, *binaryPath, *assetURL, publishedAt,
	)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(metadata)
}
