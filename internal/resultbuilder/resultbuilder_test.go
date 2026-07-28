package resultbuilder

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/redact"
)

func TestBuildSortsChecksAndClassifiesPartial(t *testing.T) {
	got := Build(Input{
		Command: "example status",
		Tool:    protocol.Tool{Name: "example-base", Version: "1.0.0"},
		Host:    "server01",
		Now:     time.Unix(100, 0).UTC(),
		Checks: []protocol.Check{
			{ID: "z", Status: protocol.StatusPass, Summary: "ok"},
			{ID: "a", Status: protocol.StatusSkipped, Summary: "missing"},
		},
		Errors: []protocol.StructuredError{{
			Kind:    protocol.ErrorDependency,
			Code:    "missing_dependency",
			Message: "optional tool is unavailable",
		}},
	})
	if got.Status != protocol.StatusPartial {
		t.Fatalf("status = %s", got.Status)
	}
	if got.Checks[0].ID != "a" || got.Checks[1].ID != "z" {
		t.Fatalf("checks = %#v", got.Checks)
	}
}

func TestBuildUsesStatusPrecedence(t *testing.T) {
	tests := []struct {
		name   string
		checks []protocol.Check
		errors []protocol.StructuredError
		want   protocol.Status
	}{
		{
			name: "critical before warning and partial",
			checks: []protocol.Check{
				{ID: "partial", Status: protocol.StatusSkipped},
				{ID: "warning", Status: protocol.StatusWarning},
				{ID: "critical", Status: protocol.StatusCritical},
			},
			errors: []protocol.StructuredError{{Kind: protocol.ErrorDependency}},
			want:   protocol.StatusCritical,
		},
		{
			name: "warning before partial",
			checks: []protocol.Check{
				{ID: "partial", Status: protocol.StatusSkipped},
				{ID: "warning", Status: protocol.StatusWarning},
			},
			errors: []protocol.StructuredError{{Kind: protocol.ErrorDependency}},
			want:   protocol.StatusWarning,
		},
		{
			name:   "errors classify partial",
			errors: []protocol.StructuredError{{Kind: protocol.ErrorGeneral}},
			want:   protocol.StatusPartial,
		},
		{
			name:   "pass without findings",
			checks: []protocol.Check{{ID: "ok", Status: protocol.StatusPass}},
			want:   protocol.StatusPass,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Build(Input{
				Command: "example status",
				Now:     time.Unix(100, 0).UTC(),
				Checks:  test.checks,
				Errors:  test.errors,
			})
			if got.Status != test.want {
				t.Fatalf("status = %s, want %s", got.Status, test.want)
			}
		})
	}
}

func TestBuildCopiesAndRedactsInput(t *testing.T) {
	started := time.Unix(99, 500_000_000).UTC()
	now := time.Unix(100, 0).UTC()
	input := Input{
		Command: "example status",
		Started: started,
		Now:     now,
		Data: map[string]any{
			"nested": map[string]any{"password": "data-secret"},
			"items":  []any{map[string]any{"token": "item-secret"}},
			"stderr": "raw stderr: pwd=stderr-secret",
		},
		Checks: []protocol.Check{{
			ID:      "check",
			Status:  protocol.StatusPass,
			Summary: "Authorization: Bearer check-secret",
			Details: map[string]any{"password": "check-detail-secret"},
		}},
		Changes: []protocol.Change{
			{
				Object:  "z-object",
				Action:  "z-action",
				Status:  "planned",
				Details: map[string]any{"token": "change-secret"},
			},
			{Object: "a-object", Action: "z-action", Status: "planned"},
			{Object: "a-object", Action: "a-action", Status: "planned"},
		},
		Errors: []protocol.StructuredError{{
			Kind:       protocol.ErrorGeneral,
			Message:    "pwd=error-secret",
			Dependency: "access_token=dependency-secret",
			Details: map[string]any{
				"private_key": "detail-secret",
				"stderr":      "unredacted external output",
			},
		}},
	}
	before := cloneInputForTest(input)

	got := Build(input)

	if !reflect.DeepEqual(input, before) {
		t.Fatalf("Build mutated input:\ngot  %#v\nwant %#v", input, before)
	}
	if got.DurationMS != 500 {
		t.Fatalf("duration_ms = %d, want 500", got.DurationMS)
	}
	if got.Timestamp != now.Format(time.RFC3339Nano) {
		t.Fatalf("timestamp = %q", got.Timestamp)
	}
	if got.Changes[0].Object != "a-object" || got.Changes[0].Action != "a-action" ||
		got.Changes[1].Object != "a-object" || got.Changes[1].Action != "z-action" ||
		got.Changes[2].Object != "z-object" {
		t.Fatalf("changes = %#v", got.Changes)
	}

	rendered := strings.Join([]string{
		got.Checks[0].Summary,
		got.Checks[0].Details["password"].(string),
		got.Changes[2].Details["token"].(string),
		got.Errors[0].Message,
		got.Errors[0].Dependency,
		got.Errors[0].Details["private_key"].(string),
		got.Data["nested"].(map[string]any)["password"].(string),
		got.Data["items"].([]any)[0].(map[string]any)["token"].(string),
	}, " ")
	for _, secret := range []string{
		"check-secret",
		"check-detail-secret",
		"change-secret",
		"error-secret",
		"dependency-secret",
		"detail-secret",
		"data-secret",
		"item-secret",
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("result leaked %q in %q", secret, rendered)
		}
	}
	if _, exists := got.Data["stderr"]; exists {
		t.Fatalf("data exposes raw stderr: %#v", got.Data)
	}
	if _, exists := got.Errors[0].Details["stderr"]; exists {
		t.Fatalf("error details expose raw stderr: %#v", got.Errors[0].Details)
	}

	got.Checks[0].Details["password"] = "changed"
	got.Data["nested"].(map[string]any)["password"] = "changed"
	got.Errors[0].Details["private_key"] = "changed"
	if !reflect.DeepEqual(input, before) {
		t.Fatalf("result aliases input after mutation:\ngot  %#v\nwant %#v", input, before)
	}
}

func TestBuildNormalizesEmptyCollections(t *testing.T) {
	got := Build(Input{Now: time.Unix(100, 0).UTC()})
	if got.Checks == nil || got.Data == nil || got.Changes == nil || got.Errors == nil {
		t.Fatalf("result is not normalized: %#v", got)
	}
}

func TestBuildCopiesTypedNestedMapsAndSlices(t *testing.T) {
	records := []map[string]any{{
		"name": "first",
		"metadata": map[string]string{
			"safe":     "original",
			"password": "typed-secret",
		},
	}}
	input := Input{
		Now:  time.Unix(100, 0).UTC(),
		Data: map[string]any{"records": records},
	}

	got := Build(input)
	gotRecords := got.Data["records"].([]map[string]any)
	gotRecords[0]["name"] = "changed"
	gotRecords[0]["metadata"].(map[string]string)["safe"] = "changed"

	if records[0]["name"] != "first" {
		t.Fatalf("typed slice aliases input: %#v", records)
	}
	metadata := records[0]["metadata"].(map[string]string)
	if metadata["safe"] != "original" {
		t.Fatalf("typed nested map aliases input: %#v", metadata)
	}
}

func TestBuildRemovesStderrFromTypedNestedMapsAndSlices(t *testing.T) {
	records := []map[string]any{{
		"name":   "first",
		"stderr": "raw direct stderr",
		"token":  "top-secret",
		"metadata": map[string]string{
			"safe":          "visible",
			"password":      "nested-secret",
			"stderr_output": "raw nested stderr",
		},
	}}

	got := Build(Input{
		Now:  time.Unix(100, 0).UTC(),
		Data: map[string]any{"records": records},
	})

	gotRecords := got.Data["records"].([]map[string]any)
	if _, exists := gotRecords[0]["stderr"]; exists {
		t.Fatalf("typed map exposes stderr: %#v", gotRecords)
	}
	metadata := gotRecords[0]["metadata"].(map[string]string)
	if _, exists := metadata["stderr_output"]; exists {
		t.Fatalf("typed nested map exposes stderr_output: %#v", metadata)
	}
	if gotRecords[0]["token"] != redact.Mask || metadata["password"] != redact.Mask {
		t.Fatalf("typed nested secrets are not redacted: %#v", gotRecords)
	}
	if gotRecords[0]["name"] != "first" || metadata["safe"] != "visible" {
		t.Fatalf("safe data changed: %#v", gotRecords)
	}
}

func TestBuildCopiesMapsNestedInArrays(t *testing.T) {
	records := [1]map[string]any{{"name": "original"}}
	got := Build(Input{
		Now:  time.Unix(100, 0).UTC(),
		Data: map[string]any{"records": records},
	})

	got.Data["records"].([1]map[string]any)[0]["name"] = "changed"
	if records[0]["name"] != "original" {
		t.Fatalf("array nested map aliases input: %#v", records)
	}
}

func cloneInputForTest(input Input) Input {
	return Input{
		Command: input.Command,
		Tool:    input.Tool,
		Host:    input.Host,
		Started: input.Started,
		Now:     input.Now,
		Checks:  cloneChecksForTest(input.Checks),
		Data:    cloneMapForTest(input.Data),
		Changes: cloneChangesForTest(input.Changes),
		Errors:  cloneErrorsForTest(input.Errors),
	}
}

func cloneChecksForTest(input []protocol.Check) []protocol.Check {
	output := make([]protocol.Check, len(input))
	for index, check := range input {
		output[index] = check
		output[index].Details = cloneMapForTest(check.Details)
	}
	return output
}

func cloneChangesForTest(input []protocol.Change) []protocol.Change {
	output := make([]protocol.Change, len(input))
	for index, change := range input {
		output[index] = change
		output[index].Details = cloneMapForTest(change.Details)
	}
	return output
}

func cloneErrorsForTest(input []protocol.StructuredError) []protocol.StructuredError {
	output := make([]protocol.StructuredError, len(input))
	for index, structuredError := range input {
		output[index] = structuredError
		output[index].Details = cloneMapForTest(structuredError.Details)
	}
	return output
}

func cloneMapForTest(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		switch typed := value.(type) {
		case map[string]any:
			output[key] = cloneMapForTest(typed)
		case []any:
			items := make([]any, len(typed))
			for index, item := range typed {
				if mapped, ok := item.(map[string]any); ok {
					items[index] = cloneMapForTest(mapped)
				} else {
					items[index] = item
				}
			}
			output[key] = items
		default:
			output[key] = value
		}
	}
	return output
}
