package apt

import (
	"errors"
	"os"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const historyFileLimit = 4 << 20

type Transaction struct {
	StartDate string         `json:"start_date"`
	EndDate   string         `json:"end_date,omitempty"`
	Actions   map[string]int `json:"actions"`
}

func collectHistory(
	local probe.Local,
) (map[string]any, []protocol.Check, []protocol.StructuredError) {
	transactions := []Transaction{}
	structuredErrors := []protocol.StructuredError{}
	encoded, err := local.Read("var/log/apt/history.log", historyFileLimit)
	switch {
	case err == nil:
		transactions = parseHistory(string(encoded))
	case errors.Is(err, os.ErrNotExist):
	default:
		structuredErrors = append(structuredErrors, protocol.StructuredError{
			Kind: protocol.ErrorGeneral, Code: "apt_history_unavailable",
			Message: "Unable to inspect local APT history",
		})
	}
	status := protocol.StatusPass
	summary := "Local APT history was inspected"
	if len(structuredErrors) > 0 {
		status = protocol.StatusPartial
		summary = "Local APT history could not be fully inspected"
	}
	return map[string]any{"transactions": transactions}, []protocol.Check{{
		ID: "apt:history", Status: status, Summary: summary,
		Details: map[string]any{"count": len(transactions)},
	}}, structuredErrors
}

func parseHistory(content string) []Transaction {
	output := []Transaction{}
	for _, stanza := range splitStanzas(content) {
		transaction := Transaction{Actions: map[string]int{}}
		for _, raw := range strings.Split(stanza, "\n") {
			key, value, ok := strings.Cut(raw, ":")
			if !ok {
				continue
			}
			key = strings.ToLower(strings.TrimSpace(key))
			value = strings.Join(strings.Fields(value), " ")
			switch key {
			case "start-date":
				transaction.StartDate = value
			case "end-date":
				transaction.EndDate = value
			case "install", "upgrade", "remove", "purge", "downgrade", "reinstall":
				transaction.Actions[key] = countTopLevelItems(value)
			}
		}
		if transaction.StartDate != "" {
			output = append(output, transaction)
		}
	}
	return output
}

func countTopLevelItems(value string) int {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	count := 1
	depth := 0
	for _, character := range value {
		switch character {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				count++
			}
		}
	}
	return count
}
