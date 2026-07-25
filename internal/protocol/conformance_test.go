package protocol

import (
	"encoding/json"
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
		SchemaVersion string `json:"schema_version"`
		Algorithm     string `json:"algorithm"`
		Encoding      string `json:"encoding"`
		Cases         []struct {
			Name          string `json:"name"`
			CanonicalJSON string `json:"canonical_json"`
			SHA256        string `json:"sha256"`
		} `json:"cases"`
	}
	readConformance(t, "plan-digest.json", &vectors)
	if vectors.SchemaVersion != "1" || vectors.Algorithm != "sha256" ||
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
