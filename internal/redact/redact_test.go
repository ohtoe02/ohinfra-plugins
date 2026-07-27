package redact

import (
	"strings"
	"testing"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestRecursiveRedactionMasksSecrets(t *testing.T) {
	input := protocol.Result{
		Data: map[string]any{"password": "secret", "safe": "visible"},
		Checks: []protocol.Check{{
			Summary: "Authorization: Bearer token-value",
			Details: map[string]any{"api_token": "nested-secret"},
		}},
		Errors: []protocol.StructuredError{{Message: "pwd=hunter2"}},
	}
	got := Result(input)
	encoded := got.Checks[0].Summary + got.Errors[0].Message + got.Data["password"].(string)
	for _, secret := range []string{"token-value", "hunter2", "secret"} {
		if strings.Contains(encoded, secret) {
			t.Fatalf("redaction leaked %q in %q", secret, encoded)
		}
	}
	if got.Data["safe"] != "visible" {
		t.Fatalf("safe value changed: %#v", got.Data["safe"])
	}
}

func TestValueRedactsTypedNestedContainersWithoutChangingTheirTypes(t *testing.T) {
	input := []map[string]any{{
		"name":  "first",
		"token": "top-secret",
		"metadata": map[string]string{
			"safe":     "visible",
			"password": "nested-secret",
		},
	}}

	got := Value(input).([]map[string]any)
	metadata := got[0]["metadata"].(map[string]string)

	if got[0]["token"] != Mask || metadata["password"] != Mask {
		t.Fatalf("typed secrets were not redacted: %#v", got)
	}
	if got[0]["name"] != "first" || metadata["safe"] != "visible" {
		t.Fatalf("safe values changed: %#v", got)
	}
	if input[0]["token"] != "top-secret" ||
		input[0]["metadata"].(map[string]string)["password"] != "nested-secret" {
		t.Fatalf("Value mutated input: %#v", input)
	}
}

func TestValueRecursivelyRedactsTypedStructsWithoutChangingTheirTypes(t *testing.T) {
	type credentials struct {
		Password string `json:"password"`
	}
	type record struct {
		Name        string
		Endpoint    string
		Credentials *credentials
	}
	input := []record{{
		Name:     "first",
		Endpoint: "https://example.test/path?access_token=endpoint-secret",
		Credentials: &credentials{
			Password: "nested-secret",
		},
	}}

	got := Value(input).([]record)

	if got[0].Name != "first" {
		t.Fatalf("safe field changed: %#v", got)
	}
	if strings.Contains(got[0].Endpoint, "endpoint-secret") ||
		got[0].Credentials.Password != Mask {
		t.Fatalf("typed struct secrets were not redacted: %#v", got)
	}
	if got[0].Credentials == input[0].Credentials {
		t.Fatalf("typed struct pointer aliases input: %#v", got)
	}
	if input[0].Credentials.Password != "nested-secret" {
		t.Fatalf("Value mutated input: %#v", input)
	}
}
