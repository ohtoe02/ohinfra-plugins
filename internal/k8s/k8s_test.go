package k8s

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

var fixedNow = time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)

func TestDefinitionExposesOnlyApprovedReadOnlyCommands(t *testing.T) {
	definition := NewDefinition(Options{Version: "1.0.0"})
	got := make([][]string, 0, len(definition.Manifest.Commands))
	for _, command := range definition.Manifest.Commands {
		got = append(got, command.Path)
		if command.Category != protocol.CategoryDiagnostic ||
			command.RequiresRoot || command.RequiresForce ||
			command.SupportsDryRun || command.RequiresConfirmation {
			t.Fatalf("command is not read-only: %#v", command)
		}
		if len(command.Arguments) != 0 || len(command.Flags) != 0 {
			t.Fatalf("v1 command accepts external input: %#v", command)
		}
	}
	want := [][]string{
		{"k8s", "status"},
		{"k8s", "contexts"},
		{"k8s", "manifests"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
	if err := protocol.ValidateManifest(definition.Manifest); err != nil {
		t.Fatalf("invalid manifest: %v", err)
	}
	if definition.Plan == nil || definition.Execute == nil {
		t.Fatal("protocol definition is incomplete")
	}
}

func TestDefinitionRejectsUnknownArgumentsOptionsAndCommands(t *testing.T) {
	definition := testDefinition(t.TempDir(), nil)
	cases := []protocol.Invocation{
		invoke([]string{"k8s", "unknown"}),
		invoke([]string{"k8s", "status"}),
		invoke([]string{"k8s", "contexts"}),
		invoke([]string{"k8s", "manifests"}),
	}
	cases[1].Arguments = []string{"extra"}
	cases[2].Options = map[string]any{"path": "/tmp/kubeconfig"}
	cases[3].Options = map[string]any{"recursive": true}

	for _, invocation := range cases {
		if _, err := definition.Plan(context.Background(), invocation); exitCode(err) != protocol.ExitArguments {
			t.Fatalf("plan %v: error = %v", invocation.CommandPath, err)
		}
		if _, err := definition.Execute(context.Background(), invocation); exitCode(err) != protocol.ExitArguments {
			t.Fatalf("execute %v: error = %v", invocation.CommandPath, err)
		}
	}
}

func TestReadOnlyPlansHaveNoChangesOrMutationRequirements(t *testing.T) {
	definition := testDefinition(t.TempDir(), nil)
	for _, path := range [][]string{
		{"k8s", "status"},
		{"k8s", "contexts"},
		{"k8s", "manifests"},
	} {
		plan, err := definition.Plan(context.Background(), invoke(path))
		if err != nil {
			t.Fatalf("%v: %v", path, err)
		}
		if len(plan.Changes) != 0 || plan.RequiresRoot || plan.RequiresForce ||
			plan.RequiresConfirmation {
			t.Fatalf("%v returned mutating plan: %#v", path, plan)
		}
	}
}

func TestStatusUsesOnlyFixedLocalCommandsAndProcessMetadata(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/101/comm", "kubelet\n")
	var calls []execx.Spec
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		calls = append(calls, spec)
		switch spec.Program {
		case "kubectl":
			return execx.Output{Stdout: []byte(
				`{"clientVersion":{"gitVersion":"v1.32.1","gitCommit":"abcdef","platform":"linux/amd64"}}`,
			)}, nil
		case "systemctl":
			return execx.Output{Stdout: []byte(
				"LoadState=loaded\nActiveState=active\nSubState=running\n",
			)}, nil
		default:
			t.Fatalf("unexpected executable: %#v", spec)
			return execx.Output{}, nil
		}
	})
	definition := testDefinition(root, runner)

	got, err := definition.Execute(context.Background(), invoke([]string{"k8s", "status"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.StatusPass {
		t.Fatalf("status = %s, result = %#v", got.Status, got)
	}
	client, ok := got.Data["client"].(ClientVersion)
	if !ok || client.Version != "v1.32.1" || client.Platform != "linux/amd64" {
		t.Fatalf("client = %#v", got.Data["client"])
	}
	kubelet, ok := got.Data["kubelet"].(KubeletState)
	if !ok || kubelet.LoadState != "loaded" || kubelet.ActiveState != "active" ||
		kubelet.SubState != "running" || !kubelet.ProcessRunning ||
		!reflect.DeepEqual(kubelet.ProcessIDs, []int{101}) {
		t.Fatalf("kubelet = %#v", got.Data["kubelet"])
	}
	want := []execx.Spec{
		{
			Program: "kubectl", Arguments: []string{"version", "--client", "--output=json"},
			StdoutLimit: statusOutputLimit, StderrLimit: commandErrorLimit,
		},
		{
			Program: "systemctl",
			Arguments: []string{
				"show", "--no-pager", "--property=LoadState,ActiveState,SubState",
				"--", "kubelet.service",
			},
			StdoutLimit: statusOutputLimit, StderrLimit: commandErrorLimit,
		},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
	for _, call := range calls {
		if len(call.Environment) != 0 {
			t.Fatalf("command inherited environment overrides: %#v", call.Environment)
		}
	}
}

func TestStatusReturnsPartialWhenOptionalToolsAreMissing(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proc"), 0o700); err != nil {
		t.Fatal(err)
	}
	definition := testDefinition(root, execx.RunnerFunc(
		func(_ context.Context, _ execx.Spec) (execx.Output, error) {
			return execx.Output{}, execx.ErrNotFound
		},
	))

	got, err := definition.Execute(context.Background(), invoke([]string{"k8s", "status"}))
	if err != nil {
		t.Fatalf("optional dependencies became fatal: %v", err)
	}
	if got.Status != protocol.StatusPartial {
		t.Fatalf("status = %s, result = %#v", got.Status, got)
	}
	if len(got.Errors) != 2 ||
		got.Errors[0].Kind != protocol.ErrorDependency ||
		got.Errors[1].Kind != protocol.ErrorDependency {
		t.Fatalf("errors = %#v", got.Errors)
	}
	text := strings.ToLower(render(got))
	if strings.Contains(text, "path=") || strings.Contains(text, "stderr") {
		t.Fatalf("result leaked external diagnostics: %#v", got)
	}
}

func TestGuardRejectsEveryNonAllowlistedKubectlArgv(t *testing.T) {
	called := false
	runner := guardedRunner{runner: execx.RunnerFunc(
		func(context.Context, execx.Spec) (execx.Output, error) {
			called = true
			return execx.Output{}, nil
		},
	)}
	for _, arguments := range [][]string{
		{"get", "pods"},
		{"list", "pods"},
		{"auth", "can-i", "get", "pods"},
		{"version", "--client", "--output=json", "--server=https://cluster.invalid"},
		{"version", "--server", "https://cluster.invalid"},
		{"cluster-info"},
		{"api-resources"},
		{"proxy"},
		{"port-forward", "pod/name", "8080"},
		{"exec", "pod/name", "--", "id"},
		{"config", "view", "--raw"},
	} {
		called = false
		_, err := runner.Run(context.Background(), execx.Spec{
			Program: "kubectl", Arguments: arguments,
		})
		if err == nil {
			t.Fatalf("accepted cluster-contact argv: %q", arguments)
		}
		if called {
			t.Fatalf("delegated rejected argv: %q", arguments)
		}
	}
}

func TestGuardRejectsKubeconfigEnvironmentAndStdin(t *testing.T) {
	called := false
	runner := guardedRunner{runner: execx.RunnerFunc(
		func(context.Context, execx.Spec) (execx.Output, error) {
			called = true
			return execx.Output{}, nil
		},
	)}
	for _, spec := range []execx.Spec{
		{
			Program: "kubectl", Arguments: []string{"version", "--client", "--output=json"},
			Environment: map[string]string{"KUBECONFIG": "/tmp/remote.conf"},
		},
		{
			Program: "kubectl", Arguments: []string{"version", "--client", "--output=json"},
			Environment: map[string]string{"HTTPS_PROXY": "https://proxy.invalid"},
		},
		{
			Program: "kubectl", Arguments: []string{"version", "--client", "--output=json"},
			Stdin: []byte("server: https://cluster.invalid"),
		},
	} {
		called = false
		if _, err := runner.Run(context.Background(), spec); err == nil {
			t.Fatalf("accepted command input override: %#v", spec)
		}
		if called {
			t.Fatalf("delegated rejected command: %#v", spec)
		}
	}
}

func TestStatusRejectsTrailingKubectlJSONWithoutLeakingIt(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proc"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "kubectl":
			return execx.Output{Stdout: []byte(
				`{"clientVersion":{"gitVersion":"v1.32.1","platform":"linux/amd64"}}` +
					` {"token":"trailing-json-secret"}`,
			)}, nil
		case "systemctl":
			return execx.Output{Stdout: []byte(
				"LoadState=loaded\nActiveState=active\nSubState=running\n",
			)}, nil
		default:
			t.Fatalf("unexpected executable: %s", spec.Program)
			return execx.Output{}, nil
		}
	})
	definition := testDefinition(root, runner)

	got, err := definition.Execute(context.Background(), invoke([]string{"k8s", "status"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.StatusPartial {
		t.Fatalf("status = %s, result = %#v", got.Status, got)
	}
	if strings.Contains(strings.ToLower(render(got)), "trailing-json-secret") {
		t.Fatalf("result leaked rejected kubectl output: %#v", got)
	}
}

func TestStatusRejectsUntrustedClientVersionText(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "proc"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "kubectl":
			return execx.Output{Stdout: []byte(
				`{"clientVersion":{"gitVersion":"raw-external-output-hunter2","platform":"linux/amd64"}}`,
			)}, nil
		case "systemctl":
			return execx.Output{Stdout: []byte(
				"LoadState=loaded\nActiveState=active\nSubState=running\n",
			)}, nil
		default:
			t.Fatalf("unexpected executable: %s", spec.Program)
			return execx.Output{}, nil
		}
	})
	definition := testDefinition(root, runner)

	got, err := definition.Execute(context.Background(), invoke([]string{"k8s", "status"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.StatusPartial {
		t.Fatalf("status = %s, result = %#v", got.Status, got)
	}
	if strings.Contains(strings.ToLower(render(got)), "raw-external-output-hunter2") {
		t.Fatalf("result leaked rejected kubectl output: %#v", got)
	}
}

func TestStatusRejectsIncompleteKubeletUnitMetadata(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/202/comm", "kubelet\n")
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "kubectl":
			return execx.Output{Stdout: []byte(
				`{"clientVersion":{"gitVersion":"v1.32.1","platform":"linux/amd64"}}`,
			)}, nil
		case "systemctl":
			return execx.Output{Stdout: []byte(
				"LoadState=loaded\nActiveState=active\n",
			)}, nil
		default:
			t.Fatalf("unexpected executable: %s", spec.Program)
			return execx.Output{}, nil
		}
	})
	definition := testDefinition(root, runner)

	got, err := definition.Execute(context.Background(), invoke([]string{"k8s", "status"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.StatusPartial {
		t.Fatalf("status = %s, result = %#v", got.Status, got)
	}
	kubelet := got.Data["kubelet"].(KubeletState)
	if !kubelet.ProcessRunning || !reflect.DeepEqual(kubelet.ProcessIDs, []int{202}) {
		t.Fatalf("usable process metadata was discarded: %#v", kubelet)
	}
}

func TestStatusPropagatesCancellationBetweenLocalProbes(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "proc/303/comm", "kubelet\n")
	ctx, cancel := context.WithCancel(context.Background())
	runner := execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
		switch spec.Program {
		case "kubectl":
			return execx.Output{Stdout: []byte(
				`{"clientVersion":{"gitVersion":"v1.32.1","platform":"linux/amd64"}}`,
			)}, nil
		case "systemctl":
			cancel()
			return execx.Output{Stdout: []byte(
				"LoadState=loaded\nActiveState=active\nSubState=running\n",
			)}, nil
		default:
			t.Fatalf("unexpected executable: %s", spec.Program)
			return execx.Output{}, nil
		}
	})
	definition := testDefinition(root, runner)

	got, err := definition.Execute(ctx, invoke([]string{"k8s", "status"}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(got, protocol.Result{}) {
		t.Fatalf("fatal error returned result: %#v", got)
	}
}

func TestContextsReturnOnlySafeKubeconfigMetadata(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "etc/kubernetes/admin.conf", `
apiVersion: v1
kind: Config
current-context: prod
clusters:
  - name: prod-cluster
    cluster:
      server: https://cluster.invalid
      certificate-authority-data: cert-secret-123
users:
  - name: admin
    user:
      token: token-secret-456
      client-certificate-data: client-cert-secret-789
      client-key-data: client-key-secret-012
      exec:
        command: credential-helper
        args: [--token, exec-args-secret-345]
      auth-provider:
        name: oidc
        config:
          access-token: provider-secret-678
contexts:
  - name: prod
    context:
      cluster: prod-cluster
      user: admin
      namespace: workloads
`)
	definition := testDefinition(root, execx.RunnerFunc(
		func(context.Context, execx.Spec) (execx.Output, error) {
			t.Fatal("contexts must not execute external commands")
			return execx.Output{}, nil
		},
	))

	got, err := definition.Execute(context.Background(), invoke([]string{"k8s", "contexts"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.StatusPass {
		t.Fatalf("status = %s, result = %#v", got.Status, got)
	}
	configs, ok := got.Data["kubeconfigs"].([]KubeconfigMetadata)
	if !ok || len(configs) != 1 {
		t.Fatalf("kubeconfigs = %#v", got.Data["kubeconfigs"])
	}
	config := configs[0]
	if config.Path != "etc/kubernetes/admin.conf" ||
		config.CurrentContext != "prod" ||
		config.ClusterCount != 1 || config.UserCount != 1 ||
		!reflect.DeepEqual(config.Contexts, []ContextMetadata{{
			Name: "prod", Namespace: "workloads",
		}}) {
		t.Fatalf("metadata = %#v", config)
	}
	text := strings.ToLower(render(got))
	for _, forbidden := range []string{
		"cluster.invalid", "cert-secret-123", "token-secret-456",
		"client-cert-secret-789", "client-key-secret-012",
		"credential-helper", "exec-args-secret-345", "provider-secret-678",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("result leaked %q: %#v", forbidden, got)
		}
	}
}

func TestContextsRejectOversizedAndSymlinkedKubeconfigs(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "etc/kubernetes/admin.conf",
			strings.Repeat("x", int(kubeconfigLimit)+1))
		definition := testDefinition(root, nil)
		_, err := definition.Execute(context.Background(), invoke([]string{"k8s", "contexts"}))
		if exitCode(err) != protocol.ExitConfiguration {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "safe/admin.conf", "apiVersion: v1\nkind: Config\n")
		link := filepath.Join(root, "etc", "kubernetes", "admin.conf")
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "safe", "admin.conf"), link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		definition := testDefinition(root, nil)
		_, err := definition.Execute(context.Background(), invoke([]string{"k8s", "contexts"}))
		if exitCode(err) != protocol.ExitConfiguration {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestManifestsReturnOnlyBoundedStaticMetadata(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, "etc/kubernetes/manifests/control-plane.yaml", `
apiVersion: v1
kind: Pod
metadata:
  name: kube-apiserver
  namespace: kube-system
spec:
  containers:
    - name: kube-apiserver
      image: registry.invalid/control-plane:secret-image-tag
      env:
        - name: TOKEN
          value: manifest-secret-123
---
apiVersion: v1
kind: Secret
metadata:
  name: bootstrap
  namespace: kube-system
data:
  token: manifest-secret-456
`)
	definition := testDefinition(root, execx.RunnerFunc(
		func(context.Context, execx.Spec) (execx.Output, error) {
			t.Fatal("manifest inspection must not execute external commands")
			return execx.Output{}, nil
		},
	))

	got, err := definition.Execute(context.Background(), invoke([]string{"k8s", "manifests"}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != protocol.StatusPass {
		t.Fatalf("status = %s, result = %#v", got.Status, got)
	}
	manifests, ok := got.Data["manifests"].([]ManifestMetadata)
	if !ok || !reflect.DeepEqual(manifests, []ManifestMetadata{
		{
			Path: "etc/kubernetes/manifests/control-plane.yaml", Document: 1,
			APIVersion: "v1", Kind: "Pod", Name: "kube-apiserver",
			Namespace: "kube-system",
		},
		{
			Path: "etc/kubernetes/manifests/control-plane.yaml", Document: 2,
			APIVersion: "v1", Kind: "Secret", Name: "bootstrap",
			Namespace: "kube-system",
		},
	}) {
		t.Fatalf("manifests = %#v", got.Data["manifests"])
	}
	text := strings.ToLower(render(got))
	for _, forbidden := range []string{
		"secret-image-tag", "manifest-secret-123", "manifest-secret-456",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("result leaked %q: %#v", forbidden, got)
		}
	}
}

func TestManifestsRejectOversizedAndSymlinkedFiles(t *testing.T) {
	t.Run("oversized", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "etc/kubernetes/manifests/large.yaml",
			strings.Repeat("x", int(manifestFileLimit)+1))
		definition := testDefinition(root, nil)
		_, err := definition.Execute(context.Background(), invoke([]string{"k8s", "manifests"}))
		if exitCode(err) != protocol.ExitConfiguration {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		writeFixture(t, root, "safe/pod.yaml",
			"apiVersion: v1\nkind: Pod\nmetadata:\n  name: safe\n")
		link := filepath.Join(root, "etc", "kubernetes", "manifests", "pod.yaml")
		if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "safe", "pod.yaml"), link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		definition := testDefinition(root, nil)
		_, err := definition.Execute(context.Background(), invoke([]string{"k8s", "manifests"}))
		if exitCode(err) != protocol.ExitConfiguration {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestCommandsPropagateCancellationAndDeadline(t *testing.T) {
	cases := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		err  error
	}{
		{
			name: "cancelled",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			err: context.Canceled,
		},
		{
			name: "deadline",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			err: context.DeadlineExceeded,
		},
	}
	for _, testCase := range cases {
		for _, path := range [][]string{
			{"k8s", "status"},
			{"k8s", "contexts"},
			{"k8s", "manifests"},
		} {
			t.Run(strings.Join(path, "-")+"/"+testCase.name, func(t *testing.T) {
				ctx, cancel := testCase.ctx()
				defer cancel()
				definition := testDefinition(t.TempDir(), nil)
				got, err := definition.Execute(ctx, invoke(path))
				if !errors.Is(err, testCase.err) {
					t.Fatalf("error = %v", err)
				}
				if !reflect.DeepEqual(got, protocol.Result{}) {
					t.Fatalf("fatal error returned result: %#v", got)
				}
			})
		}
	}
}

func TestFileCommandsPropagateCancellationDuringCollection(t *testing.T) {
	cases := []struct {
		name    string
		path    []string
		fixture string
		content string
	}{
		{
			name: "contexts", path: []string{"k8s", "contexts"},
			fixture: "etc/kubernetes/admin.conf",
			content: "apiVersion: v1\nkind: Config\ncontexts: []\n",
		},
		{
			name: "manifests", path: []string{"k8s", "manifests"},
			fixture: "etc/kubernetes/manifests/pod.yaml",
			content: "apiVersion: v1\nkind: Pod\nmetadata:\n  name: local\n",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, testCase.fixture, testCase.content)
			ctx := &cancellingContext{cancelAfter: 4}
			definition := testDefinition(root, nil)
			got, err := definition.Execute(ctx, invoke(testCase.path))
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
			if exitCode(err) == protocol.ExitConfiguration {
				t.Fatalf("cancellation was remapped to configuration: %v", err)
			}
			if !reflect.DeepEqual(got, protocol.Result{}) {
				t.Fatalf("fatal error returned result: %#v", got)
			}
		})
	}
}

type cancellingContext struct {
	calls       int
	cancelAfter int
}

func (ctx *cancellingContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *cancellingContext) Done() <-chan struct{}       { return nil }
func (ctx *cancellingContext) Value(any) any               { return nil }
func (ctx *cancellingContext) Err() error {
	ctx.calls++
	if ctx.calls > ctx.cancelAfter {
		return context.Canceled
	}
	return nil
}

func testDefinition(root string, runner execx.Runner) protocol.Definition {
	return NewDefinition(Options{
		Version: "1.0.0", Commit: "abc", BuildDate: "2026-07-27",
		Host: "server01", Root: root, Runner: runner,
		Now: func() time.Time { return fixedNow },
	})
}

func invoke(path []string) protocol.Invocation {
	return protocol.Invocation{
		ProtocolVersion: protocol.ProtocolVersion,
		CommandPath:     path,
		Arguments:       []string{},
		Options:         map[string]any{},
	}
}

func exitCode(err error) int {
	var failure protocol.ExitError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return 0
}

func writeFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func render(value any) string {
	return fmt.Sprintf("%#v", value)
}

func unusedImportsForLaterSlices() { _ = strings.Builder{} }
