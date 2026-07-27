package apt

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestDefinitionDeclaresOnlyApprovedReadOnlyCommands(t *testing.T) {
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
		{"apt", "status"},
		{"apt", "updates"},
		{"apt", "sources"},
		{"apt", "history"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("command paths = %#v, want %#v", got, want)
	}
	if err := protocol.ValidateManifest(definition.Manifest); err != nil {
		t.Fatalf("manifest is invalid: %v", err)
	}

	plan, err := definition.Plan(context.Background(), invocation("apt", "status"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Changes) != 0 || plan.RequiresRoot || plan.RequiresConfirmation {
		t.Fatalf("read-only plan can mutate: %#v", plan)
	}
}

func TestSourcesRedactCredentialsFromListAndDeb822Files(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "etc/apt/sources.list", `
deb https://alice:source-secret@example.com/debian bookworm main
deb [arch=amd64 signed-by=/usr/share/keyrings/test.gpg] https://mirror.example/debian bookworm-updates main
`)
	writeFixture(t, root, "etc/apt/sources.list.d/vendor.sources", `
Types: deb
URIs: https://bob:deb822-secret@packages.example/repository
Suites: stable
Components: main
`)

	result, err := executeTest(t, Options{Root: root}, invocation("apt", "sources"))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, secret := range []string{"alice", "source-secret", "bob", "deb822-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("source result leaks %q: %s", secret, text)
		}
	}
	for _, safeURL := range []string{
		"https://example.com/debian",
		"https://mirror.example/debian",
		"https://packages.example/repository",
	} {
		if !strings.Contains(text, safeURL) {
			t.Fatalf("source result omitted %q: %s", safeURL, text)
		}
	}
}

func TestUpdatesUsesOnlyFixedLocalSimulationArgv(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy-user:proxy-secret@attacker.invalid")
	t.Setenv("HTTPS_PROXY", "http://proxy-user:proxy-secret@attacker.invalid")
	t.Setenv("APT_CONFIG", filepath.Join(t.TempDir(), "attacker.conf"))

	var got execx.Spec
	options := Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			got = spec
			return execx.Output{
				Stdout: []byte("Inst openssl [3.0.0] (3.0.1 Debian:12/stable [amd64])\n"),
			}, nil
		}),
	}
	result, err := executeTest(t, options, invocation("apt", "updates"))
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := []string{
		"--simulate",
		"--no-download",
		"--option", "Debug::NoLocking=true",
		"upgrade",
	}
	if got.Program != "apt-get" || !reflect.DeepEqual(got.Arguments, wantArgs) {
		t.Fatalf("command spec = %#v, want apt-get %#v", got, wantArgs)
	}
	if len(got.Environment) != 0 {
		t.Fatalf("caller environment propagated to apt simulation: %#v", got.Environment)
	}
	if result.Status != protocol.StatusPass {
		t.Fatalf("status = %s, result = %#v", result.Status, result)
	}
	updates, ok := result.Data["updates"].([]map[string]any)
	if !ok || len(updates) != 1 || updates[0]["package"] != "openssl" {
		t.Fatalf("updates = %#v", result.Data["updates"])
	}
}

func TestUpdatesReportsMissingAptGetAsDependencyExit(t *testing.T) {
	t.Parallel()

	options := Options{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{}, execx.ErrNotFound
		}),
	}
	_, err := executeTest(t, options, invocation("apt", "updates"))
	var failure protocol.ExitError
	if !errors.As(err, &failure) || failure.Code != protocol.ExitDependency {
		t.Fatalf("execute error = %v, want ExitDependency", err)
	}
}

func TestStatusReportsInstalledPackagesAndInterruptedUpdates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "var/lib/dpkg/status", `Package: installed
Status: install ok installed

Package: half-configured
Status: install ok half-configured
`)
	writeFixture(t, root, "var/lib/dpkg/updates/0001", "pending")

	result, err := executeTest(t, Options{Root: root}, invocation("apt", "status"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Data["installed_packages"] != 1 ||
		result.Data["non_installed_records"] != 1 ||
		result.Data["pending_update_records"] != 1 {
		t.Fatalf("status data = %#v", result.Data)
	}
	if result.Status != protocol.StatusWarning {
		t.Fatalf("status = %s, want warning", result.Status)
	}
}

func TestHistoryParsesBoundedLocalTransactions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFixture(t, root, "var/log/apt/history.log", `Start-Date: 2026-07-26  10:00:00
Commandline: apt-get upgrade
Upgrade: openssl:amd64 (3.0.0, 3.0.1)
End-Date: 2026-07-26  10:00:01
`)

	result, err := executeTest(t, Options{Root: root}, invocation("apt", "history"))
	if err != nil {
		t.Fatal(err)
	}
	transactions, ok := result.Data["transactions"].([]Transaction)
	if !ok || len(transactions) != 1 {
		t.Fatalf("transactions = %#v", result.Data["transactions"])
	}
	if transactions[0].StartDate != "2026-07-26 10:00:00" ||
		transactions[0].Actions["upgrade"] != 1 {
		t.Fatalf("transaction = %#v", transactions[0])
	}
}

func TestExecuteRejectsArgumentsOptionsAndUnknownCommands(t *testing.T) {
	t.Parallel()

	options := Options{Root: t.TempDir()}
	tests := []protocol.Invocation{
		{
			ProtocolVersion: protocol.ProtocolVersion,
			CommandPath:     []string{"apt", "status"},
			Arguments:       []string{"extra"},
		},
		{
			ProtocolVersion: protocol.ProtocolVersion,
			CommandPath:     []string{"apt", "updates"},
			Options:         map[string]any{"proxy": "http://secret@example"},
		},
		invocation("apt", "missing"),
	}
	for _, input := range tests {
		_, err := executeTest(t, options, input)
		var failure protocol.ExitError
		if !errors.As(err, &failure) || failure.Code != protocol.ExitArguments {
			t.Fatalf("execute(%#v) error = %v, want ExitArguments", input, err)
		}
	}
}

func executeTest(t *testing.T, options Options, input protocol.Invocation) (protocol.Result, error) {
	t.Helper()
	options.Version = "1.0.0"
	options.Host = "test-host"
	options.Now = func() time.Time {
		return time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	}
	definition := NewDefinition(options)
	return definition.Execute(context.Background(), input)
}

func invocation(path ...string) protocol.Invocation {
	return protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     path,
		Arguments:       []string{},
		Options:         map[string]any{},
	}
}

func writeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
