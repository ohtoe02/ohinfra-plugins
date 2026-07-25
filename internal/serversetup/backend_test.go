package serversetup

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
)

func TestSystemBackendStagesActivatesAndVerifiesOwnedFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	var calls []execx.Spec
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			return execx.Output{}, nil
		}),
	}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemSysctl},
	}
	observation, err := backend.Observe(context.Background(), ItemSysctl, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("missing managed sysctl file was reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemSysctl, profile); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), ItemSysctl, profile); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "kernel.kptr_restrict = 2") {
		t.Fatalf("managed content = %q", content)
	}
	if len(calls) != 1 || calls[0].Program != "sysctl" ||
		!reflect.DeepEqual(calls[0].Arguments, []string{"--system"}) {
		t.Fatalf("runner calls = %#v", calls)
	}
}

func TestSystemBackendRestoresOwnedFileWhenActivationFails(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("# previous\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
			return execx.Output{ExitCode: 1, Stderr: []byte("rejected")}, nil
		}),
	}
	err := backend.Apply(context.Background(), ItemSysctl, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   DefaultConfig(),
		Items:    []Item{ItemPackages, ItemSysctl},
	})
	if err == nil {
		t.Fatal("activation failure was ignored")
	}
	content, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != "# previous\n" {
		t.Fatalf("rollback content = %q", content)
	}
}

func TestSystemBackendRefusesSymlinkedManagedFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "etc", "sysctl.d", "60-ohtools-server-setup.conf")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlink is unavailable: %v", err)
	}
	backend := SystemBackend{Root: root, Runner: successfulRunner()}
	_, err := backend.Observe(context.Background(), ItemSysctl, Profile{
		Platform: Platform{ID: "debian", Version: "12"}, Config: DefaultConfig(),
	})
	if err == nil {
		t.Fatal("symlinked managed file was accepted")
	}
}

func TestSystemBackendUsesDirectPackageArgv(t *testing.T) {
	t.Parallel()

	var calls []execx.Spec
	backend := SystemBackend{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Packages = []string{"curl"}
	profile := Profile{
		Platform: Platform{ID: "ubuntu", Version: "22.04"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers, ItemSSH},
	}
	if err := backend.Apply(context.Background(), ItemPackages, profile); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %#v", calls)
	}
	if calls[0].Program != "apt-get" || !reflect.DeepEqual(calls[0].Arguments, []string{"update"}) {
		t.Fatalf("apt update = %#v", calls[0])
	}
	install := calls[1]
	if install.Program != "apt-get" || !reflect.DeepEqual(install.Arguments,
		[]string{"install", "-y", "--no-install-recommends", "--", "curl", "openssh-server", "sudo"}) {
		t.Fatalf("apt install = %#v", install)
	}
	if len(install.Environment) == 0 || install.Environment["DEBIAN_FRONTEND"] != "noninteractive" {
		t.Fatalf("apt environment = %#v", install.Environment)
	}
}

func TestSystemBackendVerifiesPinnedZabbixRepositoryPackage(t *testing.T) {
	t.Parallel()

	content := []byte("repo-pkg")
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	var calls []execx.Spec
	backend := SystemBackend{
		Root: t.TempDir(),
		HTTPClient: httpDoerFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Scheme != "https" || request.URL.Host != "repo.zabbix.com" {
				t.Fatalf("request URL = %s", request.URL)
			}
			return &http.Response{
				StatusCode:    http.StatusOK,
				ContentLength: int64(len(content)),
				Body:          io.NopCloser(strings.NewReader(string(content))),
				Header:        make(http.Header),
			}, nil
		}),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Zabbix = &ZabbixConfig{
		Enabled: true, Server: "192.0.2.10", Hostname: "web-01",
		RepositoryPackageURL:    "https://repo.zabbix.com/release.deb",
		RepositoryPackageSize:   int64(len(content)),
		RepositoryPackageSHA256: digest,
	}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemZabbix},
	}
	if err := backend.Apply(context.Background(), ItemPackages, profile); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[0].Program != "dpkg" ||
		!reflect.DeepEqual(calls[0].Arguments[:2], []string{"--install", "--"}) ||
		calls[1].Program != "apt-get" || calls[2].Program != "apt-get" {
		t.Fatalf("calls = %#v", calls)
	}

	config.Zabbix.RepositoryPackageSHA256 = strings.Repeat("0", 64)
	calls = nil
	if err := backend.Apply(context.Background(), ItemPackages, Profile{
		Platform: profile.Platform, Config: config, Items: profile.Items,
	}); err == nil {
		t.Fatal("digest mismatch was accepted")
	}
	if len(calls) != 0 {
		t.Fatalf("commands ran after digest mismatch: %#v", calls)
	}
}

func TestSystemBackendTreatsMissingPackagesAsDrift(t *testing.T) {
	t.Parallel()

	backend := SystemBackend{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program != "dpkg-query" {
				return execx.Output{}, errors.New("unexpected program")
			}
			return execx.Output{
				ExitCode: 1,
				Stdout:   []byte("curl\tinstall ok installed\n"),
			}, nil
		}),
	}
	config := DefaultConfig()
	config.Packages = []string{"curl", "vim"}
	observation, err := backend.Observe(context.Background(), ItemPackages, Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages},
	})
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged ||
		!reflect.DeepEqual(observation.Details["missing"], []string{"vim"}) {
		t.Fatalf("observation = %#v", observation)
	}
}

func TestSystemBackendCreatesMissingAdministratorWithDirectArgv(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	passwd := filepath.Join(root, "etc", "passwd")
	if err := os.MkdirAll(filepath.Dir(passwd), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passwd, []byte("root:x:0:0:root:/root:/bin/bash\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls []execx.Spec
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			if spec.Program == "id" {
				return execx.Output{ExitCode: 1}, nil
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{Name: "operator", Groups: []string{"sudo", "adm"}}}
	if err := backend.Apply(context.Background(), ItemUsers, Profile{
		Platform: Platform{ID: "ubuntu", Version: "22.04"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers},
	}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Program != "id" || calls[1].Program != "useradd" {
		t.Fatalf("calls = %#v", calls)
	}
	if !reflect.DeepEqual(calls[1].Arguments,
		[]string{"--create-home", "--shell", "/bin/bash", "--groups", "adm,sudo", "--", "operator"}) {
		t.Fatalf("useradd argv = %#v", calls[1].Arguments)
	}
}

func TestSystemBackendRepairsOnlyMissingAdministratorGroups(t *testing.T) {
	t.Parallel()

	groups := []string{"operator", "sudo"}
	var calls []execx.Spec
	backend := SystemBackend{
		Root: t.TempDir(),
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			calls = append(calls, spec)
			switch {
			case spec.Program == "id" && containsArgument(spec.Arguments, "-u"):
				return execx.Output{Stdout: []byte("1000\n")}, nil
			case spec.Program == "id" && containsArgument(spec.Arguments, "-nG"):
				return execx.Output{Stdout: []byte(strings.Join(groups, " ") + "\n")}, nil
			case spec.Program == "usermod":
				groups = append(groups, "adm")
				return execx.Output{}, nil
			default:
				return execx.Output{}, errors.New("unexpected command")
			}
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{
		Name: "operator", Groups: []string{"sudo", "adm"},
	}}
	profile := Profile{
		Platform: Platform{ID: "debian", Version: "12"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers},
	}
	observation, err := backend.Observe(context.Background(), ItemUsers, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("missing administrator group was reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemUsers, profile); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), ItemUsers, profile); err != nil {
		t.Fatal(err)
	}
	var usermodCalls []execx.Spec
	for _, call := range calls {
		if call.Program == "usermod" {
			usermodCalls = append(usermodCalls, call)
		}
	}
	if len(usermodCalls) != 1 || !reflect.DeepEqual(
		usermodCalls[0].Arguments,
		[]string{"--append", "--groups", "adm", "--", "operator"},
	) {
		t.Fatalf("usermod calls = %#v", usermodCalls)
	}
}

func TestSystemBackendObservesAndInstallsAuthorizedKeysUnderInjectedRoot(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "etc", "ohtools", "plugins", "keys", "operator.pub")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	key := "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFixtureKey operator@example"
	if err := os.WriteFile(source, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program != "id" {
				return execx.Output{}, errors.New("unexpected command")
			}
			if containsArgument(spec.Arguments, "-u") {
				return execx.Output{Stdout: []byte("1000\n")}, nil
			}
			return execx.Output{Stdout: []byte("operator sudo\n")}, nil
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{
		Name: "operator", Groups: []string{"sudo"},
		AuthorizedKeySources: []string{
			"/etc/ohtools/plugins/keys/operator.pub",
		},
	}}
	profile := Profile{
		Platform: Platform{ID: "ubuntu", Version: "24.04"},
		Config:   config,
		Items:    []Item{ItemPackages, ItemUsers},
	}
	observation, err := backend.Observe(context.Background(), ItemUsers, profile)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Converged {
		t.Fatal("missing authorized key was reported as converged")
	}
	if err := backend.Apply(context.Background(), ItemUsers, profile); err != nil {
		t.Fatal(err)
	}
	if err := backend.Verify(context.Background(), ItemUsers, profile); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "home", "operator", ".ssh", "authorized_keys")
	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != key+"\n" {
		t.Fatalf("authorized_keys = %q", content)
	}
}

func TestSystemBackendChecksLegacyEntitlementWithoutMutation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	backend := SystemBackend{Root: root, Runner: successfulRunner()}
	if err := backend.Entitled(context.Background(), Platform{
		ID: "debian", Version: "10", RequiresExtendedSupport: true,
	}); err == nil {
		t.Fatal("missing Debian ELTS source was accepted")
	}
	source := filepath.Join(root, "etc", "apt", "sources.list.d", "elts.list")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("deb https://deb.freexian.com/extended-lts buster-lts main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := backend.Entitled(context.Background(), Platform{
		ID: "debian", Version: "10", RequiresExtendedSupport: true,
	}); err != nil {
		t.Fatal(err)
	}

	ubuntu := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program != "pro" {
				return execx.Output{}, errors.New("unexpected program")
			}
			return execx.Output{Stdout: []byte(`{"attached":true}`)}, nil
		}),
	}
	if err := ubuntu.Entitled(context.Background(), Platform{
		ID: "ubuntu", Version: "20.04", RequiresExtendedSupport: true,
	}); err != nil {
		t.Fatal(err)
	}
}

func successfulRunner() execx.Runner {
	return execx.RunnerFunc(func(context.Context, execx.Spec) (execx.Output, error) {
		return execx.Output{}, nil
	})
}

type httpDoerFunc func(*http.Request) (*http.Response, error)

func (function httpDoerFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}
