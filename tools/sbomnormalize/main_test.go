package main

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeSPDXMakesTimestampAndNamespaceDeterministic(t *testing.T) {
	first := []byte(`{
		"spdxVersion":"SPDX-2.3",
		"documentNamespace":"https://anchore.example/random-one",
		"creationInfo":{"created":"2026-07-25T01:02:03Z","creators":["Tool: syft"]},
		"packages":[{"name":"sample"}]
	}`)
	second := []byte(`{
		"packages":[{"name":"sample"}],
		"creationInfo":{"creators":["Tool: syft"],"created":"2026-07-26T04:05:06Z"},
		"documentNamespace":"https://anchore.example/random-two",
		"spdxVersion":"SPDX-2.3"
	}`)
	created := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	namespace := "https://github.com/ohtoe02/ohtools-plugins/releases/tag/system-base-v1.1.0/sbom/abc"

	normalizedFirst, err := normalizeSPDX(first, created, namespace)
	if err != nil {
		t.Fatal(err)
	}
	normalizedSecond, err := normalizeSPDX(second, created, namespace)
	if err != nil {
		t.Fatal(err)
	}
	if string(normalizedFirst) != string(normalizedSecond) {
		t.Fatalf("normalization differs:\n%s\n%s", normalizedFirst, normalizedSecond)
	}
	for _, expected := range []string{
		`"created": "2026-07-24T10:00:00Z"`,
		`"documentNamespace": "` + namespace + `"`,
	} {
		if !strings.Contains(string(normalizedFirst), expected) {
			t.Fatalf("normalized SPDX lacks %q:\n%s", expected, normalizedFirst)
		}
	}
}

func TestNormalizeSPDXRejectsMalformedOrIncompleteDocuments(t *testing.T) {
	created := time.Date(2026, 7, 24, 10, 0, 0, 0, time.UTC)
	for _, input := range []string{
		`{}`,
		`{"spdxVersion":"SPDX-2.3","creationInfo":null}`,
		`{"spdxVersion":"SPDX-2.3","creationInfo":{},"unknown":1} trailing`,
	} {
		if _, err := normalizeSPDX(
			[]byte(input),
			created,
			"https://example.com/sbom",
		); err == nil {
			t.Fatalf("invalid SPDX accepted: %s", input)
		}
	}
}
