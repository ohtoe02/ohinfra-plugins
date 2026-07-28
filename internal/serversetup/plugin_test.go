package serversetup

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestDefinitionExposesOnlyApprovedCommands(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{Version: "1.0.0"})
	if err := protocol.ValidateManifest(definition.Manifest); err != nil {
		t.Fatalf("ValidateManifest() error = %v", err)
	}

	got := make([][]string, 0, len(definition.Manifest.Commands))
	for _, command := range definition.Manifest.Commands {
		got = append(got, command.Path)
	}
	want := [][]string{
		{"setup", "check"},
		{"setup", "apply"},
		{"setup", "upgrade"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command paths = %#v, want %#v", got, want)
	}

	check := definition.Manifest.Commands[0]
	if check.Category != protocol.CategoryDiagnostic || check.RequiresRoot ||
		check.SupportsDryRun || check.RequiresConfirmation {
		t.Fatalf("setup check contract = %#v", check)
	}
	for _, command := range definition.Manifest.Commands[1:] {
		if command.Category != protocol.CategoryOperational || !command.RequiresRoot ||
			!command.SupportsDryRun || !command.RequiresConfirmation {
			t.Fatalf("mutation command contract = %#v", command)
		}
	}
}

func TestDefinitionRejectsUnknownArgumentsAndOptions(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{Version: "1.0.0"})
	for _, invocation := range []protocol.Invocation{
		{
			ProtocolVersion: protocol.ProtocolVersion,
			CommandPath:     []string{"setup", "unknown"},
		},
		{
			ProtocolVersion: protocol.ProtocolVersion,
			CommandPath:     []string{"setup", "check"},
			Arguments:       []string{"unexpected"},
		},
		{
			ProtocolVersion: protocol.ProtocolVersion,
			CommandPath:     []string{"setup", "check"},
			Options:         map[string]any{"url": "https://example.invalid"},
		},
	} {
		_, err := definition.Execute(context.Background(), invocation)
		var failure protocol.ExitError
		if !errors.As(err, &failure) || failure.Code != protocol.ExitArguments {
			t.Fatalf("Execute(%#v) error = %v", invocation, err)
		}
	}
}

func TestDefinitionExecutesSetupCheck(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	definition := NewDefinition(Options{
		Version: "1.0.0", Root: fixture.root,
		ConfigPath: fixture.path("missing-config.yaml"),
		Host:       "server01", Runner: healthyRunner(t),
		Now: func() time.Time {
			return fixture.now
		},
		Identity: FileIdentityReaderFunc(
			func(path string, _ os.FileInfo) (FileIdentity, error) {
				return expectedFixtureIdentity(path), nil
			},
		),
	})

	result, err := definition.Execute(context.Background(), protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     []string{"setup", "check"},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Status != protocol.StatusPass ||
		result.Data["profile"] != "debian-12" ||
		result.Tool.Name != Name {
		t.Fatalf("result = %#v", result)
	}
}

func TestDefinitionReturnsGuidanceForUnsupportedPlatform(t *testing.T) {
	t.Parallel()

	fixture := newCheckFixture(t, "debian-12")
	if err := os.WriteFile(
		fixture.path("etc/os-release"),
		[]byte("ID=fedora\nVERSION_ID=42\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	definition := NewDefinition(Options{
		Version: "1.0.0", Root: fixture.root,
		ConfigPath: fixture.path("missing-config.yaml"),
		Host:       "server01",
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			t.Fatal("unsupported platform must not run local commands")
			return execx.Output{}, nil
		}),
		Now: func() time.Time { return fixture.now },
	})

	result, err := definition.Execute(context.Background(), protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     []string{"setup", "check"},
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Status != protocol.StatusCritical ||
		result.Data["platform_id"] != "fedora" ||
		result.Data["platform_version"] != "42" ||
		len(result.Errors) != 1 ||
		result.Errors[0].Kind != protocol.ErrorConfiguration {
		t.Fatalf("result = %#v", result)
	}
}

func TestDefinitionReturnsFatalContextErrorsDuringCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(context.CancelFunc) execx.Runner
		want  error
	}{
		{
			name: "cancelled between probes",
			setup: func(cancel context.CancelFunc) execx.Runner {
				return execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
					if spec.Program == "dpkg-query" {
						cancel()
						return healthyPackageOutput(), nil
					}
					t.Fatalf("command ran after cancellation: %#v", spec)
					return execx.Output{}, nil
				})
			},
			want: context.Canceled,
		},
		{
			name: "deadline returned by unit probe",
			setup: func(context.CancelFunc) execx.Runner {
				return execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
					if spec.Program == "dpkg-query" {
						return healthyPackageOutput(), nil
					}
					return execx.Output{}, context.DeadlineExceeded
				})
			},
			want: context.DeadlineExceeded,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := newCheckFixture(t, "debian-12")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			definition := NewDefinition(Options{
				Version: "1.0.0", Root: fixture.root,
				ConfigPath: fixture.path("missing-config.yaml"),
				Host:       "server01", Runner: test.setup(cancel),
				Now: func() time.Time { return fixture.now },
			})

			_, err := definition.Execute(ctx, protocol.Invocation{
				ProtocolVersion: protocol.ProtocolVersion,
				CommandPath:     []string{"setup", "check"},
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("Execute() error = %v, want %v", err, test.want)
			}
		})
	}
}
