package backup

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const unitOutputLimit int64 = 16 << 10

type ScheduleStatus struct {
	ProductID    string `json:"product_id"`
	ServiceState string `json:"service_state,omitempty"`
	TimerState   string `json:"timer_state,omitempty"`
}

func executeStatus(
	ctx context.Context,
	options Options,
	local probe.Local,
) (protocol.Result, error) {
	products, checks, failures, err := collectInventory(ctx, local)
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return protocol.Result{}, fatal
		}
		return protocol.Result{}, dependencyFailure("local backup inventory is unavailable")
	}
	schedules := make([]ScheduleStatus, 0, len(adapters))
	for _, current := range adapters {
		if err := ctx.Err(); err != nil {
			return protocol.Result{}, err
		}
		service, serviceFound, serviceIssue, err := inspectUnit(ctx, local, current.Service)
		if err != nil {
			return protocol.Result{}, err
		}
		timer, timerFound, timerIssue, err := inspectUnit(ctx, local, current.Timer)
		if err != nil {
			return protocol.Result{}, err
		}
		if serviceIssue != nil {
			failures = append(failures, backupFailure(
				protocol.ErrorDependency, "service_metadata",
				"A local backup service could not be inspected", current.ID,
			))
		}
		if timerIssue != nil {
			failures = append(failures, backupFailure(
				protocol.ErrorDependency, "timer_metadata",
				"A local backup timer could not be inspected", current.ID,
			))
		}
		schedules = append(schedules, ScheduleStatus{
			ProductID: current.ID, ServiceState: service, TimerState: timer,
		})
		check := protocol.Check{
			ID: "backup." + current.ID + ".timer", Status: protocol.StatusPass,
			Summary: current.Name + " has a local systemd timer",
		}
		if !timerFound {
			check.Status = protocol.StatusInfo
			check.Summary = current.Name + " has no loaded systemd timer"
		}
		if timerIssue != nil {
			check.Status = protocol.StatusPartial
			check.Summary = current.Name + " timer metadata is unavailable"
		}
		if !serviceFound && !timerFound && serviceIssue == nil && timerIssue == nil {
			check.ID = "backup." + current.ID + ".schedule_missing"
		}
		checks = append(checks, check)
	}
	return buildResult(options, "backup status", map[string]any{
		"products": products, "schedules": schedules,
	}, checks, failures), nil
}

func inspectUnit(
	ctx context.Context,
	local probe.Local,
	unit string,
) (string, bool, error, error) {
	output, err := local.Run(ctx, probe.Command{
		Program: "systemctl",
		Arguments: []string{
			"show", unit, "--property=LoadState,ActiveState,SubState", "--no-pager",
		},
		StdoutLimit: unitOutputLimit, StderrLimit: commandErrorLimit,
	})
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return "", false, nil, fatal
		}
		if errors.Is(err, execx.ErrNotFound) {
			return "", false, nil, nil
		}
		return "", false, err, nil
	}
	if output.ExitCode != 0 || output.StdoutTruncated || output.StderrTruncated ||
		len(output.Stderr) != 0 || !utf8.Valid(output.Stdout) {
		return "", false, errors.New("untrusted unit output"), nil
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(string(output.Stdout), "\n"), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || !validUnitKey(key) || !validUnitValue(value) {
			return "", false, errors.New("malformed unit output"), nil
		}
		if _, duplicate := values[key]; duplicate {
			return "", false, errors.New("duplicate unit field"), nil
		}
		values[key] = value
	}
	if values["LoadState"] == "not-found" {
		if len(values) != 1 {
			return "", false, errors.New("unexpected not-found metadata"), nil
		}
		return "", false, nil, nil
	}
	if len(values) != 3 || values["LoadState"] != "loaded" ||
		values["ActiveState"] == "" || values["SubState"] == "" {
		return "", false, errors.New("incomplete unit output"), nil
	}
	return values["ActiveState"] + "/" + values["SubState"], true, nil, nil
}

func validUnitKey(value string) bool {
	switch value {
	case "LoadState", "ActiveState", "SubState":
		return true
	default:
		return false
	}
}

func validUnitValue(value string) bool {
	switch value {
	case "loaded", "not-found", "active", "inactive", "failed", "activating",
		"deactivating", "running", "dead", "exited", "waiting":
		return true
	default:
		return false
	}
}
