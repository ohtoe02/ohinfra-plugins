package protocol

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type manifestConformance struct {
	SchemaVersion string `json:"schema_version"`
	Cases         []struct {
		Name          string   `json:"name"`
		ErrorContains string   `json:"error_contains"`
		Manifest      Manifest `json:"manifest"`
	} `json:"cases"`
}

func TestVendoredManifestConformance(t *testing.T) {
	t.Parallel()

	var valid manifestConformance
	readConformance(t, "manifest-valid.json", &valid)
	if valid.SchemaVersion != "1" || len(valid.Cases) == 0 {
		t.Fatalf("invalid valid-manifest vector header: %#v", valid)
	}
	for _, test := range valid.Cases {
		test := test
		t.Run("valid/"+test.Name, func(t *testing.T) {
			if err := ValidateManifest(test.Manifest); err != nil {
				t.Fatalf("valid manifest rejected: %v", err)
			}
		})
	}

	var invalid manifestConformance
	readConformance(t, "manifest-invalid.json", &invalid)
	if invalid.SchemaVersion != "1" || len(invalid.Cases) == 0 {
		t.Fatalf("invalid invalid-manifest vector header: %#v", invalid)
	}
	for _, test := range invalid.Cases {
		test := test
		t.Run("invalid/"+test.Name, func(t *testing.T) {
			err := ValidateManifest(test.Manifest)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(test.ErrorContains)) {
				t.Fatalf("error=%v, expected substring %q", err, test.ErrorContains)
			}
		})
	}
}

func TestVendoredPlanDigestConformance(t *testing.T) {
	t.Parallel()

	var vectors struct {
		SchemaVersion    string `json:"schema_version"`
		Algorithm        string `json:"algorithm"`
		Canonicalization string `json:"canonicalization"`
		Encoding         string `json:"encoding"`
		Cases            []struct {
			Name          string `json:"name"`
			CanonicalJSON string `json:"canonical_json"`
			SHA256        string `json:"sha256"`
		} `json:"cases"`
	}
	readConformance(t, "plan-digest.json", &vectors)
	if vectors.SchemaVersion != "1" || vectors.Algorithm != "sha256" ||
		vectors.Canonicalization != "ohtools-plan-json-v1" ||
		vectors.Encoding == "" || len(vectors.Cases) == 0 {
		t.Fatalf("invalid plan-digest vector header: %#v", vectors)
	}
	for _, test := range vectors.Cases {
		test := test
		t.Run(test.Name, func(t *testing.T) {
			var plan Plan
			if err := json.Unmarshal([]byte(test.CanonicalJSON), &plan); err != nil {
				t.Fatal(err)
			}
			digest, err := PlanDigest(plan)
			if err != nil {
				t.Fatal(err)
			}
			if digest != test.SHA256 {
				t.Fatalf("digest = %q, want %q", digest, test.SHA256)
			}
		})
	}
}

func TestVendoredManifestSemanticConformance(t *testing.T) {
	t.Parallel()

	var vectors struct {
		SchemaVersion string `json:"schema_version"`
		HelpTextCases []struct {
			Name          string `json:"name"`
			Unit          string `json:"unit"`
			Repeat        int    `json:"repeat"`
			Valid         bool   `json:"valid"`
			ErrorContains string `json:"error_contains"`
		} `json:"help_text_cases"`
		FlagDefaultCases []struct {
			Name          string `json:"name"`
			Type          string `json:"type"`
			Default       any    `json:"default"`
			Valid         bool   `json:"valid"`
			ErrorContains string `json:"error_contains"`
		} `json:"flag_default_cases"`
	}
	readConformance(t, "manifest-semantics.json", &vectors)
	if vectors.SchemaVersion != "1" || len(vectors.HelpTextCases) == 0 ||
		len(vectors.FlagDefaultCases) == 0 {
		t.Fatalf("invalid manifest semantic vector header: %#v", vectors)
	}
	for _, test := range vectors.HelpTextCases {
		test := test
		t.Run("help/"+test.Name, func(t *testing.T) {
			manifest := semanticManifest()
			manifest.Commands[0].Short = strings.Repeat(test.Unit, test.Repeat)
			assertConformanceValidity(t, ValidateManifest(manifest), test.Valid, test.ErrorContains)
		})
	}
	for _, test := range vectors.FlagDefaultCases {
		test := test
		t.Run("default/"+test.Name, func(t *testing.T) {
			manifest := semanticManifest()
			manifest.Commands[0].Flags = []Flag{{
				Name: "mode", Type: test.Type, Default: test.Default,
			}}
			assertConformanceValidity(t, ValidateManifest(manifest), test.Valid, test.ErrorContains)
		})
	}
}

func TestVendoredInvocationConformance(t *testing.T) {
	t.Parallel()

	type invocationCase struct {
		Name          string `json:"name"`
		Phase         string `json:"phase"`
		CanonicalJSON string `json:"canonical_json"`
		ErrorContains string `json:"error_contains"`
	}
	var valid struct {
		SchemaVersion string           `json:"schema_version"`
		Cases         []invocationCase `json:"cases"`
	}
	readConformance(t, "invocation-valid.json", &valid)
	if valid.SchemaVersion != "1" || len(valid.Cases) == 0 {
		t.Fatalf("invalid valid-invocation vector header: %#v", valid)
	}
	for _, test := range valid.Cases {
		test := test
		t.Run("valid/"+test.Name, func(t *testing.T) {
			if err := validateInvocationVector(test.CanonicalJSON, test.Phase); err != nil {
				t.Fatalf("valid invocation rejected: %v", err)
			}
		})
	}

	var invalid struct {
		SchemaVersion string           `json:"schema_version"`
		Cases         []invocationCase `json:"cases"`
	}
	readConformance(t, "invocation-invalid.json", &invalid)
	if invalid.SchemaVersion != "1" || len(invalid.Cases) == 0 {
		t.Fatalf("invalid invalid-invocation vector header: %#v", invalid)
	}
	for _, test := range invalid.Cases {
		test := test
		t.Run("invalid/"+test.Name, func(t *testing.T) {
			err := validateInvocationVector(test.CanonicalJSON, test.Phase)
			if err == nil || !strings.Contains(
				strings.ToLower(err.Error()),
				strings.ToLower(test.ErrorContains),
			) {
				t.Fatalf("error=%v, expected substring %q", err, test.ErrorContains)
			}
		})
	}
}

func TestVendoredExitBehaviorConformance(t *testing.T) {
	t.Parallel()

	var vectors struct {
		SchemaVersion        string `json:"schema_version"`
		ResultStatusExitCode []struct {
			Status   Status `json:"status"`
			ExitCode int    `json:"exit_code"`
		} `json:"result_status_exit_codes"`
		ErrorKindExitCode []struct {
			Kind     ErrorKind `json:"kind"`
			ExitCode int       `json:"exit_code"`
		} `json:"error_kind_exit_codes"`
		ProcessFailureKind []struct {
			ProcessExitCode int       `json:"process_exit_code"`
			Kind            ErrorKind `json:"kind"`
		} `json:"process_failure_kind"`
	}
	readConformance(t, "exit-behavior.json", &vectors)
	if vectors.SchemaVersion != "1" || len(vectors.ResultStatusExitCode) == 0 ||
		len(vectors.ErrorKindExitCode) == 0 || len(vectors.ProcessFailureKind) == 0 {
		t.Fatalf("invalid exit-behavior vector header: %#v", vectors)
	}
	for _, test := range vectors.ResultStatusExitCode {
		if got := resultExitCode(test.Status, ""); got != test.ExitCode {
			t.Errorf("status %q exit=%d want=%d", test.Status, got, test.ExitCode)
		}
	}
	for _, test := range vectors.ErrorKindExitCode {
		if got := errorKindExitCode(test.Kind); got != test.ExitCode {
			t.Errorf("error kind %q exit=%d want=%d", test.Kind, got, test.ExitCode)
		}
	}
	for _, test := range vectors.ProcessFailureKind {
		if got := processFailureKind(test.ProcessExitCode); got != test.Kind {
			t.Errorf("process exit %d kind=%q want=%q", test.ProcessExitCode, got, test.Kind)
		}
	}
}

func semanticManifest() Manifest {
	return Manifest{
		ProtocolVersion: ProtocolVersion,
		Name:            "example-plugin",
		Version:         "1.0.0",
		Commands: []Command{{
			Path: []string{"example", "status"}, Use: "status", Short: "Show status",
			Category: CategoryDiagnostic, Arguments: []Argument{}, Flags: []Flag{},
		}},
	}
}

func assertConformanceValidity(t *testing.T, err error, valid bool, contains string) {
	t.Helper()
	if valid && err != nil {
		t.Fatalf("valid vector rejected: %v", err)
	}
	if !valid && (err == nil || !strings.Contains(
		strings.ToLower(err.Error()),
		strings.ToLower(contains),
	)) {
		t.Fatalf("error=%v, expected substring %q", err, contains)
	}
}

func validateInvocationVector(encoded, phase string) error {
	invocation, err := decodeInvocation([]byte(encoded))
	if err != nil {
		return err
	}
	_, cancel, err := invocationContext(invocation)
	if cancel != nil {
		cancel()
	}
	if err != nil {
		return err
	}
	command := Command{
		Path: []string{"example", "apply"}, Use: "apply <item>", Short: "Apply item",
		Category:  CategoryOperational,
		Arguments: []Argument{{Name: "item", Required: true}},
		Flags: []Flag{
			{Name: "mode", Type: "string"},
			{Name: "enabled", Type: "bool"},
			{Name: "count", Type: "int"},
			{Name: "timeout", Type: "duration"},
		},
		SupportsDryRun: true,
	}
	switch phase {
	case "plan":
		return validateInvocation(command, invocation, false)
	case "execute":
		return validateInvocation(command, invocation, true)
	default:
		return errors.New("unknown invocation conformance phase")
	}
}

func readConformance(t *testing.T, name string, target any) {
	t.Helper()
	path := filepath.Join("..", "..", "contracts", "protocol-v1", "conformance", name)
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		t.Fatal(err)
	}
}
