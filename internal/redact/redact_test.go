package redact

import (
	"strings"
	"testing"

	"github.com/ohtoe02/ohinfra-plugins/internal/protocol"
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
