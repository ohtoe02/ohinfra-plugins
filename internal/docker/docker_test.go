package docker

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestManifestRegistersCompleteReadOnlyBaseline(t *testing.T) {
	definition := NewDefinition(Options{Version: "1.0.0"})
	want := [][]string{
		{"docker", "status"}, {"docker", "info"}, {"docker", "ps"}, {"docker", "usage"},
		{"docker", "logs"}, {"docker", "inspect"}, {"docker", "health"}, {"docker", "registry-test"},
		{"compose", "find"}, {"compose", "validate"}, {"compose", "inspect"}, {"compose", "diff"},
	}
	if definition.Manifest.Name != "docker-base" || definition.Manifest.Description == "" {
		t.Fatalf("manifest = %#v", definition.Manifest)
	}
	for _, path := range want {
		if !hasCommand(definition.Manifest.Commands, path) {
			t.Fatalf("missing command %v", path)
		}
	}
}

func TestDockerCommandsUseLiteralArgvAndCanonicalResults(t *testing.T) {
	var calls []execx.Spec
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		calls = append(calls, spec)
		switch spec.Arguments[0] {
		case "info":
			return execx.Output{Stdout: []byte(`{"ServerVersion":"28.0","Containers":3}`)}, nil
		case "ps":
			return execx.Output{Stdout: []byte("{\"ID\":\"abc\",\"Names\":\"web\"}\n")}, nil
		case "logs":
			return execx.Output{Stdout: []byte("password=hunter2\nnormal\n")}, nil
		case "inspect":
			return execx.Output{Stdout: []byte(`[{"Id":"abc"}]`)}, nil
		}
		return execx.Output{Stdout: []byte(`{"ok":true}`)}, nil
	})
	definition := NewDefinition(Options{
		Version: "1.0.0", Runner: runner,
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"), Host: "server01",
	})
	cases := []protocol.Invocation{
		invoke([]string{"docker", "info"}, nil, nil),
		invoke([]string{"docker", "ps"}, nil, nil),
		invoke([]string{"docker", "logs"}, []string{"web"}, map[string]any{"since": "1h0m0s", "lines": 20}),
		invoke([]string{"docker", "inspect"}, []string{"abc"}, nil),
	}
	var results []protocol.Result
	for _, invocation := range cases {
		got, err := definition.Execute(context.Background(), invocation)
		if err != nil {
			t.Fatalf("%v: %v", invocation.CommandPath, err)
		}
		if got.SchemaVersion != protocol.SchemaVersion || got.Tool.Name != "docker-base" {
			t.Fatalf("result = %#v", got)
		}
		results = append(results, got)
	}
	if strings.Contains(strings.Join(results[2].Data["lines"].([]string), " "), "hunter2") {
		t.Fatalf("logs leaked a secret: %#v", results[2].Data)
	}
	logCall := calls[2]
	if !slices.Equal(logCall.Arguments, []string{"logs", "--since", "1h0m0s", "--tail", "20", "--", "web"}) {
		t.Fatalf("docker logs argv = %#v", logCall.Arguments)
	}
}

func TestDockerDependencyAndSocketPermissionHaveCanonicalExitKinds(t *testing.T) {
	for _, fixture := range []struct {
		output execx.Output
		err    error
		kind   protocol.ErrorKind
	}{
		{err: execx.ErrNotFound, kind: protocol.ErrorDependency},
		{output: execx.Output{ExitCode: 1, Stderr: []byte("permission denied while connecting to the Docker daemon socket")}, kind: protocol.ErrorPrivilege},
	} {
		definition := NewDefinition(Options{
			ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
			Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
				return fixture.output, fixture.err
			}),
		})
		got, err := definition.Execute(context.Background(), invoke([]string{"docker", "info"}, nil, nil))
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Errors) != 1 || got.Errors[0].Kind != fixture.kind {
			t.Fatalf("result = %#v", got)
		}
	}
}

func TestComposeFindIsBoundedAndDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "docker-compose.yml")
	if err := os.WriteFile(outside, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Symlink(outside, filepath.Join(root, "linked.yaml"))
	definition := NewDefinition(Options{
		ConfigPath: filepath.Join(t.TempDir(), "missing.yaml"),
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{}, nil
		}),
	})
	got, err := definition.Execute(context.Background(),
		invoke([]string{"compose", "find"}, []string{root}, nil))
	if err != nil {
		t.Fatal(err)
	}
	files := got.Data["files"].([]string)
	if len(files) != 1 || filepath.Base(files[0]) != "compose.yaml" {
		t.Fatalf("files = %#v", files)
	}
}

func TestObjectIdentifiersRejectOptionSmuggling(t *testing.T) {
	definition := NewDefinition(Options{ConfigPath: filepath.Join(t.TempDir(), "missing.yaml")})
	for _, value := range []string{"-all", "web;reboot", "../web", "web\nname"} {
		if _, err := definition.Execute(context.Background(),
			invoke([]string{"docker", "inspect"}, []string{value}, nil)); err == nil {
			t.Fatalf("accepted %q", value)
		}
	}
}

func hasCommand(commands []protocol.Command, path []string) bool {
	for _, command := range commands {
		if slices.Equal(command.Path, path) {
			return true
		}
	}
	return false
}

func invoke(path, arguments []string, options map[string]any) protocol.Invocation {
	if arguments == nil {
		arguments = []string{}
	}
	if options == nil {
		options = map[string]any{}
	}
	return protocol.Invocation{ProtocolVersion: 1, CommandPath: path, Arguments: arguments, Options: options}
}
