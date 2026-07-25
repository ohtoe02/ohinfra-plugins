package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestValidateDescriptionAcceptsAbsentOrSingleLineText(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "Docker and Compose diagnostics"} {
		if err := ValidateDescription(value); err != nil {
			t.Fatalf("ValidateDescription(%q) = %v", value, err)
		}
	}
}

func TestValidateDescriptionRejectsUnsafeOrOversizedText(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		" leading whitespace",
		"trailing whitespace ",
		"two\nlines",
		"control\x00character",
		strings.Repeat("x", 513),
		string([]byte{0xff}),
	} {
		if err := ValidateDescription(value); err == nil {
			t.Fatalf("ValidateDescription(%q) succeeded", value)
		}
	}
}

func TestValidateManifestEnforcesIdentityCommandsAndDescription(t *testing.T) {
	t.Parallel()

	valid := Manifest{
		ProtocolVersion: 1,
		Name:            "system-base",
		Version:         "1.0.0",
		Description:     "System diagnostics",
		Commands: []Command{{
			Path:      []string{"system", "info"},
			Use:       "info",
			Short:     "Show system information",
			Category:  CategoryDiagnostic,
			Arguments: []Argument{},
			Flags:     []Flag{},
		}},
	}
	if err := ValidateManifest(valid); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{name: "name", mutate: func(value *Manifest) { value.Name = "../bad" }},
		{name: "version", mutate: func(value *Manifest) { value.Version = "" }},
		{name: "description", mutate: func(value *Manifest) { value.Description = "bad\ntext" }},
		{name: "commands", mutate: func(value *Manifest) { value.Commands = nil }},
		{name: "path", mutate: func(value *Manifest) { value.Commands[0].Path = []string{"info"} }},
		{name: "category", mutate: func(value *Manifest) { value.Commands[0].Category = "unsafe" }},
		{name: "dry-run", mutate: func(value *Manifest) {
			value.Commands[0].Category = CategoryOperational
			value.Commands[0].SupportsDryRun = false
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			value := valid
			value.Commands = append([]Command(nil), valid.Commands...)
			test.mutate(&value)
			if err := ValidateManifest(value); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestServeManifestDoesNotCallInvocationHandlers(t *testing.T) {
	t.Parallel()

	called := false
	definition := Definition{
		Manifest: Manifest{
			ProtocolVersion: 1,
			Name:            "system-base",
			Version:         "1.0.0",
			Commands: []Command{{
				Path: []string{"system", "info"}, Use: "info", Short: "info",
				Category: CategoryDiagnostic, Arguments: []Argument{}, Flags: []Flag{},
			}},
		},
		Execute: func(context.Context, Invocation) (Result, error) {
			called = true
			return Result{}, nil
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exit := Serve(
		definition,
		[]string{"system-base", "manifest", "--protocol=1"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)

	if exit != 0 || called || stderr.Len() != 0 {
		t.Fatalf("exit=%d called=%v stderr=%q", exit, called, stderr.String())
	}
	var manifest Manifest
	if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != "system-base" {
		t.Fatalf("manifest name = %q", manifest.Name)
	}
}

func TestServeExecuteStrictlyDecodesInvocationAndNormalizesResult(t *testing.T) {
	t.Parallel()

	definition := Definition{
		Manifest: Manifest{
			ProtocolVersion: 1,
			Name:            "system-base",
			Version:         "1.0.0",
			Commands: []Command{{
				Path: []string{"system", "info"}, Use: "info", Short: "info",
				Category: CategoryDiagnostic, Arguments: []Argument{}, Flags: []Flag{},
			}},
		},
		Execute: func(_ context.Context, invocation Invocation) (Result, error) {
			if invocation.RequestID != "request-1" {
				t.Fatalf("request id = %q", invocation.RequestID)
			}
			return Result{
				Command:   "system info",
				Status:    StatusPass,
				Timestamp: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
				Tool:      Tool{Name: "system-base", Version: "1.0.0"},
			}, nil
		},
	}
	invocation := `{
		"protocol_version":1,
		"request_id":"request-1",
		"command_path":["system","info"],
		"arguments":[],
		"options":{}
	}`
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exit := Serve(
		definition,
		[]string{"system-base", "execute", "--protocol=1"},
		strings.NewReader(invocation),
		&stdout,
		&stderr,
	)

	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	var result Result
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != "1.0" || result.Checks == nil || result.Data == nil ||
		result.Changes == nil || result.Errors == nil {
		t.Fatalf("non-canonical result: %#v", result)
	}

	for _, malformed := range []string{
		`{"protocol_version":1,"unknown":true}`,
		`{"protocol_version":1,"protocol_version":1}`,
		`{"protocol_version":1} {}`,
		`{"protocol_version":1,"request_id":"request-1","command_path":["system","info"],"arguments":null,"options":{}}`,
		`{"protocol_version":1,"request_id":"request-1","command_path":["system","info"],"options":{}}`,
		`{"protocol_version":1,"request_id":"request-1","command_path":["system","info"],"arguments":[],"options":null}`,
		`{"protocol_version":1,"request_id":"request-1","command_path":["system","info"],"arguments":[]}`,
		`{"protocol_version":1,"request_id":"request-1","command_path":["system","info"],"arguments":[],"options":{}}` +
			strings.Repeat(" ", 1<<20),
	} {
		stdout.Reset()
		stderr.Reset()
		exit = Serve(
			definition,
			[]string{"system-base", "execute", "--protocol=1"},
			strings.NewReader(malformed),
			&stdout,
			&stderr,
		)
		if exit != ExitArguments || stderr.Len() == 0 {
			t.Fatalf("strict decode len=%d exit=%d stderr=%q", len(malformed), exit, stderr.String())
		}
	}
}

func TestServeRejectsRawInvalidUTF8InvocationBeforeHandler(t *testing.T) {
	t.Parallel()

	called := false
	definition := Definition{
		Manifest: Manifest{
			ProtocolVersion: 1,
			Name:            "system-base",
			Version:         "1.0.0",
			Commands: []Command{{
				Path: []string{"system", "info"}, Use: "info", Short: "info",
				Category: CategoryDiagnostic, Arguments: []Argument{}, Flags: []Flag{},
			}},
		},
		Execute: func(context.Context, Invocation) (Result, error) {
			called = true
			return Result{Status: StatusPass}, nil
		},
	}
	encoded := []byte(
		`{"protocol_version":1,"request_id":"request-1","command_path":["system","info"],` +
			`"arguments":["value` + string([]byte{0xff}) + `"],"options":{}}`,
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exit := Serve(
		definition,
		[]string{"system-base", "execute", "--protocol=1"},
		bytes.NewReader(encoded),
		&stdout,
		&stderr,
	)

	if exit != ExitArguments || called {
		t.Fatalf("exit=%d called=%t stdout=%q stderr=%q", exit, called, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "valid UTF-8") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestPlanDigestMatchesCanonicalJSON(t *testing.T) {
	t.Parallel()

	plan := Plan{
		CommandID: "service.restart",
		Summary:   "Restart nginx.service",
		Checks:    []Check{},
		Changes:   []Change{},
		Risks:     []string{"Service interruption"},
	}
	first, err := PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	again, err := PlanDigest(plan)
	if err != nil {
		t.Fatal(err)
	}
	if first != again || len(first) != 64 {
		t.Fatalf("unstable digest %q %q", first, again)
	}
}
