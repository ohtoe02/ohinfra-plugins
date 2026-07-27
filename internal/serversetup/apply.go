package serversetup

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/redact"
	"github.com/ohtoe02/ohtools-plugins/internal/resultbuilder"
)

func executeMutation(
	ctx context.Context,
	invocation protocol.Invocation,
	options Options,
	manifest protocol.Manifest,
) (protocol.Result, error) {
	if invocation.PlanDigest == "" {
		return protocol.Result{}, argumentError("setup mutation requires a plan digest")
	}
	plan, err := buildCurrentPlan(ctx, invocation, options, manifest)
	if err != nil {
		return protocol.Result{}, err
	}
	digest, err := protocol.PlanDigest(plan)
	if err != nil {
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitGeneral, Err: err,
		}
	}
	if subtle.ConstantTimeCompare([]byte(invocation.PlanDigest), []byte(digest)) != 1 {
		return protocol.Result{}, argumentError("setup plan digest does not match current state")
	}
	if len(plan.Changes) == 0 {
		now := options.Now().UTC()
		return resultbuilder.Build(resultbuilder.Input{
			Command: strings.Join(invocation.CommandPath, " "),
			Tool:    tool(options), Host: options.Host, Started: now, Now: now,
		}), nil
	}
	if options.Transactions == nil {
		return protocol.Result{}, protocol.ExitError{
			Code: protocol.ExitConfiguration,
			Err:  errors.New("setup mutation transaction adapter is unavailable"),
		}
	}
	_, profile, err := loadSetupState(options)
	if err != nil {
		return protocol.Result{}, err
	}
	transaction, err := options.Transactions.Begin(options.Root, profile)
	if err != nil {
		return protocol.Result{}, setupTransactionFailure(err, nil)
	}
	for _, change := range plan.Changes {
		if err := ctx.Err(); err != nil {
			_ = rollbackSetupTransaction(transaction)
			return protocol.Result{}, err
		}
		if err := transaction.Apply(ctx, change); err != nil {
			return protocol.Result{}, setupTransactionFailure(
				err, rollbackSetupTransaction(transaction),
			)
		}
	}
	settings, profile, err := loadSetupState(options)
	if err != nil {
		_ = rollbackSetupTransaction(transaction)
		return protocol.Result{}, err
	}
	verified, err := Checker{
		Root: options.Root, Runner: options.Runner, Host: options.Host,
		Now: options.Now, Tool: tool(options), Identity: options.Identity,
	}.Run(ctx, profile, settings)
	if err != nil {
		_ = rollbackSetupTransaction(transaction)
		return protocol.Result{}, err
	}
	if err := verifyPlannedChanges(plan.Changes, verified.Checks); err != nil {
		return protocol.Result{}, setupTransactionFailure(
			err, rollbackSetupTransaction(transaction),
		)
	}
	if err := transaction.Commit(ctx); err != nil {
		return protocol.Result{}, setupTransactionFailure(
			err, rollbackSetupTransaction(transaction),
		)
	}
	applied := append([]protocol.Change(nil), plan.Changes...)
	for index := range applied {
		applied[index].Status = "applied"
	}
	now := options.Now().UTC()
	return resultbuilder.Build(resultbuilder.Input{
		Command: strings.Join(invocation.CommandPath, " "),
		Tool:    tool(options), Host: options.Host, Started: now, Now: now,
		Checks: verified.Checks, Data: map[string]any{
			"profile": profile.ID,
		},
		Changes: applied,
	}), nil
}

func rollbackSetupTransaction(transaction SetupTransaction) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return transaction.Rollback(ctx)
}

func setupTransactionFailure(primary, rollback error) error {
	message := primary.Error()
	if rollback != nil {
		message += "; rollback failed: " + rollback.Error()
	}
	return protocol.ExitError{
		Code: protocol.ExitGeneral,
		Err:  errors.New(redact.String(message)),
	}
}

func verifyPlannedChanges(
	changes []protocol.Change,
	checks []protocol.Check,
) error {
	statuses := make(map[string]protocol.Status, len(checks))
	for _, check := range checks {
		statuses[check.ID] = check.Status
	}
	for _, change := range changes {
		if change.Action == "upgrade-cached" {
			if statuses[change.Object] == protocol.StatusPass {
				continue
			}
		} else if statuses[change.Object] == protocol.StatusPass {
			continue
		}
		return fmt.Errorf("verification failed for %s", change.Object)
	}
	return nil
}
