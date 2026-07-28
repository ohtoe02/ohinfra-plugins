package apt

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	sourceFileLimit = 1 << 20
	sourceFileCount = 256
)

type Source struct {
	File       string   `json:"file"`
	Types      []string `json:"types"`
	URIs       []string `json:"uris"`
	Suites     []string `json:"suites"`
	Components []string `json:"components"`
}

func collectSources(
	local probe.Local,
) (map[string]any, []protocol.Check, []protocol.StructuredError) {
	sources := []Source{}
	structuredErrors := []protocol.StructuredError{}

	if encoded, err := local.Read("etc/apt/sources.list", sourceFileLimit); err == nil {
		sources = append(sources, parseListSources("etc/apt/sources.list", string(encoded))...)
	} else if !errors.Is(err, os.ErrNotExist) {
		structuredErrors = append(structuredErrors, sourceReadError())
	}

	files, err := regularFiles(local, "etc/apt/sources.list.d", []string{".list", ".sources"}, sourceFileCount)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		structuredErrors = append(structuredErrors, sourceReadError())
	}
	for _, relative := range files {
		encoded, readErr := local.Read(relative, sourceFileLimit)
		if readErr != nil {
			structuredErrors = append(structuredErrors, sourceReadError())
			continue
		}
		if strings.HasSuffix(relative, ".sources") {
			sources = append(sources, parseDeb822Sources(relative, string(encoded))...)
		} else {
			sources = append(sources, parseListSources(relative, string(encoded))...)
		}
	}
	sort.Slice(sources, func(left, right int) bool {
		if sources[left].File == sources[right].File {
			return strings.Join(sources[left].URIs, "\x00") < strings.Join(sources[right].URIs, "\x00")
		}
		return sources[left].File < sources[right].File
	})

	status := protocol.StatusPass
	summary := "APT sources were read from local configuration"
	if len(structuredErrors) > 0 {
		status = protocol.StatusPartial
		summary = "Some APT source files could not be inspected"
	}
	return map[string]any{"sources": sources}, []protocol.Check{{
		ID: "apt:sources", Status: status, Summary: summary,
		Details: map[string]any{"count": len(sources)},
	}}, structuredErrors
}

func parseListSources(file, content string) []Source {
	output := []Source{}
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || (fields[0] != "deb" && fields[0] != "deb-src") {
			continue
		}
		uriIndex := 1
		if strings.HasPrefix(fields[uriIndex], "[") {
			for uriIndex < len(fields) && !strings.HasSuffix(fields[uriIndex], "]") {
				uriIndex++
			}
			uriIndex++
		}
		if uriIndex+1 >= len(fields) {
			continue
		}
		output = append(output, Source{
			File: file, Types: []string{fields[0]},
			URIs:       []string{sanitizeURI(fields[uriIndex])},
			Suites:     []string{fields[uriIndex+1]},
			Components: append([]string(nil), fields[uriIndex+2:]...),
		})
	}
	return output
}

func parseDeb822Sources(file, content string) []Source {
	output := []Source{}
	for _, stanza := range splitStanzas(content) {
		fields := map[string][]string{}
		for _, raw := range strings.Split(stanza, "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			fields[strings.ToLower(strings.TrimSpace(key))] = strings.Fields(strings.TrimSpace(value))
		}
		uris := fields["uris"]
		for index := range uris {
			uris[index] = sanitizeURI(uris[index])
		}
		if len(uris) == 0 {
			continue
		}
		output = append(output, Source{
			File: file, Types: append([]string(nil), fields["types"]...),
			URIs:       append([]string(nil), uris...),
			Suites:     append([]string(nil), fields["suites"]...),
			Components: append([]string(nil), fields["components"]...),
		})
	}
	return output
}

func splitStanzas(content string) []string {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	return strings.Split(normalized, "\n\n")
}

func sanitizeURI(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "[invalid-uri]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}

func sourceReadError() protocol.StructuredError {
	return protocol.StructuredError{
		Kind: protocol.ErrorGeneral, Code: "apt_source_unavailable",
		Message: "Unable to inspect a local APT source file",
	}
}

func regularFiles(
	local probe.Local,
	directory string,
	suffixes []string,
	maxFiles int,
) ([]string, error) {
	exists, err := local.Exists(filepath.FromSlash(directory))
	if err != nil || !exists {
		return nil, err
	}
	path := filepath.Join(local.Root, filepath.FromSlash(directory))
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			continue
		}
		if len(suffixes) > 0 && !hasSuffix(entry.Name(), suffixes) {
			continue
		}
		files = append(files, filepath.Join(filepath.FromSlash(directory), entry.Name()))
		if len(files) > maxFiles {
			return nil, errors.New("local file inventory exceeds limit")
		}
	}
	sort.Strings(files)
	return files, nil
}

func countRegularFiles(
	local probe.Local,
	directory string,
	suffixes []string,
	maxFiles int,
) (int, error) {
	files, err := regularFiles(local, directory, suffixes, maxFiles)
	return len(files), err
}

func hasSuffix(name string, suffixes []string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}
