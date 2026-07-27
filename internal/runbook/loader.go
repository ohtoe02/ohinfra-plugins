package runbook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	runbookDirectory = "etc/ohtools/runbooks"
	maxRunbookFiles  = 128
	maxRunbookSize   = 64 << 10
	maxRunbookTotal  = 1 << 20
)

var runbookName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

type Document struct {
	SchemaVersion int    `yaml:"schema_version"`
	Name          string `yaml:"name"`
	Title         string `yaml:"title"`
	Description   string `yaml:"description"`
	Steps         []Step `yaml:"steps"`
}

type Step struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
}

type RunbookSummary struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	StepCount   int    `json:"step_count"`
}

func loadDocuments(ctx context.Context, root string) ([]Document, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory, directoryFile, exists, err := openRunbookDirectory(root)
	if err != nil {
		return nil, err
	}
	if !exists {
		return []Document{}, nil
	}
	defer directoryFile.Close()
	entries, err := directoryFile.ReadDir(maxRunbookFiles + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, errors.New("read runbook directory")
	}
	if len(entries) > maxRunbookFiles {
		return nil, errors.New("runbook directory entry count exceeds limit")
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].Name() < entries[right].Name()
	})

	documents := make([]Document, 0, len(entries))
	seenNames := make(map[string]struct{}, len(entries))
	total := int64(0)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if extension != ".yaml" && extension != ".yml" {
			continue
		}
		if len(documents) >= maxRunbookFiles {
			return nil, errors.New("runbook file count exceeds limit")
		}
		path := filepath.Join(directory, entry.Name())
		document, size, err := loadDocument(root, path)
		if err != nil {
			return nil, fmt.Errorf("invalid runbook %q: %w", entry.Name(), err)
		}
		total += size
		if total > maxRunbookTotal {
			return nil, errors.New("runbook collection exceeds size limit")
		}
		if _, duplicate := seenNames[document.Name]; duplicate {
			return nil, errors.New("duplicate runbook name")
		}
		seenNames[document.Name] = struct{}{}
		documents = append(documents, document)
	}
	sort.Slice(documents, func(left, right int) bool {
		return documents[left].Name < documents[right].Name
	})
	return documents, nil
}

func openRunbookDirectory(root string) (string, *os.File, bool, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", nil, false, errors.New("runbook root must be absolute and canonical")
	}
	absoluteRoot := root
	components := []string{
		absoluteRoot,
		filepath.Join(absoluteRoot, "etc"),
		filepath.Join(absoluteRoot, "etc", "ohtools"),
		filepath.Join(absoluteRoot, filepath.FromSlash(runbookDirectory)),
	}
	for _, component := range components {
		info, err := os.Lstat(component)
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, false, nil
		}
		if err != nil {
			return "", nil, false, errors.New("inspect runbook path")
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", nil, false, errors.New("runbook path is not a trusted directory")
		}
		if err := validateTrustedPath(absoluteRoot, info); err != nil {
			return "", nil, false, err
		}
	}
	directory := components[len(components)-1]
	file, err := os.Open(directory)
	if err != nil {
		return "", nil, false, errors.New("open runbook directory")
	}
	openedInfo, err := file.Stat()
	if err != nil {
		file.Close()
		return "", nil, false, errors.New("inspect opened runbook directory")
	}
	pathInfo, err := os.Lstat(directory)
	if err != nil || !os.SameFile(pathInfo, openedInfo) {
		file.Close()
		return "", nil, false, errors.New("runbook directory changed while opening")
	}
	if err := validateTrustedPath(absoluteRoot, openedInfo); err != nil {
		file.Close()
		return "", nil, false, err
	}
	return directory, file, true, nil
}

func loadDocument(root, path string) (Document, int64, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Document{}, 0, errors.New("inspect file")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return Document{}, 0, errors.New("file is not a trusted regular file")
	}
	if err := validateTrustedPath(root, info); err != nil {
		return Document{}, 0, err
	}
	if info.Size() > maxRunbookSize {
		return Document{}, 0, errors.New("file exceeds size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return Document{}, 0, errors.New("open file")
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) {
		return Document{}, 0, errors.New("file changed while opening")
	}
	if err := validateTrustedPath(root, openedInfo); err != nil {
		return Document{}, 0, err
	}
	encoded, err := io.ReadAll(io.LimitReader(file, maxRunbookSize+1))
	if err != nil {
		return Document{}, 0, errors.New("read file")
	}
	if len(encoded) > maxRunbookSize {
		return Document{}, 0, errors.New("file exceeds size limit")
	}
	if !utf8.Valid(encoded) {
		return Document{}, 0, errors.New("file is not valid UTF-8")
	}

	decoder := yaml.NewDecoder(bytes.NewReader(encoded))
	decoder.KnownFields(true)
	var document Document
	if err := decoder.Decode(&document); err != nil {
		return Document{}, 0, errors.New("malformed YAML")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Document{}, 0, errors.New("multiple YAML documents are not allowed")
	}
	var tree yaml.Node
	if err := yaml.Unmarshal(encoded, &tree); err != nil {
		return Document{}, 0, errors.New("malformed YAML")
	}
	if err := validateYAMLTree(&tree); err != nil {
		return Document{}, 0, err
	}
	if err := validateDocument(document); err != nil {
		return Document{}, 0, err
	}
	filename := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if filename != document.Name {
		return Document{}, 0, errors.New("file name does not match runbook name")
	}
	return document, int64(len(encoded)), nil
}
