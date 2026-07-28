package runbook

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestDefinitionDeclaresApprovedReadOnlyCommands(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{Version: "1.0.0", Root: t.TempDir()})
	got := make([][]string, 0, len(definition.Manifest.Commands))
	for _, command := range definition.Manifest.Commands {
		got = append(got, command.Path)
		if command.Category != protocol.CategoryDiagnostic || command.RequiresRoot ||
			command.RequiresForce || command.RequiresConfirmation || command.SupportsDryRun {
			t.Fatalf("command has mutation requirements: %#v", command)
		}
	}
	want := [][]string{
		{"runbook", "list"},
		{"runbook", "inspect"},
		{"runbook", "validate"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command paths = %#v, want %#v", got, want)
	}
	if err := protocol.ValidateManifest(definition.Manifest); err != nil {
		t.Fatalf("manifest is invalid: %v", err)
	}

	for _, invocation := range []protocol.Invocation{
		testInvocation([]string{"runbook", "list"}),
		testInvocation([]string{"runbook", "inspect"}, "safe-runbook"),
		testInvocation([]string{"runbook", "validate"}),
		testInvocation([]string{"runbook", "validate"}, "safe-runbook"),
	} {
		plan, err := definition.Plan(context.Background(), invocation)
		if err != nil {
			t.Fatalf("plan %v: %v", invocation.CommandPath, err)
		}
		if len(plan.Changes) != 0 || plan.RequiresRoot || plan.RequiresConfirmation {
			t.Fatalf("read-only plan can mutate: %#v", plan)
		}
	}
}

func TestListReturnsSortedBoundedMetadata(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeRunbook(t, root, "zeta-maintenance.yaml", `
schema_version: 1
name: zeta-maintenance
title: Zeta maintenance
description: Check the local service state.
steps:
  - title: Review status
    description: Review the status reported by ohtools.
`)
	writeRunbook(t, root, "alpha-review.yml", `
schema_version: 1
name: alpha-review
title: Alpha review
description: Review local documentation.
steps:
  - title: Read guidance
    description: Read the operator guidance.
`)
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	definition := NewDefinition(Options{
		Version: "1.0.0", Root: root, Host: "runbook-host",
		Now: func() time.Time { return now },
	})

	result, err := definition.Execute(
		context.Background(),
		testInvocation([]string{"runbook", "list"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := []RunbookSummary{
		{
			Name: "alpha-review", Title: "Alpha review",
			Description: "Review local documentation.", StepCount: 1,
		},
		{
			Name: "zeta-maintenance", Title: "Zeta maintenance",
			Description: "Check the local service state.", StepCount: 1,
		},
	}
	if got := result.Data["runbooks"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("runbooks = %#v, want %#v", got, want)
	}
	if result.Status != protocol.StatusPass || len(result.Errors) != 0 ||
		len(result.Changes) != 0 || result.Command != "runbook list" {
		t.Fatalf("unexpected list result: %#v", result)
	}
}

func TestInspectReturnsNormalizedPlainText(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeRunbook(t, root, "safe-recovery.yaml", `
schema_version: 1
name: safe-recovery
title: "  Safe   recovery  "
description: |
  Review the local state.
  Record the observations.
steps:
  - title: "  Review   status "
    description: |
      Read the status.
      Compare it with the approved baseline.
  - title: Record outcome
    description: Document the result in the incident record.
`)
	definition := NewDefinition(Options{Root: root})

	result, err := definition.Execute(
		context.Background(),
		testInvocation([]string{"runbook", "inspect"}, "safe-recovery"),
	)
	if err != nil {
		t.Fatal(err)
	}
	want := RunbookView{
		Name:        "safe-recovery",
		Title:       "Safe recovery",
		Description: "Review the local state. Record the observations.",
		Steps: []StepView{
			{
				Number: 1, Title: "Review status",
				Description: "Read the status. Compare it with the approved baseline.",
			},
			{
				Number: 2, Title: "Record outcome",
				Description: "Document the result in the incident record.",
			},
		},
	}
	if got := result.Data["runbook"]; !reflect.DeepEqual(got, want) {
		t.Fatalf("runbook = %#v, want %#v", got, want)
	}
	if result.Command != "runbook inspect" || result.Status != protocol.StatusPass {
		t.Fatalf("unexpected inspect result: %#v", result)
	}
}

func TestValidateReportsValidDocumentsForOneOrAll(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeRunbook(t, root, "alpha-review.yaml", `
schema_version: 1
name: alpha-review
title: Alpha review
description: Review local documentation.
steps:
  - title: Read guidance
    description: Read the operator guidance.
`)
	writeRunbook(t, root, "zeta-review.yaml", `
schema_version: 1
name: zeta-review
title: Zeta review
description: Review the final state.
steps:
  - title: Record status
    description: Record a sanitized status summary.
`)
	definition := NewDefinition(Options{Root: root})

	all, err := definition.Execute(
		context.Background(),
		testInvocation([]string{"runbook", "validate"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	wantAll := []ValidationView{
		{Name: "alpha-review", Valid: true},
		{Name: "zeta-review", Valid: true},
	}
	if got := all.Data["validations"]; !reflect.DeepEqual(got, wantAll) {
		t.Fatalf("all validations = %#v, want %#v", got, wantAll)
	}

	one, err := definition.Execute(
		context.Background(),
		testInvocation([]string{"runbook", "validate"}, "zeta-review"),
	)
	if err != nil {
		t.Fatal(err)
	}
	wantOne := []ValidationView{{Name: "zeta-review", Valid: true}}
	if got := one.Data["validations"]; !reflect.DeepEqual(got, wantOne) {
		t.Fatalf("one validation = %#v, want %#v", got, wantOne)
	}
	if all.Status != protocol.StatusPass || one.Status != protocol.StatusPass {
		t.Fatalf("validation status = all %q one %q", all.Status, one.Status)
	}
}

func TestValidateRejectsUnsafeYAMLWithoutLeakingContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{
			name: "unknown root field",
			content: validRunbookYAML("unsafe-document") +
				"owner: operator\n",
		},
		{
			name: "executable shell step",
			content: strings.Replace(
				validRunbookYAML("unsafe-document"),
				"    description: Read the approved guidance.\n",
				"    description: Read the approved guidance.\n    shell: /bin/sh\n",
				1,
			),
		},
		{
			name: "executable argv step",
			content: strings.Replace(
				validRunbookYAML("unsafe-document"),
				"    description: Read the approved guidance.\n",
				"    description: Read the approved guidance.\n    argv: [rm, -rf, /]\n",
				1,
			),
		},
		{
			name: "URL field",
			content: strings.Replace(
				validRunbookYAML("unsafe-document"),
				"description: Review local documentation.\n",
				"description: Review local documentation.\nurl: https://example.invalid/runbook\n",
				1,
			),
		},
		{
			name: "include",
			content: strings.Replace(
				validRunbookYAML("unsafe-document"),
				"description: Review local documentation.\n",
				"description: Review local documentation.\ninclude: ../../secret.yaml\n",
				1,
			),
		},
		{
			name: "duplicate key",
			content: strings.Replace(
				validRunbookYAML("unsafe-document"),
				"title: Safe document\n",
				"title: Safe document\ntitle: Replacement title\n",
				1,
			),
		},
		{
			name: "YAML alias",
			content: `schema_version: 1
name: unsafe-document
title: Safe document
description: &shared Review local documentation.
steps:
  - title: Read guidance
    description: *shared
`,
		},
		{
			name: "custom YAML tag",
			content: strings.Replace(
				validRunbookYAML("unsafe-document"),
				"Review local documentation.",
				"!unsafe Review local documentation.",
				1,
			),
		},
		{
			name: "URL in documentation",
			content: strings.Replace(
				validRunbookYAML("unsafe-document"),
				"Review local documentation.",
				"Open https://alice:url-secret@example.invalid/runbook.",
				1,
			),
		},
		{
			name: "inline credential",
			content: strings.Replace(
				validRunbookYAML("unsafe-document"),
				"Review local documentation.",
				"Use token=inline-credential-secret during review.",
				1,
			),
		},
		{
			name: "raw HTML",
			content: strings.Replace(
				validRunbookYAML("unsafe-document"),
				"Review local documentation.",
				"<script>html-secret</script>",
				1,
			),
		},
		{
			name: "missing steps",
			content: `schema_version: 1
name: unsafe-document
title: Safe document
description: Review local documentation.
steps: []
`,
		},
		{
			name: "unsupported schema",
			content: strings.Replace(
				validRunbookYAML("unsafe-document"),
				"schema_version: 1",
				"schema_version: 2",
				1,
			),
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeRunbook(t, root, "unsafe-document.yaml", test.content)
			definition := NewDefinition(Options{Root: root})
			_, err := definition.Execute(
				context.Background(),
				testInvocation([]string{"runbook", "validate"}, "unsafe-document"),
			)
			assertExitCode(t, err, protocol.ExitConfiguration)
			if err != nil {
				for _, secret := range []string{
					"url-secret", "inline-credential-secret", "html-secret",
					"alice", "example.invalid",
				} {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("validation error leaks %q: %v", secret, err)
					}
				}
			}
		})
	}
}

func TestLoaderRejectsMalformedUTF8SymlinksAndResourceLimitViolations(t *testing.T) {
	t.Parallel()

	t.Run("malformed YAML", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeRunbook(t, root, "broken.yaml", "schema_version: [\n")
		assertValidationConfigurationFailure(t, root)
	})

	t.Run("invalid UTF-8", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeRunbookBytes(t, root, "broken.yaml", []byte{
			's', 'c', 'h', 'e', 'm', 'a', '_', 'v', 'e', 'r', 's', 'i', 'o', 'n', ':', ' ', '1', '\n',
			'n', 'a', 'm', 'e', ':', ' ', 'b', 'r', 'o', 'k', 'e', 'n', '\n',
			't', 'i', 't', 'l', 'e', ':', ' ', 0xff, '\n',
		})
		assertValidationConfigurationFailure(t, root)
	})

	t.Run("symlinked directory", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		outside := t.TempDir()
		writeRunbook(t, outside, "outside.yaml", validRunbookYAML("outside"))
		path := filepath.Join(root, "etc", "ohtools")
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(outside, "etc", "ohtools", "runbooks")
		if err := os.Symlink(target, filepath.Join(path, "runbooks")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		assertValidationConfigurationFailure(t, root)
	})

	t.Run("symlinked intermediate component", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		outside := t.TempDir()
		writeRunbook(t, outside, "outside.yaml", validRunbookYAML("outside"))
		if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(outside, "etc", "ohtools")
		if err := os.Symlink(target, filepath.Join(root, "etc", "ohtools")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		assertValidationConfigurationFailure(t, root)
	})

	t.Run("symlinked file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.yaml")
		if err := os.WriteFile(outside, []byte(validRunbookYAML("outside")), 0o600); err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(root, "etc", "ohtools", "runbooks")
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(directory, "outside.yaml")); err != nil {
			t.Skipf("symlink unavailable: %v", err)
		}
		assertValidationConfigurationFailure(t, root)
	})

	t.Run("oversized file", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeRunbook(t, root, "oversized.yaml", strings.Repeat("x", maxRunbookSize+1))
		assertValidationConfigurationFailure(t, root)
	})

	t.Run("file count", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		for index := 0; index <= maxRunbookFiles; index++ {
			name := "item-" + strconv.Itoa(index)
			writeRunbook(t, root, name+".yaml", validRunbookYAML(name))
		}
		assertValidationConfigurationFailure(t, root)
	})

	t.Run("directory entry count", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		directory := filepath.Join(root, "etc", "ohtools", "runbooks")
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		for index := 0; index <= maxRunbookFiles; index++ {
			path := filepath.Join(directory, "ignored-"+strconv.Itoa(index)+".txt")
			if err := os.WriteFile(path, []byte("ignored"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		assertValidationConfigurationFailure(t, root)
	})

	t.Run("cumulative size", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		padding := strings.Repeat("safe documentation ", 3300)
		for index := 0; index < 18; index++ {
			name := "item-" + strconv.Itoa(index)
			content := strings.Replace(
				validRunbookYAML(name),
				"Review local documentation.",
				padding,
				1,
			)
			writeRunbook(t, root, name+".yaml", content)
		}
		assertValidationConfigurationFailure(t, root)
	})

	t.Run("duplicate document name", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		writeRunbook(t, root, "duplicate.yaml", validRunbookYAML("duplicate"))
		writeRunbook(t, root, "duplicate.yml", validRunbookYAML("duplicate"))
		assertValidationConfigurationFailure(t, root)
	})
}

func TestCommandsRejectUnsafeNamesOptionsAndExtraArguments(t *testing.T) {
	t.Parallel()

	definition := NewDefinition(Options{Root: t.TempDir()})
	for _, name := range []string{
		"--help", "../escape", "Upper-Case", "two words", "name.yaml",
	} {
		for _, path := range [][]string{
			{"runbook", "inspect"},
			{"runbook", "validate"},
		} {
			_, err := definition.Execute(
				context.Background(),
				testInvocation(path, name),
			)
			assertExitCode(t, err, protocol.ExitArguments)
		}
	}

	listExtra := testInvocation([]string{"runbook", "list"}, "unexpected")
	_, err := definition.Execute(context.Background(), listExtra)
	assertExitCode(t, err, protocol.ExitArguments)

	withOption := testInvocation([]string{"runbook", "validate"})
	withOption.Options = map[string]any{
		"include": "../../outside",
	}
	_, err = definition.Execute(context.Background(), withOption)
	assertExitCode(t, err, protocol.ExitArguments)
}

func TestCommandsPropagateCancellationAndResultsAreDeterministic(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeRunbook(t, root, "safe-review.yaml", validRunbookYAML("safe-review"))
	now := time.Date(2026, 7, 27, 17, 0, 0, 0, time.UTC)
	definition := NewDefinition(Options{
		Root: root, Host: "runbook-host", Now: func() time.Time { return now },
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := definition.Execute(ctx, testInvocation([]string{"runbook", "list"}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled execute error = %v, want context cancellation", err)
	}

	first, err := definition.Execute(
		context.Background(),
		testInvocation([]string{"runbook", "inspect"}, "safe-review"),
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := definition.Execute(
		context.Background(),
		testInvocation([]string{"runbook", "inspect"}, "safe-review"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("inspect is not deterministic:\nfirst=%#v\nsecond=%#v", first, second)
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("result contains caller environment key %q: %s", forbidden, encoded)
		}
	}
}

func TestTrustedMetadataPolicyRequiresRootOwnershipInProduction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		requireRoot bool
		uid         uint32
		mode        os.FileMode
		wantError   bool
	}{
		{name: "root-owned file", requireRoot: true, uid: 0, mode: 0o644},
		{name: "test seam owner", requireRoot: false, uid: 1000, mode: 0o600},
		{
			name: "production non-root owner", requireRoot: true,
			uid: 1000, mode: 0o600, wantError: true,
		},
		{
			name: "group writable", requireRoot: false,
			uid: 1000, mode: 0o620, wantError: true,
		},
		{
			name: "world writable", requireRoot: true,
			uid: 0, mode: 0o606, wantError: true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validateTrustedMetadata(
				test.requireRoot, test.uid, test.mode,
			)
			if (err != nil) != test.wantError {
				t.Fatalf("validate error = %v, wantError %t", err, test.wantError)
			}
		})
	}
}

func TestLoaderRejectsRelativeAndNonCanonicalRoots(t *testing.T) {
	t.Parallel()

	for _, root := range []string{
		".",
		t.TempDir() + string(os.PathSeparator) + ".",
	} {
		definition := NewDefinition(Options{Root: root})
		_, err := definition.Execute(
			context.Background(),
			testInvocation([]string{"runbook", "validate"}),
		)
		assertExitCode(t, err, protocol.ExitConfiguration)
	}
}

func testInvocation(path []string, arguments ...string) protocol.Invocation {
	return protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		RequestID:       "runbook-test",
		CommandPath:     path,
		Arguments:       arguments,
		Options:         map[string]any{},
	}
}

func validRunbookYAML(name string) string {
	return `schema_version: 1
name: ` + name + `
title: Safe document
description: Review local documentation.
steps:
  - title: Read guidance
    description: Read the approved guidance.
`
}

func writeRunbook(t *testing.T, root, name, content string) {
	t.Helper()
	writeRunbookBytes(t, root, name, []byte(content))
}

func writeRunbookBytes(t *testing.T, root, name string, content []byte) {
	t.Helper()
	directory := filepath.Join(root, "etc", "ohtools", "runbooks")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, name), content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertValidationConfigurationFailure(t *testing.T, root string) {
	t.Helper()
	definition := NewDefinition(Options{Root: root})
	_, err := definition.Execute(
		context.Background(),
		testInvocation([]string{"runbook", "validate"}),
	)
	assertExitCode(t, err, protocol.ExitConfiguration)
}

func assertExitCode(t *testing.T, err error, code int) {
	t.Helper()
	var failure protocol.ExitError
	if !errors.As(err, &failure) || failure.Code != code {
		t.Fatalf("execute error = %v, want exit %d", err, code)
	}
}
