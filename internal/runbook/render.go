package runbook

import "strings"

type RunbookView struct {
	Name        string     `json:"name"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Steps       []StepView `json:"steps"`
}

type StepView struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

func renderDocument(document Document) RunbookView {
	steps := make([]StepView, 0, len(document.Steps))
	for index, step := range document.Steps {
		steps = append(steps, StepView{
			Number: index + 1, Title: normalizeText(step.Title),
			Description: normalizeText(step.Description),
		})
	}
	return RunbookView{
		Name: document.Name, Title: normalizeText(document.Title),
		Description: normalizeText(document.Description), Steps: steps,
	}
}

func normalizeText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
