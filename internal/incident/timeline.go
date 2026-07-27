package incident

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const (
	defaultTimelineSince = time.Hour
	maxTimelineSince     = 168 * time.Hour
	maxTimelineEvents    = 1024
	journalShortISO      = "2006-01-02T15:04:05-0700"
)

type TimelineEvent struct {
	Timestamp string `json:"timestamp"`
	Category  string `json:"category"`
}

type Timeline struct {
	Since  string          `json:"since"`
	Events []TimelineEvent `json:"events"`
}

func timelineSince(invocation protocol.Invocation) (time.Duration, error) {
	value, present := invocation.Options["since"]
	if !present {
		return defaultTimelineSince, nil
	}
	text, ok := value.(string)
	if !ok {
		return 0, invalidTimelineDuration()
	}
	duration, err := time.ParseDuration(text)
	if err != nil || duration <= 0 || duration > maxTimelineSince {
		return 0, invalidTimelineDuration()
	}
	return duration, nil
}

func invalidTimelineDuration() error {
	return protocol.ExitError{
		Code: protocol.ExitArguments,
		Err:  errors.New("option \"since\" must be a positive duration no greater than 168h"),
	}
}

func executeTimeline(
	ctx context.Context,
	invocation protocol.Invocation,
	options Options,
) (protocol.Result, error) {
	since, err := timelineSince(invocation)
	if err != nil {
		return protocol.Result{}, err
	}
	local := probe.Local{Root: options.Root, Runner: options.Runner}
	output, err := runBounded(
		ctx, local, "journalctl", journalArguments(since.String()), journalOutputLimit,
	)
	if err != nil {
		if contextFailure(err) != nil {
			return protocol.Result{}, err
		}
		return timelineFailure(options, since, "Journal timeline evidence is unavailable"), nil
	}
	events, err := parseTimeline(string(output))
	if err != nil {
		return timelineFailure(options, since, "Journal timeline evidence is malformed"), nil
	}
	return buildResult(options, "incident timeline",
		map[string]any{"timeline": Timeline{Since: since.String(), Events: events}},
		[]protocol.Check{{
			ID: "incident.timeline", Status: protocol.StatusPass,
			Summary: "Bounded local incident timeline collected",
			Details: map[string]any{"event_count": len(events), "since": since.String()},
		}}, nil,
	), nil
}

func timelineFailure(options Options, since time.Duration, message string) protocol.Result {
	return buildResult(options, "incident timeline",
		map[string]any{"timeline": Timeline{Since: since.String(), Events: []TimelineEvent{}}},
		[]protocol.Check{{
			ID: "incident.timeline", Status: protocol.StatusPartial, Summary: message,
		}},
		[]protocol.StructuredError{evidenceFailure("timeline", message)},
	)
}

func parseTimeline(value string) ([]TimelineEvent, error) {
	if strings.TrimSpace(value) == "" {
		return []TimelineEvent{}, nil
	}
	lines := strings.Split(strings.TrimSpace(value), "\n")
	if len(lines) > maxTimelineEvents {
		return nil, errors.New("timeline event limit exceeded")
	}
	events := make([]TimelineEvent, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			return nil, errors.New("malformed journal event")
		}
		if !validJournalTimestamp(fields[0]) {
			return nil, errors.New("malformed journal timestamp")
		}
		events = append(events, TimelineEvent{
			Timestamp: fields[0],
			Category:  journalCategory(strings.ToLower(line)),
		})
	}
	return events, nil
}

func validJournalTimestamp(value string) bool {
	for _, layout := range []string{journalShortISO, time.RFC3339} {
		if _, err := time.Parse(layout, value); err == nil {
			return true
		}
	}
	return false
}

func journalCategory(line string) string {
	switch {
	case strings.Contains(line, "out of memory") || strings.Contains(line, "oom-kill"):
		return "out-of-memory"
	case strings.Contains(line, "systemd") &&
		(strings.Contains(line, "failed") || strings.Contains(line, "failure")):
		return "service-failure"
	case strings.Contains(line, "authentication") ||
		strings.Contains(line, "invalid user") ||
		strings.Contains(line, "failed password"):
		return "authentication"
	case strings.Contains(line, "kernel"):
		return "kernel"
	default:
		return "error"
	}
}
