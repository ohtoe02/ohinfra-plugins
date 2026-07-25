package serversetup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

type Observation struct {
	Converged bool
	Summary   string
	Details   map[string]any
}

type Profile struct {
	Platform Platform
	Config   Config
	Items    []Item
}

type Backend interface {
	Entitled(context.Context, Platform) error
	Observe(context.Context, Item, Profile) (Observation, error)
	Apply(context.Context, Item, Profile) error
	Verify(context.Context, Item, Profile) error
}

type Manager struct {
	Backend  Backend
	Config   Config
	Platform Platform
	Host     string
	Tool     protocol.Tool
	Now      func() time.Time
}

func (manager Manager) Check(ctx context.Context, requested []string) protocol.Result {
	started := manager.now()
	items, err := ExpandItems(requested, manager.Config)
	if err != nil {
		return protocol.Normalize(protocol.Result{
			Command: "setup check", Status: protocol.StatusError,
			Timestamp:  manager.now().Format(time.RFC3339Nano),
			DurationMS: manager.now().Sub(started).Milliseconds(),
			Host:       manager.Host, Tool: manager.Tool,
			Errors: []protocol.StructuredError{{
				Kind: protocol.ErrorArguments, Code: "invalid_setup_item", Message: err.Error(),
			}},
		})
	}
	profile := Profile{
		Platform: manager.Platform,
		Config:   manager.Config,
		Items:    append([]Item(nil), items...),
	}
	status := protocol.StatusPass
	checks := make([]protocol.Check, 0, len(items))
	recommendations := []string{}
	failures := []protocol.StructuredError{}
	for _, item := range items {
		observation, observeErr := manager.Backend.Observe(ctx, item, profile)
		if observeErr != nil {
			status = protocol.StatusPartial
			checks = append(checks, protocol.Check{
				ID: "setup:" + string(item), Status: protocol.StatusSkipped,
				Summary: string(item) + " could not be inspected",
			})
			failures = append(failures, protocol.StructuredError{
				Kind: protocol.ErrorGeneral, Code: "setup_check_failed",
				Message: fmt.Sprintf("%s: %v", item, observeErr),
			})
			continue
		}
		checkStatus := protocol.StatusPass
		summary := observation.Summary
		if !observation.Converged {
			if status == protocol.StatusPass {
				status = protocol.StatusWarning
			}
			checkStatus = protocol.StatusWarning
			recommendation := "sudo ohtools setup apply " + string(item)
			recommendations = append(recommendations, recommendation)
			summary += "; run " + recommendation
		}
		checks = append(checks, protocol.Check{
			ID: "setup:" + string(item), Status: checkStatus,
			Summary: summary, Details: observation.Details,
		})
	}
	return protocol.Normalize(protocol.Result{
		Command: "setup check", Status: status,
		Timestamp:  manager.now().Format(time.RFC3339Nano),
		DurationMS: manager.now().Sub(started).Milliseconds(),
		Host:       manager.Host, Tool: manager.Tool,
		Checks: checks,
		Data: map[string]any{
			"platform":        manager.Platform,
			"recommendations": recommendations,
		},
		Errors: failures,
	})
}

func (manager Manager) Plan(ctx context.Context, requested []string) (protocol.Plan, error) {
	if manager.Backend == nil {
		return protocol.Plan{}, protocol.ExitError{
			Code: protocol.ExitDependency, Err: errors.New("server setup backend is unavailable"),
		}
	}
	if manager.Platform.RequiresExtendedSupport {
		if err := manager.Backend.Entitled(ctx, manager.Platform); err != nil {
			return protocol.Plan{}, protocol.ExitError{Code: protocol.ExitDependency, Err: err}
		}
	}
	items, err := ExpandItems(requested, manager.Config)
	if err != nil {
		return protocol.Plan{}, protocol.ExitError{Code: protocol.ExitArguments, Err: err}
	}
	profile := Profile{
		Platform: manager.Platform,
		Config:   manager.Config,
		Items:    append([]Item(nil), items...),
	}
	plan := protocol.Plan{
		CommandID: "setup.apply",
		Summary:   "Converge the selected server setup profile",
		Checks:    []protocol.Check{},
		Changes:   []protocol.Change{},
		Risks: []string{
			"Configuration and package changes are applied to the local host",
		},
		RequiresRoot:         true,
		RequiresConfirmation: true,
	}
	for _, item := range items {
		observation, observeErr := manager.Backend.Observe(ctx, item, profile)
		if observeErr != nil {
			return protocol.Plan{}, fmt.Errorf("inspect %s: %w", item, observeErr)
		}
		status := protocol.StatusWarning
		if observation.Converged {
			status = protocol.StatusPass
		}
		plan.Checks = append(plan.Checks, protocol.Check{
			ID: "setup:" + string(item), Status: status,
			Summary: observation.Summary, Details: observation.Details,
		})
		if !observation.Converged {
			plan.Changes = append(plan.Changes, protocol.Change{
				Object: string(item), Action: "converge", Status: "planned",
			})
		}
	}
	return plan, nil
}

func (manager Manager) Apply(ctx context.Context, approved protocol.Plan) (protocol.Result, error) {
	started := manager.now()
	items, err := planItems(approved)
	if err != nil {
		return protocol.Result{}, protocol.ExitError{Code: protocol.ExitArguments, Err: err}
	}
	profile := Profile{
		Platform: manager.Platform,
		Config:   manager.Config,
		Items:    items,
	}
	completed := make([]protocol.Change, 0, len(approved.Changes))
	for _, change := range approved.Changes {
		item := Item(change.Object)
		if _, err := ExpandItems([]string{string(item)}, manager.Config); err != nil {
			return protocol.Result{}, protocol.ExitError{Code: protocol.ExitArguments, Err: err}
		}
		if err := manager.Backend.Apply(ctx, item, profile); err != nil {
			return manager.failure(started, item, "setup_apply_failed", err, completed), nil
		}
		if err := manager.Backend.Verify(ctx, item, profile); err != nil {
			return manager.failure(started, item, "setup_verify_failed", err, completed), nil
		}
		completed = append(completed, protocol.Change{
			Object: string(item), Action: "converge", Status: "completed",
		})
	}
	return protocol.Normalize(protocol.Result{
		Command: "setup apply", Status: protocol.StatusPass,
		Timestamp:  manager.now().Format(time.RFC3339Nano),
		DurationMS: manager.now().Sub(started).Milliseconds(),
		Host:       manager.Host, Tool: manager.Tool,
		Data: map[string]any{
			"operation": map[string]any{
				"changed": len(completed) > 0,
				"reason":  changeReason(len(completed)),
			},
		},
		Changes: completed,
	}), nil
}

func planItems(plan protocol.Plan) ([]Item, error) {
	items := make([]Item, 0, len(plan.Checks))
	seen := map[Item]bool{}
	for _, check := range plan.Checks {
		const prefix = "setup:"
		if len(check.ID) <= len(prefix) || check.ID[:len(prefix)] != prefix {
			continue
		}
		item := Item(check.ID[len(prefix):])
		if seen[item] {
			continue
		}
		if _, err := ExpandItems([]string{string(item)}, DefaultConfig()); err != nil {
			return nil, err
		}
		seen[item] = true
		items = append(items, item)
	}
	return items, nil
}

func (manager Manager) failure(
	started time.Time,
	item Item,
	code string,
	err error,
	completed []protocol.Change,
) protocol.Result {
	status := protocol.StatusError
	if len(completed) > 0 {
		status = protocol.StatusPartial
	}
	return protocol.Normalize(protocol.Result{
		Command: "setup apply", Status: status,
		Timestamp:  manager.now().Format(time.RFC3339Nano),
		DurationMS: manager.now().Sub(started).Milliseconds(),
		Host:       manager.Host, Tool: manager.Tool,
		Changes: completed,
		Errors: []protocol.StructuredError{{
			Kind: protocol.ErrorGeneral, Code: code,
			Message: fmt.Sprintf("%s: %v", item, err),
		}},
	})
}

func (manager Manager) now() time.Time {
	if manager.Now != nil {
		return manager.Now()
	}
	return time.Now()
}

func changeReason(count int) string {
	if count == 0 {
		return "already_converged"
	}
	return "desired_state_applied"
}
