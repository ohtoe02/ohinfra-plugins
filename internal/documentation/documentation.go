package plugindocs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

const SchemaVersion = 1

type Document struct {
	SchemaVersion     int      `yaml:"schema_version" json:"schema_version"`
	PluginID          string   `yaml:"plugin_id" json:"plugin_id"`
	Locale            string   `yaml:"locale" json:"locale"`
	DocumentedVersion string   `yaml:"documented_version" json:"documented_version"`
	Title             string   `yaml:"title" json:"title"`
	Summary           string   `yaml:"summary" json:"summary"`
	CommandPaths      []string `yaml:"command_paths" json:"command_paths"`
	LocalOnly         bool     `yaml:"local_only" json:"local_only"`
	Markdown          string   `yaml:"-" json:"markdown"`
}

var (
	pluginIDPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
	semverPattern   = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	commitPattern   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	linkPattern     = regexp.MustCompile(`\[[^\]]*\]\(([^)\s]+)\)`)
	htmlPattern     = regexp.MustCompile(`<[/!?A-Za-z][^>]*>`)
	headingPattern  = regexp.MustCompile(`^#{1,6}\s+(.+?)\s*$`)
)

type Bundle struct {
	SchemaVersion int            `json:"schema_version"`
	SourceCommit  string         `json:"source_commit"`
	Plugins       []BundlePlugin `json:"plugins"`
}

type BundlePlugin struct {
	PluginID          string                       `json:"plugin_id"`
	DocumentedVersion string                       `json:"documented_version"`
	LocalOnly         bool                         `json:"local_only"`
	Locales           map[string]LocalizedDocument `json:"locales"`
}

type LocalizedDocument struct {
	Title        string   `json:"title"`
	Summary      string   `json:"summary"`
	CommandPaths []string `json:"command_paths"`
	Markdown     string   `json:"markdown"`
}

var englishHeadings = []string{
	"Purpose and supported scenarios",
	"Quick start",
	"Commands",
	"How it works",
	"Data access",
	"Results and exit behavior",
	"Configuration",
	"What can be changed",
	"Fixed behavior",
	"Safety",
	"Troubleshooting",
	"Limitations and TODO",
	"Compatibility and source",
}

var russianHeadings = map[string]string{
	"Purpose and supported scenarios": "Назначение и поддерживаемые сценарии",
	"Quick start":                     "Быстрый старт",
	"Commands":                        "Команды",
	"How it works":                    "Как это работает",
	"Data access":                     "Доступ к данным",
	"Results and exit behavior":       "Результаты и коды завершения",
	"Configuration":                   "Конфигурация",
	"What can be changed":             "Что можно изменить",
	"Fixed behavior":                  "Зафиксированное поведение",
	"Safety":                          "Безопасность",
	"Troubleshooting":                 "Диагностика проблем",
	"Limitations and TODO":            "Ограничения и TODO",
	"Compatibility and source":        "Совместимость и исходный код",
}

func ParseDocument(name string, data []byte) (Document, error) {
	frontmatter, markdown, err := splitFrontmatter(data)
	if err != nil {
		return Document{}, fmt.Errorf("%s: %w", name, err)
	}

	var document Document
	decoder := yaml.NewDecoder(bytes.NewReader(frontmatter))
	decoder.KnownFields(true)
	if err := decoder.Decode(&document); err != nil {
		return Document{}, fmt.Errorf("%s: invalid frontmatter: %w", name, err)
	}
	var present map[string]yaml.Node
	if err := yaml.Unmarshal(frontmatter, &present); err != nil {
		return Document{}, fmt.Errorf("%s: invalid frontmatter: %w", name, err)
	}
	if _, ok := present["local_only"]; !ok {
		return Document{}, fmt.Errorf("%s: frontmatter requires local_only", name)
	}
	document.Markdown = strings.TrimSpace(string(markdown)) + "\n"

	if err := validateMetadata(name, document); err != nil {
		return Document{}, err
	}
	if err := validateMarkdown(document); err != nil {
		return Document{}, fmt.Errorf("%s: %w", name, err)
	}
	return document, nil
}

func ValidateSet(documents []Document, manifests map[string][]string) error {
	byPlugin := make(map[string]map[string]Document, len(manifests))
	for _, document := range documents {
		if _, ok := manifests[document.PluginID]; !ok {
			return fmt.Errorf("documentation exists for unknown plugin %q", document.PluginID)
		}
		locales := byPlugin[document.PluginID]
		if locales == nil {
			locales = make(map[string]Document, 2)
			byPlugin[document.PluginID] = locales
		}
		if _, duplicate := locales[document.Locale]; duplicate {
			return fmt.Errorf("duplicate %s documentation for %q", document.Locale, document.PluginID)
		}
		locales[document.Locale] = document
	}

	pluginIDs := make([]string, 0, len(manifests))
	for pluginID := range manifests {
		pluginIDs = append(pluginIDs, pluginID)
	}
	sort.Strings(pluginIDs)

	for _, pluginID := range pluginIDs {
		locales := byPlugin[pluginID]
		for _, locale := range []string{"en", "ru"} {
			if _, ok := locales[locale]; !ok {
				return fmt.Errorf("missing %s documentation for %q", locale, pluginID)
			}
		}
		english := locales["en"]
		russian := locales["ru"]
		if english.DocumentedVersion != russian.DocumentedVersion {
			return fmt.Errorf("documented version mismatch for %q", pluginID)
		}
		if english.LocalOnly != russian.LocalOnly {
			return fmt.Errorf("local_only mismatch for %q", pluginID)
		}
		if err := compareCommands(pluginID, english.CommandPaths, manifests[pluginID]); err != nil {
			return err
		}
		if err := compareCommands(pluginID, russian.CommandPaths, manifests[pluginID]); err != nil {
			return err
		}
	}
	return nil
}

func LoadDirectory(root string) ([]Document, error) {
	var documents []Document
	for _, locale := range []string{"en", "ru"} {
		directory := filepath.Join(root, locale)
		entries, err := os.ReadDir(directory)
		if err != nil {
			return nil, fmt.Errorf("read %s documentation directory: %w", locale, err)
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
				return nil, fmt.Errorf("%s documentation entry %q must be a regular file", locale, entry.Name())
			}
			if filepath.Ext(entry.Name()) != ".md" {
				return nil, fmt.Errorf("%s documentation entry %q must use .md", locale, entry.Name())
			}
			path := filepath.Join(directory, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read documentation %s: %w", path, err)
			}
			document, err := ParseDocument(entry.Name(), data)
			if err != nil {
				return nil, err
			}
			if document.Locale != locale {
				return nil, fmt.Errorf("%s: locale %q does not match directory %q", path, document.Locale, locale)
			}
			documents = append(documents, document)
		}
	}
	sort.Slice(documents, func(i, j int) bool {
		if documents[i].PluginID == documents[j].PluginID {
			return documents[i].Locale < documents[j].Locale
		}
		return documents[i].PluginID < documents[j].PluginID
	})
	return documents, nil
}

func BuildBundle(documents []Document, sourceCommit string) ([]byte, error) {
	if !commitPattern.MatchString(sourceCommit) {
		return nil, errors.New("source commit must be a lowercase 40-character SHA-1")
	}

	grouped := make(map[string]map[string]Document)
	for _, document := range documents {
		locales := grouped[document.PluginID]
		if locales == nil {
			locales = make(map[string]Document, 2)
			grouped[document.PluginID] = locales
		}
		if _, duplicate := locales[document.Locale]; duplicate {
			return nil, fmt.Errorf("duplicate %s documentation for %q", document.Locale, document.PluginID)
		}
		locales[document.Locale] = document
	}

	pluginIDs := make([]string, 0, len(grouped))
	for pluginID := range grouped {
		pluginIDs = append(pluginIDs, pluginID)
	}
	sort.Strings(pluginIDs)

	bundle := Bundle{
		SchemaVersion: SchemaVersion,
		SourceCommit:  sourceCommit,
		Plugins:       make([]BundlePlugin, 0, len(pluginIDs)),
	}
	for _, pluginID := range pluginIDs {
		locales := grouped[pluginID]
		english, hasEnglish := locales["en"]
		russian, hasRussian := locales["ru"]
		if !hasEnglish || !hasRussian {
			return nil, fmt.Errorf("plugin %q requires en and ru documentation", pluginID)
		}
		if english.DocumentedVersion != russian.DocumentedVersion || english.LocalOnly != russian.LocalOnly {
			return nil, fmt.Errorf("localized metadata mismatch for %q", pluginID)
		}
		bundle.Plugins = append(bundle.Plugins, BundlePlugin{
			PluginID:          pluginID,
			DocumentedVersion: english.DocumentedVersion,
			LocalOnly:         english.LocalOnly,
			Locales: map[string]LocalizedDocument{
				"en": localizedDocument(english),
				"ru": localizedDocument(russian),
			},
		})
	}

	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode documentation bundle: %w", err)
	}
	return append(data, '\n'), nil
}

func localizedDocument(document Document) LocalizedDocument {
	return LocalizedDocument{
		Title:        document.Title,
		Summary:      document.Summary,
		CommandPaths: append([]string(nil), document.CommandPaths...),
		Markdown:     document.Markdown,
	}
}

func splitFrontmatter(data []byte) ([]byte, []byte, error) {
	normalized := bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	if !bytes.HasPrefix(normalized, []byte("---\n")) {
		return nil, nil, errors.New("missing YAML frontmatter")
	}
	rest := normalized[len("---\n"):]
	end := bytes.Index(rest, []byte("\n---\n"))
	if end < 0 {
		return nil, nil, errors.New("unterminated YAML frontmatter")
	}
	return rest[:end], rest[end+len("\n---\n"):], nil
}

func validateMetadata(name string, document Document) error {
	if document.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%s: unsupported schema_version %d", name, document.SchemaVersion)
	}
	if !pluginIDPattern.MatchString(document.PluginID) {
		return fmt.Errorf("%s: invalid plugin_id %q", name, document.PluginID)
	}
	if strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)) != document.PluginID {
		return fmt.Errorf("%s: filename must match plugin_id %q", name, document.PluginID)
	}
	if document.Locale != "en" && document.Locale != "ru" {
		return fmt.Errorf("%s: unsupported locale %q", name, document.Locale)
	}
	if !semverPattern.MatchString(document.DocumentedVersion) {
		return fmt.Errorf("%s: invalid documented_version %q", name, document.DocumentedVersion)
	}
	if err := validateSingleLine("title", document.Title); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if err := validateSingleLine("summary", document.Summary); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if len(document.CommandPaths) == 0 {
		return fmt.Errorf("%s: command_paths must not be empty", name)
	}
	seen := make(map[string]struct{}, len(document.CommandPaths))
	for _, commandPath := range document.CommandPaths {
		if strings.TrimSpace(commandPath) != commandPath || commandPath == "" {
			return fmt.Errorf("%s: invalid command path %q", name, commandPath)
		}
		if _, duplicate := seen[commandPath]; duplicate {
			return fmt.Errorf("%s: duplicate command path %q", name, commandPath)
		}
		seen[commandPath] = struct{}{}
	}
	return nil
}

func validateSingleLine(field, value string) error {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("%s must be a non-empty single line", field)
	}
	if len([]byte(value)) > 512 {
		return fmt.Errorf("%s exceeds 512 bytes", field)
	}
	return nil
}

func validateMarkdown(document Document) error {
	required := englishHeadings
	if document.Locale == "ru" {
		required = make([]string, 0, len(englishHeadings))
		for _, heading := range englishHeadings {
			required = append(required, russianHeadings[heading])
		}
	}

	found := make(map[string]bool, len(required))
	slugs := make(map[string]struct{})
	inFence := false
	for lineNumber, line := range strings.Split(document.Markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if htmlPattern.MatchString(line) {
			return fmt.Errorf("raw HTML is forbidden at line %d", lineNumber+1)
		}
		if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "export ") {
			return fmt.Errorf("MDX is forbidden at line %d", lineNumber+1)
		}
		for _, match := range linkPattern.FindAllStringSubmatch(line, -1) {
			if err := validateLink(match[1]); err != nil {
				return fmt.Errorf("unsafe link at line %d: %w", lineNumber+1, err)
			}
		}
		match := headingPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		heading := strings.TrimSpace(match[1])
		found[heading] = true
		slug := headingSlug(heading)
		if _, duplicate := slugs[slug]; duplicate {
			return fmt.Errorf("duplicate heading anchor %q", slug)
		}
		slugs[slug] = struct{}{}
	}
	if inFence {
		return errors.New("unterminated fenced code block")
	}
	for _, heading := range required {
		if !found[heading] {
			return fmt.Errorf("missing required heading %q", heading)
		}
	}
	return nil
}

func validateLink(raw string) error {
	if strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, "../") || strings.HasPrefix(raw, "./") {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Hostname() != "github.com" {
		return fmt.Errorf("%q is not an approved HTTPS GitHub URL", raw)
	}
	if !strings.HasPrefix(parsed.Path, "/ohtoe02/") {
		return fmt.Errorf("%q is outside the approved organization", raw)
	}
	return nil
}

func headingSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	lastDash := false
	for _, current := range value {
		if current >= 'a' && current <= 'z' || current >= '0' && current <= '9' || current >= 'а' && current <= 'я' || current == 'ё' {
			builder.WriteRune(current)
			lastDash = false
			continue
		}
		if !lastDash && builder.Len() > 0 {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
}

func compareCommands(pluginID string, documented, manifest []string) error {
	left := append([]string(nil), documented...)
	right := append([]string(nil), manifest...)
	sort.Strings(left)
	sort.Strings(right)
	if strings.Join(left, "\x00") != strings.Join(right, "\x00") {
		return fmt.Errorf("documented commands for %q do not match manifest: got %v, want %v", pluginID, left, right)
	}
	return nil
}
