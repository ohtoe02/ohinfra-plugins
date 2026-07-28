package backup

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	historyFileLimit int64 = 128 << 10
	historyLineLimit       = 512
)

type HistorySummary struct {
	ProductID     string `json:"product_id"`
	Entries       int    `json:"entries"`
	Successes     int    `json:"successes"`
	Failures      int    `json:"failures"`
	LastTimestamp string `json:"last_timestamp,omitempty"`
}

func executeHistory(
	ctx context.Context,
	options Options,
	local probe.Local,
) (protocol.Result, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	summaries := make([]HistorySummary, 0, len(adapters))
	checks := make([]protocol.Check, 0, len(adapters))
	failures := []protocol.StructuredError{}
	for _, current := range adapters {
		if err := ctx.Err(); err != nil {
			return protocol.Result{}, err
		}
		summary, present, issue := inspectHistory(local, current)
		summaries = append(summaries, summary)
		check := protocol.Check{
			ID: "backup." + current.ID + ".history", Status: protocol.StatusPass,
			Summary: current.Name + " local history is available",
		}
		if !present {
			check.Status = protocol.StatusInfo
			check.Summary = current.Name + " has no local history file"
		}
		if issue != nil {
			check.Status = protocol.StatusPartial
			check.Summary = current.Name + " local history is unsafe or malformed"
			failures = append(failures, backupFailure(
				protocol.ErrorConfiguration, "history_metadata",
				"A local backup history file is unsafe or malformed", current.ID,
			))
		}
		checks = append(checks, check)
	}
	return buildResult(options, "backup history",
		map[string]any{"history": summaries}, checks, failures), nil
}

func inspectHistory(local probe.Local, current adapter) (HistorySummary, bool, error) {
	summary := HistorySummary{ProductID: current.ID}
	data, err := local.Read(current.HistoryPath, historyFileLimit)
	if errors.Is(err, os.ErrNotExist) {
		return summary, false, nil
	}
	if err != nil {
		return summary, false, err
	}
	if !utf8.Valid(data) {
		return summary, true, errors.New("history is not UTF-8")
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	nonempty := 0
	var last time.Time
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonempty++
		if nonempty > historyLineLimit {
			return HistorySummary{ProductID: current.ID}, true, probe.ErrTooLarge
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return HistorySummary{ProductID: current.ID}, true, errors.New("malformed history")
		}
		timestamp, parseErr := time.Parse(time.RFC3339, fields[0])
		if parseErr != nil {
			return HistorySummary{ProductID: current.ID}, true, errors.New("malformed history timestamp")
		}
		message := strings.ToLower(strings.Join(fields[1:], " "))
		switch {
		case strings.Contains(message, "failed"), strings.Contains(message, "error"):
			summary.Failures++
		case strings.Contains(message, "success"), strings.Contains(message, "completed"):
			summary.Successes++
		default:
			return HistorySummary{ProductID: current.ID}, true, errors.New("unknown history outcome")
		}
		summary.Entries++
		if timestamp.After(last) {
			last = timestamp
		}
	}
	if !last.IsZero() {
		summary.LastTimestamp = last.UTC().Format(time.RFC3339)
	}
	return summary, true, nil
}
