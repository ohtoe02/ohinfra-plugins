package serversetup

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestExecuteRejectsMissingMismatchedAndDryRunDigestsBeforeMutation(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	if err := os.Remove(fixture.path("etc/ohtools/setup-state.yaml")); err != nil {
		t.Fatal(err)
	}
	begins := 0
	factory := SetupTransactionFactoryFunc(func(string, Profile) (SetupTransaction, error) {
		begins++
		t.Fatal("mutation transaction started without a matching plan digest")
		return nil, nil
	})
	definition := NewDefinition(Options{
		Version: "1.0.0", Root: fixture.root,
		ConfigPath: fixture.path("missing-config.yaml"),
		Host:       "server01", Runner: healthyRunner(t),
		Now: func() time.Time { return fixture.now },
		Identity: FileIdentityReaderFunc(
			func(path string, _ os.FileInfo) (FileIdentity, error) {
				return expectedFixtureIdentity(path), nil
			},
		),
		Transactions: factory,
	})

	for _, invocation := range []protocol.Invocation{
		{
			ProtocolVersion: protocol.ProtocolVersion,
			CommandPath:     []string{"setup", "apply"},
		},
		{
			ProtocolVersion: protocol.ProtocolVersion,
			CommandPath:     []string{"setup", "apply"},
			PlanDigest:      "wrong-digest",
		},
		{
			ProtocolVersion: protocol.ProtocolVersion,
			CommandPath:     []string{"setup", "apply"},
			PlanDigest:      "irrelevant",
			Options:         map[string]any{"dry-run": true},
		},
	} {
		_, err := definition.Execute(context.Background(), invocation)
		var failure protocol.ExitError
		if !errors.As(err, &failure) || failure.Code != protocol.ExitArguments {
			t.Fatalf("Execute(%#v) error = %v", invocation, err)
		}
	}
	if begins != 0 {
		t.Fatalf("transaction begins = %d", begins)
	}
}

func TestExecuteAppliesMatchingPlanAndVerifiesResult(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	if err := os.Remove(fixture.path("etc/ohtools/setup-state.yaml")); err != nil {
		t.Fatal(err)
	}
	applied := []string{}
	begins := 0
	commits := 0
	rollbacks := 0
	factory := SetupTransactionFactoryFunc(func(_ string, profile Profile) (SetupTransaction, error) {
		begins++
		return &recordingTransaction{
			apply: func(_ context.Context, change protocol.Change) error {
				applied = append(applied, change.Object+":"+change.Action)
				if change.Object == "file:/etc/ohtools/setup-state.yaml" {
					file := profile.ManagedFiles[0]
					return os.WriteFile(
						fixture.path(strings.TrimPrefix(file.Path, "/")),
						file.Content,
						os.FileMode(file.Mode),
					)
				}
				return nil
			},
			commit:   func(context.Context) error { commits++; return nil },
			rollback: func(context.Context) error { rollbacks++; return nil },
		}, nil
	})
	definition := mutationDefinition(t, fixture, factory)
	invocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     []string{"setup", "apply"},
	}
	plan, err := definition.Plan(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	invocation.PlanDigest, err = protocol.PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}

	result, err := definition.Execute(context.Background(), invocation)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if commits != 1 || rollbacks != 0 ||
		!reflect.DeepEqual(applied, []string{
			"file:/etc/ohtools/setup-state.yaml:replace",
		}) {
		t.Fatalf("applied = %#v, commits = %d, rollbacks = %d", applied, commits, rollbacks)
	}
	if result.Status != protocol.StatusPass || len(result.Changes) != 1 ||
		result.Changes[0].Status != "applied" {
		t.Fatalf("result = %#v", result)
	}

	secondPlan, err := definition.Plan(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPlan.Changes) != 0 {
		t.Fatalf("second plan = %#v", secondPlan)
	}
	invocation.PlanDigest, err = protocol.PlanDigest(secondPlan)
	if err != nil {
		t.Fatal(err)
	}
	secondResult, err := definition.Execute(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if begins != 1 || len(secondResult.Changes) != 0 {
		t.Fatalf("second result = %#v, transaction begins = %d", secondResult, begins)
	}
}

func TestDefinitionBuildsDefaultTransactionFromInjectedAdapters(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	if err := os.Remove(fixture.path("etc/ohtools/setup-state.yaml")); err != nil {
		t.Fatal(err)
	}
	files := &osMutationFiles{
		setIdentity:   func(string, string, string) error { return nil },
		copyMetadata:  func(string, os.FileInfo) error { return nil },
		syncDirectory: func(string) error { return nil },
	}
	definition := NewDefinition(Options{
		Version: "1.0.0", Root: fixture.root,
		ConfigPath: fixture.path("missing-config.yaml"),
		Host:       "server01", Runner: healthyRunner(t),
		Now: func() time.Time { return fixture.now },
		Identity: FileIdentityReaderFunc(
			func(path string, _ os.FileInfo) (FileIdentity, error) {
				return expectedFixtureIdentity(path), nil
			},
		),
		MutationFiles:    files,
		MutationCommands: &recordingMutationCommands{events: &[]string{}},
	})
	invocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     []string{"setup", "apply"},
	}
	plan, err := definition.Plan(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	invocation.PlanDigest, err = protocol.PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	result, err := definition.Execute(context.Background(), invocation)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Status != protocol.StatusPass || len(result.Changes) != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestExecuteRollsBackAndRedactsTransactionFailure(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	if err := os.Remove(fixture.path("etc/ohtools/setup-state.yaml")); err != nil {
		t.Fatal(err)
	}
	rollbacks := 0
	factory := SetupTransactionFactoryFunc(func(string, Profile) (SetupTransaction, error) {
		return &recordingTransaction{
			apply: func(context.Context, protocol.Change) error {
				return errors.New("password=apply-secret")
			},
			commit: func(context.Context) error {
				t.Fatal("commit called after transaction failure")
				return nil
			},
			rollback: func(context.Context) error {
				rollbacks++
				return errors.New("access_token=rollback-secret")
			},
		}, nil
	})
	definition := mutationDefinition(t, fixture, factory)
	invocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     []string{"setup", "apply"},
	}
	plan, err := definition.Plan(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	invocation.PlanDigest, err = protocol.PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	_, err = definition.Execute(context.Background(), invocation)
	if err == nil || rollbacks != 1 {
		t.Fatalf("Execute() error = %v, rollbacks = %d", err, rollbacks)
	}
	if strings.Contains(err.Error(), "apply-secret") ||
		strings.Contains(err.Error(), "rollback-secret") {
		t.Fatalf("transaction error leaked a secret: %v", err)
	}
}

func TestExecuteRedactsTransactionBeginFailure(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	if err := os.Remove(fixture.path("etc/ohtools/setup-state.yaml")); err != nil {
		t.Fatal(err)
	}
	definition := mutationDefinition(t, fixture, SetupTransactionFactoryFunc(
		func(string, Profile) (SetupTransaction, error) {
			return nil, errors.New("password=begin-secret")
		},
	))
	invocation := protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     []string{"setup", "apply"},
	}
	plan, err := definition.Plan(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	invocation.PlanDigest, err = protocol.PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	_, err = definition.Execute(context.Background(), invocation)
	if err == nil || strings.Contains(err.Error(), "begin-secret") {
		t.Fatalf("Execute() error = %v", err)
	}
}

type recordingTransaction struct {
	apply    func(context.Context, protocol.Change) error
	commit   func(context.Context) error
	rollback func(context.Context) error
}

func (transaction *recordingTransaction) Apply(
	ctx context.Context,
	change protocol.Change,
) error {
	return transaction.apply(ctx, change)
}

func (transaction *recordingTransaction) Commit(ctx context.Context) error {
	return transaction.commit(ctx)
}

func (transaction *recordingTransaction) Rollback(ctx context.Context) error {
	return transaction.rollback(ctx)
}

func mutationDefinition(
	t *testing.T,
	fixture checkFixture,
	factory SetupTransactionFactory,
) protocol.Definition {
	t.Helper()
	return NewDefinition(Options{
		Version: "1.0.0", Root: fixture.root,
		ConfigPath: fixture.path("missing-config.yaml"),
		Host:       "server01", Runner: healthyRunner(t),
		Now: func() time.Time { return fixture.now },
		Identity: FileIdentityReaderFunc(
			func(path string, _ os.FileInfo) (FileIdentity, error) {
				return expectedFixtureIdentity(path), nil
			},
		),
		Transactions: factory,
	})
}
