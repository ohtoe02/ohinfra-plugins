package serversetup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/redact"
)

type Observation struct {
	Converged bool
	Summary   string
	Details   map[string]any
}

type Profile struct {
	Platform              Platform
	Config                Config
	Items                 []Item
	DesiredAuthorizedKeys map[string][]string
}

type Backend interface {
	Entitled(context.Context, Platform) error
	Observe(context.Context, Item, Profile) (Observation, error)
	Apply(context.Context, Item, Profile) error
	Verify(context.Context, Item, Profile) error
}

type ApprovedObservation struct {
	Details map[string]any
}

type ApprovedApplyBackend interface {
	ApplyApproved(context.Context, Item, Profile, ApprovedObservation) error
}

var ErrApprovedStateChanged = errors.New("approved setup state changed")

type DesiredStateMaterialProvider interface {
	PrepareDesiredState(context.Context, *Profile) (map[string]string, error)
}

type Manager struct {
	Backend       Backend
	Config        Config
	Platform      Platform
	ConfigPath    string
	ConfigMissing bool
	Host          string
	Tool          protocol.Tool
	Now           func() time.Time
}

func (manager Manager) Check(ctx context.Context, requested []string) protocol.Result {
	started := manager.now()
	items, err := ExpandItems(requested, manager.Config)
	if err != nil {
		return normalizeResult(protocol.Result{
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
	if manager.ConfigMissing {
		status = protocol.StatusWarning
		checks = append(checks, protocol.Check{
			ID:     "setup:configuration",
			Status: protocol.StatusWarning,
			Summary: "mutation profile is absent; create " +
				manager.ConfigPath + " before running setup apply",
			Details: map[string]any{"path": manager.ConfigPath},
		})
	}
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
			recommendation := "ohtools setup apply " + string(item)
			recommendations = append(recommendations, recommendation)
			summary += "; run " + recommendation
		}
		checks = append(checks, protocol.Check{
			ID: "setup:" + string(item), Status: checkStatus,
			Summary: summary, Details: observation.Details,
		})
	}
	return normalizeResult(protocol.Result{
		Command: "setup check", Status: status,
		Timestamp:  manager.now().Format(time.RFC3339Nano),
		DurationMS: manager.now().Sub(started).Milliseconds(),
		Host:       manager.Host, Tool: manager.Tool,
		Checks: checks,
		Data: map[string]any{
			"platform":           manager.Platform,
			"recommendations":    recommendations,
			"configuration_path": manager.ConfigPath,
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
	desiredStateSHA256, err := manager.desiredStateFingerprint(ctx, &profile)
	if err != nil {
		return protocol.Plan{}, fmt.Errorf("fingerprint desired setup state: %w", err)
	}
	plan := protocol.Plan{
		Summary: "Converge the selected server setup profile",
		Checks:  []protocol.Check{},
		Changes: []protocol.Change{},
		Risks: []string{
			"Configuration and package changes are applied to the local host",
		},
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
			Summary: observation.Summary,
			Details: withDesiredStateFingerprint(
				observation.Details,
				desiredStateSHA256,
			),
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
	approvedFingerprint, err := planDesiredStateFingerprint(approved)
	if err != nil {
		return protocol.Result{}, protocol.ExitError{Code: protocol.ExitArguments, Err: err}
	}
	currentFingerprint, err := manager.desiredStateFingerprint(ctx, &profile)
	if err != nil {
		return protocol.Result{}, err
	}
	if approvedFingerprint != currentFingerprint {
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitArguments,
			Err:  errors.New("approved setup plan no longer matches desired state"),
		}
	}
	completed := make([]protocol.Change, 0, len(approved.Changes))
	for _, change := range approved.Changes {
		item := Item(change.Object)
		if _, err := ExpandItems([]string{string(item)}, manager.Config); err != nil {
			return protocol.Result{}, protocol.ExitError{Code: protocol.ExitArguments, Err: err}
		}
		var applyErr error
		if backend, ok := manager.Backend.(ApprovedApplyBackend); ok {
			observation, err := approvedObservation(approved, item)
			if err != nil {
				return protocol.Result{}, protocol.ExitError{
					Code: protocol.ExitArguments,
					Err:  err,
				}
			}
			applyErr = backend.ApplyApproved(ctx, item, profile, observation)
		} else {
			applyErr = manager.Backend.Apply(ctx, item, profile)
		}
		if applyErr != nil {
			code := "setup_apply_failed"
			if errors.Is(applyErr, ErrApprovedStateChanged) {
				code = "setup_plan_stale"
			}
			return manager.failure(started, item, code, applyErr, completed), nil
		}
		if err := manager.Backend.Verify(ctx, item, profile); err != nil {
			return manager.failure(started, item, "setup_verify_failed", err, completed), nil
		}
		completed = append(completed, protocol.Change{
			Object: string(item), Action: "converge", Status: "completed",
		})
	}
	return normalizeResult(protocol.Result{
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

func approvedObservation(
	plan protocol.Plan,
	item Item,
) (ApprovedObservation, error) {
	expectedID := "setup:" + string(item)
	for _, check := range plan.Checks {
		if check.ID == expectedID {
			return ApprovedObservation{Details: check.Details}, nil
		}
	}
	return ApprovedObservation{}, fmt.Errorf(
		"approved setup plan is missing check %q",
		expectedID,
	)
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
	return normalizeResult(protocol.Result{
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

func normalizeResult(input protocol.Result) protocol.Result {
	return redact.Result(protocol.Normalize(input))
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

func (manager Manager) desiredStateFingerprint(
	ctx context.Context,
	profile *Profile,
) (string, error) {
	config := normalizedConfig(profile.Config)
	material := map[string]string{}
	if provider, ok := manager.Backend.(DesiredStateMaterialProvider); ok {
		var err error
		material, err = provider.PrepareDesiredState(ctx, profile)
		if err != nil {
			return "", err
		}
	}
	encoded, err := json.Marshal(struct {
		Platform Platform          `json:"platform"`
		Config   Config            `json:"config"`
		Items    []Item            `json:"items"`
		Material map[string]string `json:"material_sha256"`
	}{
		Platform: profile.Platform,
		Config:   config,
		Items:    append([]Item(nil), profile.Items...),
		Material: material,
	})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func normalizedConfig(input Config) Config {
	output := input
	output.EnabledItems = append([]string(nil), input.EnabledItems...)
	output.Packages = append([]string(nil), input.Packages...)
	slices.Sort(output.EnabledItems)
	slices.Sort(output.Packages)
	output.Administrators = append([]Administrator(nil), input.Administrators...)
	for index := range output.Administrators {
		output.Administrators[index].Groups = append(
			[]string(nil),
			output.Administrators[index].Groups...,
		)
		output.Administrators[index].AuthorizedKeySources = append(
			[]string(nil),
			output.Administrators[index].AuthorizedKeySources...,
		)
		slices.Sort(output.Administrators[index].Groups)
		slices.Sort(output.Administrators[index].AuthorizedKeySources)
	}
	slices.SortFunc(output.Administrators, func(first, second Administrator) int {
		return strings.Compare(first.Name, second.Name)
	})
	if input.Zabbix != nil {
		zabbix := *input.Zabbix
		output.Zabbix = &zabbix
	}
	return output
}

func withDesiredStateFingerprint(
	input map[string]any,
	fingerprint string,
) map[string]any {
	output := make(map[string]any, len(input)+1)
	for key, value := range input {
		output[key] = value
	}
	output["desired_state_sha256"] = fingerprint
	return output
}

func planDesiredStateFingerprint(plan protocol.Plan) (string, error) {
	fingerprint := ""
	for _, check := range plan.Checks {
		value, ok := check.Details["desired_state_sha256"].(string)
		if !ok || len(value) != 64 {
			return "", errors.New("approved setup plan lacks desired-state fingerprint")
		}
		if fingerprint == "" {
			fingerprint = value
		} else if fingerprint != value {
			return "", errors.New("approved setup plan has inconsistent desired-state fingerprints")
		}
	}
	if fingerprint == "" {
		return "", errors.New("approved setup plan lacks checks")
	}
	return fingerprint, nil
}
