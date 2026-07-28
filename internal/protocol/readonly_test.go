package protocol

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestReadOnlyPlanHasNoChangesOrMutationRequirements(t *testing.T) {
	t.Parallel()

	manifest := readOnlyTestManifest()
	plan, err := ReadOnlyPlan(context.Background(), Invocation{
		ProtocolVersion: ProtocolVersion,
		CommandPath:     []string{"network", "overview"},
		Arguments:       []string{},
		Options:         map[string]any{},
	}, manifest)
	if err != nil {
		t.Fatal(err)
	}

	want := Plan{
		CommandID: "network.overview",
		Summary:   "Read local system state without making changes",
		Checks:    []Check{},
		Changes:   []Change{},
		Risks:     []string{},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("ReadOnlyPlan() = %#v, want %#v", plan, want)
	}
}

func TestReadOnlyPlanValidatesManifestCommandArgumentsAndOptions(t *testing.T) {
	t.Parallel()

	manifest := readOnlyTestManifest()
	for _, test := range []struct {
		name       string
		invocation Invocation
	}{
		{
			name: "unknown command",
			invocation: Invocation{
				ProtocolVersion: ProtocolVersion,
				CommandPath:     []string{"network", "missing"},
			},
		},
		{
			name: "missing required argument",
			invocation: Invocation{
				ProtocolVersion: ProtocolVersion,
				CommandPath:     []string{"tls", "inspect"},
			},
		},
		{
			name: "unexpected argument",
			invocation: Invocation{
				ProtocolVersion: ProtocolVersion,
				CommandPath:     []string{"network", "overview"},
				Arguments:       []string{"extra"},
			},
		},
		{
			name: "unknown option",
			invocation: Invocation{
				ProtocolVersion: ProtocolVersion,
				CommandPath:     []string{"incident", "timeline"},
				Options:         map[string]any{"remote": true},
			},
		},
		{
			name: "wrong option type",
			invocation: Invocation{
				ProtocolVersion: ProtocolVersion,
				CommandPath:     []string{"incident", "timeline"},
				Options:         map[string]any{"since": true},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ReadOnlyPlan(context.Background(), test.invocation, manifest)
			var failure ExitError
			if !errors.As(err, &failure) || failure.Code != ExitArguments {
				t.Fatalf("ReadOnlyPlan() error = %v, want ExitArguments", err)
			}
		})
	}

	for _, invocation := range []Invocation{
		{
			ProtocolVersion: ProtocolVersion,
			CommandPath:     []string{"tls", "inspect"},
			Arguments:       []string{"/etc/ssl/cert.pem"},
		},
		{
			ProtocolVersion: ProtocolVersion,
			CommandPath:     []string{"incident", "timeline"},
			Options:         map[string]any{"since": "30m"},
		},
	} {
		if _, err := ReadOnlyPlan(context.Background(), invocation, manifest); err != nil {
			t.Fatalf("valid invocation rejected: %v", err)
		}
	}
}

func TestReadOnlyPlanReturnsContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ReadOnlyPlan(ctx, Invocation{
		ProtocolVersion: ProtocolVersion,
		CommandPath:     []string{"network", "overview"},
	}, readOnlyTestManifest())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ReadOnlyPlan() error = %v, want context.Canceled", err)
	}
}

func readOnlyTestManifest() Manifest {
	return Manifest{
		ProtocolVersion: ProtocolVersion,
		Name:            "network-base",
		Version:         "1.0.0",
		Commands: []Command{
			{
				Path:      []string{"network", "overview"},
				Use:       "overview",
				Short:     "Show a local network overview",
				Category:  CategoryDiagnostic,
				Arguments: []Argument{},
				Flags:     []Flag{},
			},
			{
				Path:     []string{"tls", "inspect"},
				Use:      "inspect <path>",
				Short:    "Inspect a local TLS certificate",
				Category: CategoryDiagnostic,
				Arguments: []Argument{{
					Name:     "path",
					Required: true,
				}},
				Flags: []Flag{},
			},
			{
				Path:      []string{"incident", "timeline"},
				Use:       "timeline",
				Short:     "Show a local incident timeline",
				Category:  CategoryDiagnostic,
				Arguments: []Argument{},
				Flags: []Flag{{
					Name: "since",
					Type: "duration",
				}},
			},
		},
	}
}
