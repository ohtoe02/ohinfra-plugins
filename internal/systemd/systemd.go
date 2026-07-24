package systemd

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ohtoe02/ohinfra-plugins/internal/execx"
	"github.com/ohtoe02/ohinfra-plugins/internal/protocol"
	"github.com/ohtoe02/ohinfra-plugins/internal/redact"
)

var unitName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:_.@-]{0,254}$`)

type Manager struct {
	Runner        execx.Runner
	Host          string
	Tool          protocol.Tool
	Now           func() time.Time
	VerifyTimeout time.Duration
	Sleep         func(context.Context, time.Duration) error
}

func ValidateUnit(unit string) error {
	if !unitName.MatchString(unit) || strings.HasPrefix(unit, "-") ||
		strings.ContainsAny(unit, `/\`) {
		return fmt.Errorf("invalid systemd unit name %q", unit)
	}
	return nil
}

func (manager Manager) Status(ctx context.Context, unit string) protocol.Result {
	started := time.Now()
	if err := ValidateUnit(unit); err != nil {
		return manager.failure("service status", started, protocol.ErrorArguments, "invalid_unit", err)
	}
	properties, output, err := manager.show(ctx, unit)
	if err != nil {
		return manager.commandFailure("service status", started, "systemctl", err)
	}
	if output.ExitCode != 0 {
		return manager.failure("service status", started, protocol.ErrorGeneral, "systemctl_failed",
			fmt.Errorf("systemctl show exited with %d: %s", output.ExitCode, strings.TrimSpace(string(output.Stderr))))
	}
	data := make(map[string]any, len(properties)+1)
	for key, value := range properties {
		data[toSnakeCase(key)] = value
	}
	data["unit"] = unit
	return manager.success("service status", started, data)
}

func (manager Manager) Logs(ctx context.Context, unit string, since time.Duration, lines int) protocol.Result {
	started := time.Now()
	if err := ValidateUnit(unit); err != nil {
		return manager.failure("service logs", started, protocol.ErrorArguments, "invalid_unit", err)
	}
	if since <= 0 || lines <= 0 || lines > 100000 {
		return manager.failure("service logs", started, protocol.ErrorArguments, "invalid_log_range",
			errors.New("since must be positive and lines must be within 1..100000"))
	}
	output, err := manager.run(ctx, "journalctl",
		"--unit="+unit, "--since=-"+since.String(), "--lines="+strconv.Itoa(lines),
		"--no-pager", "--output=short-iso-precise")
	if err != nil {
		return manager.commandFailure("service logs", started, "journalctl", err)
	}
	if output.ExitCode != 0 {
		return manager.failure("service logs", started, protocol.ErrorGeneral, "journalctl_failed",
			fmt.Errorf("journalctl exited with %d: %s", output.ExitCode, strings.TrimSpace(string(output.Stderr))))
	}
	linesOutput := []string{}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		if line != "" {
			linesOutput = append(linesOutput, redact.String(line))
		}
	}
	return manager.success("service logs", started, map[string]any{
		"unit": unit, "since": since.String(), "lines": linesOutput,
	})
}

func (manager Manager) RestartPlan(ctx context.Context, unit string) (protocol.Plan, error) {
	if err := ValidateUnit(unit); err != nil {
		return protocol.Plan{}, err
	}
	properties, output, err := manager.show(ctx, unit)
	if err != nil {
		return protocol.Plan{}, err
	}
	if output.ExitCode != 0 {
		return protocol.Plan{}, fmt.Errorf("systemctl show exited with %d: %s",
			output.ExitCode, strings.TrimSpace(string(output.Stderr)))
	}
	return protocol.Plan{
		CommandID:    "service.restart",
		Summary:      fmt.Sprintf("Restart %s and verify that it becomes active?", unit),
		RequiresRoot: true, RequiresConfirmation: true,
		Checks: []protocol.Check{{
			ID: "current-state", Status: protocol.StatusInfo,
			Summary: fmt.Sprintf("%s is %s", unit, properties["ActiveState"]),
			Details: map[string]any{"properties": properties},
		}},
		Changes: []protocol.Change{{Object: unit, Action: "restart", Status: "planned"}},
		Risks:   []string{"the service may be temporarily unavailable"},
	}, nil
}

func (manager Manager) ExecuteRestart(ctx context.Context, unit string, plan protocol.Plan) (protocol.Result, error) {
	started := time.Now()
	output, err := manager.run(ctx, "systemctl", "restart", "--", unit)
	if err != nil {
		return protocol.Result{}, err
	}
	if output.ExitCode != 0 {
		return manager.restartFailure(ctx, unit, started, protocol.ErrorGeneral, "restart_failed",
			fmt.Errorf("systemctl restart exited with %d: %s", output.ExitCode, strings.TrimSpace(string(output.Stderr)))), nil
	}
	timeout := manager.VerifyTimeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	verifyContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for {
		verified, err := manager.run(verifyContext, "systemctl", "is-active", "--quiet", "--", unit)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return protocol.Result{}, err
		}
		if err == nil && verified.ExitCode == 0 {
			digest, _ := protocol.PlanDigest(plan)
			return protocol.Normalize(protocol.Result{
				Command: "service restart", Status: protocol.StatusPass,
				Timestamp:  manager.now().Format(time.RFC3339Nano),
				DurationMS: time.Since(started).Milliseconds(), Host: manager.Host, Tool: manager.Tool,
				Checks:  []protocol.Check{{ID: "verify-active", Status: protocol.StatusPass, Summary: unit + " is active"}},
				Data:    map[string]any{"plan_digest": digest},
				Changes: []protocol.Change{{Object: unit, Action: "restart", Status: "completed"}},
			}), nil
		}
		if verifyContext.Err() != nil {
			return manager.restartFailure(ctx, unit, started, protocol.ErrorTimeout, "verify_timeout",
				fmt.Errorf("%s did not become active within %s", unit, timeout)), nil
		}
		if err := manager.sleep(verifyContext, 250*time.Millisecond); err != nil && verifyContext.Err() != nil {
			continue
		}
	}
}

func (manager Manager) restartFailure(
	ctx context.Context,
	unit string,
	started time.Time,
	kind protocol.ErrorKind,
	code string,
	failure error,
) protocol.Result {
	diagnostics := map[string]any{}
	if properties, output, err := manager.show(ctx, unit); err == nil {
		diagnostics["properties"] = properties
		if output.ExitCode != 0 {
			diagnostics["show_error"] = redact.String(string(output.Stderr))
		}
	}
	if logs, err := manager.run(ctx, "journalctl", "--unit="+unit, "--lines=50",
		"--no-pager", "--output=short-iso-precise"); err == nil {
		diagnostics["journal"] = redact.String(string(logs.Stdout))
	}
	return redact.Result(protocol.Normalize(protocol.Result{
		Command: "service restart", Status: protocol.StatusError,
		Timestamp:  manager.now().Format(time.RFC3339Nano),
		DurationMS: time.Since(started).Milliseconds(), Host: manager.Host, Tool: manager.Tool,
		Data:    map[string]any{"diagnostics": diagnostics},
		Changes: []protocol.Change{{Object: unit, Action: "restart", Status: "failed"}},
		Errors:  []protocol.StructuredError{{Kind: kind, Code: code, Message: failure.Error()}},
	}))
}

func (manager Manager) show(ctx context.Context, unit string) (map[string]string, execx.Output, error) {
	output, err := manager.run(ctx, "systemctl", "show", "--no-pager",
		"--property=Id,LoadState,ActiveState,SubState,UnitFileState,MainPID,ExecMainStartTimestamp", "--", unit)
	if err != nil {
		return nil, output, err
	}
	properties := map[string]string{}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		key, value, found := strings.Cut(line, "=")
		if found {
			properties[key] = value
		}
	}
	return properties, output, nil
}

func (manager Manager) run(ctx context.Context, program string, arguments ...string) (execx.Output, error) {
	if manager.Runner == nil {
		return execx.Output{}, execx.ErrNotFound
	}
	return manager.Runner.Run(ctx, execx.Spec{
		Program: program, Arguments: arguments, StdoutLimit: 10 << 20, StderrLimit: 1 << 20,
	})
}

func (manager Manager) commandFailure(command string, started time.Time, dependency string, err error) protocol.Result {
	kind := protocol.ErrorGeneral
	if errors.Is(err, execx.ErrNotFound) {
		kind = protocol.ErrorDependency
	}
	return manager.failure(command, started, kind, "dependency_failed", fmt.Errorf("%s: %w", dependency, err))
}

func (manager Manager) failure(
	command string, started time.Time, kind protocol.ErrorKind, code string, err error,
) protocol.Result {
	status := protocol.StatusError
	if kind == protocol.ErrorDependency {
		status = protocol.StatusSkipped
	}
	return redact.Result(protocol.Normalize(protocol.Result{
		Command: command, Status: status, Timestamp: manager.now().Format(time.RFC3339Nano),
		DurationMS: time.Since(started).Milliseconds(), Host: manager.Host, Tool: manager.Tool,
		Errors: []protocol.StructuredError{{Kind: kind, Code: code, Message: err.Error()}},
	}))
}

func (manager Manager) success(command string, started time.Time, data map[string]any) protocol.Result {
	return redact.Result(protocol.Normalize(protocol.Result{
		Command: command, Status: protocol.StatusPass,
		Timestamp:  manager.now().Format(time.RFC3339Nano),
		DurationMS: time.Since(started).Milliseconds(), Host: manager.Host, Tool: manager.Tool, Data: data,
	}))
}

func (manager Manager) sleep(ctx context.Context, duration time.Duration) error {
	if manager.Sleep != nil {
		return manager.Sleep(ctx, duration)
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func (manager Manager) now() time.Time {
	if manager.Now != nil {
		return manager.Now()
	}
	return time.Now()
}
func toSnakeCase(value string) string {
	var output strings.Builder
	for index, character := range value {
		if character >= 'A' && character <= 'Z' {
			if index > 0 {
				output.WriteByte('_')
			}
			output.WriteRune(character + ('a' - 'A'))
		} else {
			output.WriteRune(character)
		}
	}
	return output.String()
}
