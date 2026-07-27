package runbook

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

const (
	maxRunbookTitle       = 128
	maxRunbookDescription = 2048
	maxRunbookSteps       = 64
	maxStepDescription    = 4096
)

var (
	urlReference = regexp.MustCompile(
		`(?i)(?:\b[a-z][a-z0-9+.-]*://|\bwww\.)`,
	)
	inlineCredential = regexp.MustCompile(
		`(?i)\b(?:password|passwd|passphrase|secret|token|api[-_]?key|authorization|credential)\s*[:=]\s*\S+`,
	)
)

type ValidationView struct {
	Name  string `json:"name"`
	Valid bool   `json:"valid"`
	Code  string `json:"code,omitempty"`
}

func validationViews(documents []Document, selected string) []ValidationView {
	validations := make([]ValidationView, 0, len(documents))
	for _, document := range documents {
		if selected == "" || document.Name == selected {
			validations = append(validations, ValidationView{
				Name: document.Name, Valid: true,
			})
		}
	}
	return validations
}

func validateYAMLTree(node *yaml.Node) error {
	if node == nil {
		return errors.New("missing YAML document")
	}
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("YAML aliases and anchors are not allowed")
	}
	switch node.Tag {
	case "", "!!map", "!!seq", "!!str", "!!int":
	default:
		return errors.New("custom YAML tags are not allowed")
	}
	for _, child := range node.Content {
		if err := validateYAMLTree(child); err != nil {
			return err
		}
	}
	return nil
}

func validateDocument(document Document) error {
	if document.SchemaVersion != 1 {
		return errors.New("unsupported schema version")
	}
	if !runbookName.MatchString(document.Name) {
		return errors.New("invalid runbook name")
	}
	if err := validateText("title", document.Title, maxRunbookTitle); err != nil {
		return err
	}
	if err := validateText(
		"description", document.Description, maxRunbookDescription,
	); err != nil {
		return err
	}
	if len(document.Steps) == 0 || len(document.Steps) > maxRunbookSteps {
		return errors.New("runbook step count must be within limits")
	}
	for _, step := range document.Steps {
		if err := validateText("step title", step.Title, maxRunbookTitle); err != nil {
			return err
		}
		if err := validateText(
			"step description", step.Description, maxStepDescription,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateText(field, value string, limit int) error {
	if !utf8.ValidString(value) {
		return errors.New(field + " must be valid UTF-8")
	}
	normalized := normalizeText(value)
	if normalized == "" || len(normalized) > limit {
		return errors.New(field + " length is outside limits")
	}
	for _, character := range value {
		if unicode.IsControl(character) &&
			character != '\n' && character != '\r' && character != '\t' {
			return errors.New(field + " contains control characters")
		}
	}
	if strings.ContainsAny(value, "<>") {
		return errors.New(field + " contains raw HTML")
	}
	if urlReference.MatchString(value) {
		return errors.New(field + " contains a URL")
	}
	if inlineCredential.MatchString(value) {
		return errors.New(field + " contains an inline credential")
	}
	return nil
}
