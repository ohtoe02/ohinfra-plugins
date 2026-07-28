package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"go.yaml.in/yaml/v3"
)

const (
	manifestDirectory          = "etc/kubernetes/manifests"
	manifestFileLimit    int64 = 512 * 1024
	manifestTotalLimit   int64 = 4 * 1024 * 1024
	maxManifestEntries         = 256
	maxManifestDocuments       = 512
)

type ManifestMetadata struct {
	Path       string `json:"path"`
	Document   int    `json:"document"`
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace,omitempty"`
}

func executeManifests(ctx context.Context, options Options) (protocol.Result, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	manifests, found, err := collectManifests(ctx, options.Root)
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return protocol.Result{}, fatal
		}
		return protocol.Result{}, configurationFailure(
			"static Kubernetes manifests are not safe bounded YAML files",
		)
	}
	status := protocol.StatusPass
	summary := fmt.Sprintf("Collected metadata from %d static manifest documents", len(manifests))
	if !found {
		status = protocol.StatusInfo
		summary = "No local static Kubernetes manifest directory was found"
	}
	return buildResult(options, "k8s manifests", map[string]any{
		"manifests": manifests,
	}, []protocol.Check{{
		ID: "k8s.manifests", Status: status, Summary: summary,
	}}, nil), nil
}

func collectManifests(
	ctx context.Context,
	root string,
) ([]ManifestMetadata, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	directory, found, err := confinedDirectory(root, manifestDirectory)
	if err != nil || !found {
		return []ManifestMetadata{}, found, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, true, err
	}
	if len(entries) > maxManifestEntries {
		return nil, true, errors.New("static manifest directory exceeds entry limit")
	}
	local := probe.Local{Root: root}
	manifests := []ManifestMetadata{}
	var total int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, true, err
		}
		entryType := entry.Type()
		if entryType&os.ModeSymlink != 0 {
			return nil, true, errors.New("static manifest directory contains a symlink")
		}
		if entry.IsDir() || !isManifestName(entry.Name()) {
			continue
		}
		relative := filepath.ToSlash(filepath.Join(manifestDirectory, entry.Name()))
		encoded, err := local.Read(relative, manifestFileLimit)
		if err != nil {
			return nil, true, err
		}
		if err := ctx.Err(); err != nil {
			return nil, true, err
		}
		total += int64(len(encoded))
		if total > manifestTotalLimit {
			return nil, true, errors.New("static manifests exceed total size limit")
		}
		documents, err := parseManifestFile(ctx, relative, encoded)
		if err != nil {
			return nil, true, err
		}
		manifests = append(manifests, documents...)
		if len(manifests) > maxManifestDocuments {
			return nil, true, errors.New("static manifests exceed document limit")
		}
	}
	sort.Slice(manifests, func(left, right int) bool {
		if manifests[left].Path == manifests[right].Path {
			return manifests[left].Document < manifests[right].Document
		}
		return manifests[left].Path < manifests[right].Path
	})
	return manifests, true, nil
}

func confinedDirectory(root, relative string) (string, bool, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", false, errors.New("manifest root must be an absolute clean path")
	}
	current := root
	rootInfo, err := os.Lstat(current)
	if err != nil {
		return "", false, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", false, errors.New("manifest root must be a non-symlink directory")
	}
	for _, component := range strings.Split(filepath.FromSlash(relative), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", false, errors.New("manifest path must contain only non-symlink directories")
		}
	}
	return current, true, nil
}

func isManifestName(name string) bool {
	extension := strings.ToLower(filepath.Ext(name))
	return extension == ".yaml" || extension == ".yml"
}

func parseManifestFile(
	ctx context.Context,
	path string,
	encoded []byte,
) ([]ManifestMetadata, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	documents := []ManifestMetadata{}
	documentNumber := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var node yaml.Node
		err := decoder.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		documentNumber++
		if len(node.Content) == 0 {
			continue
		}
		if err := rejectAliases(&node); err != nil {
			return nil, err
		}
		var document struct {
			APIVersion string `yaml:"apiVersion"`
			Kind       string `yaml:"kind"`
			Metadata   struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		if err := node.Decode(&document); err != nil {
			return nil, err
		}
		if document.APIVersion == "" || document.Kind == "" || document.Metadata.Name == "" {
			return nil, errors.New("static manifest is missing required metadata")
		}
		for _, value := range []string{
			document.APIVersion, document.Kind,
			document.Metadata.Name, document.Metadata.Namespace,
		} {
			if err := validateMetadataValue(value); err != nil {
				return nil, err
			}
		}
		documents = append(documents, ManifestMetadata{
			Path: path, Document: documentNumber,
			APIVersion: document.APIVersion, Kind: document.Kind,
			Name: document.Metadata.Name, Namespace: document.Metadata.Namespace,
		})
	}
	return documents, nil
}
