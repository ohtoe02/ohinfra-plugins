package serversetup

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

func TestResolvePlatformAcceptsOnlyDeclaredMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		id              string
		version         string
		extendedSupport bool
	}{
		{id: "debian", version: "10", extendedSupport: true},
		{id: "debian", version: "11"},
		{id: "debian", version: "12"},
		{id: "debian", version: "13"},
		{id: "ubuntu", version: "20.04", extendedSupport: true},
		{id: "ubuntu", version: "22.04"},
		{id: "ubuntu", version: "24.04"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.id+"-"+test.version, func(t *testing.T) {
			t.Parallel()
			platform, err := ResolvePlatform([]byte(
				"ID=" + test.id + "\nVERSION_ID=\"" + test.version + "\"\n",
			))
			if err != nil {
				t.Fatal(err)
			}
			if platform.ID != test.id || platform.Version != test.version ||
				platform.RequiresExtendedSupport != test.extendedSupport {
				t.Fatalf("platform = %#v", platform)
			}
		})
	}

	for _, release := range []string{
		"ID=debian\nVERSION_ID=9\n",
		"ID=ubuntu\nVERSION_ID=24.10\n",
		"ID=centos\nVERSION_ID=9\n",
		"ID=debian\n",
		"ID=debian\nID=ubuntu\nVERSION_ID=12\n",
	} {
		if _, err := ResolvePlatform([]byte(release)); err == nil {
			t.Fatalf("ResolvePlatform(%q) succeeded", release)
		}
	}
}

func TestExpandItemsIsStableAndIncludesDependencies(t *testing.T) {
	t.Parallel()

	config := DefaultConfig()
	config.ManageFirewall = true
	got, err := ExpandItems([]string{"zabbix", "fail2ban"}, config)
	if err != nil {
		t.Fatal(err)
	}
	want := []Item{
		ItemPackages,
		ItemUsers,
		ItemFirewall,
		ItemSSH,
		ItemFail2Ban,
		ItemZabbix,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandItems() = %#v, want %#v", got, want)
	}
	if _, err := ExpandItems([]string{"../ssh"}, config); err == nil {
		t.Fatal("unsafe item was accepted")
	}
}

func TestPlanAndApplyConvergeWithoutSecondMutation(t *testing.T) {
	t.Parallel()

	backend := &memoryBackend{
		drift: map[Item]bool{
			ItemPackages: true,
			ItemUsers:    true,
			ItemFirewall: true,
			ItemSSH:      true,
		},
	}
	config := DefaultConfig()
	config.ManageFirewall = true
	manager := Manager{
		Backend:  backend,
		Config:   config,
		Platform: Platform{ID: "debian", Version: "12"},
		Host:     "fixture",
		Tool:     protocol.Tool{Name: Name, Version: "1.0.0"},
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}

	first, err := manager.Plan(context.Background(), []string{"ssh"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Plan(context.Background(), []string{"ssh"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("plans differ:\nfirst=%#v\nsecond=%#v", first, second)
	}
	if len(first.Changes) != 4 {
		t.Fatalf("planned changes = %#v", first.Changes)
	}

	result, err := manager.Apply(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPass || len(result.Changes) != 4 {
		t.Fatalf("result = %#v", result)
	}
	if !reflect.DeepEqual(backend.applied, []Item{
		ItemPackages, ItemUsers, ItemFirewall, ItemSSH,
	}) {
		t.Fatalf("applied = %#v", backend.applied)
	}

	converged, err := manager.Plan(context.Background(), []string{"ssh"})
	if err != nil {
		t.Fatal(err)
	}
	if len(converged.Changes) != 0 {
		t.Fatalf("second plan changes = %#v", converged.Changes)
	}
	if _, err := manager.Apply(context.Background(), converged); err != nil {
		t.Fatal(err)
	}
	if len(backend.applied) != 4 {
		t.Fatalf("second apply mutated backend: %#v", backend.applied)
	}
}

func TestPlanDigestBindsHashedAuthorizedKeyMaterial(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "etc", "ohtools", "plugins", "keys", "operator.pub")
	if err := os.MkdirAll(filepath.Dir(source), 0o700); err != nil {
		t.Fatal(err)
	}
	firstKey := validEd25519PublicKey("first@example")
	if err := os.WriteFile(source, []byte(firstKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backend := SystemBackend{
		Root: root,
		Runner: execx.RunnerFunc(func(_ context.Context, spec execx.Spec) (execx.Output, error) {
			if spec.Program == "id" {
				return execx.Output{ExitCode: 1}, nil
			}
			return execx.Output{}, nil
		}),
	}
	config := DefaultConfig()
	config.Administrators = []Administrator{{
		Name:                 "operator",
		AuthorizedKeySources: []string{"/etc/ohtools/plugins/keys/operator.pub"},
	}}
	manager := Manager{
		Backend: backend, Config: config,
		Platform: Platform{ID: "debian", Version: "12"},
	}
	first, err := manager.Plan(context.Background(), []string{"users"})
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := protocol.PlanDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondKey := validEd25519PublicKeyWithSeed("second@example", 2)
	if secondKey == firstKey {
		t.Fatal("key fixture did not change")
	}
	if err := os.WriteFile(source, []byte(secondKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := manager.Plan(context.Background(), []string{"users"})
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := protocol.PlanDigest(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstDigest == secondDigest {
		t.Fatal("authorized key material did not change the plan digest")
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), firstKey) {
		t.Fatal("raw authorized key material leaked into the plan")
	}
}

func TestMutationRequiresExtendedSupportEntitlementBeforePlanningChanges(t *testing.T) {
	t.Parallel()

	backend := &memoryBackend{
		drift:       map[Item]bool{ItemPackages: true},
		entitledErr: errors.New("Debian ELTS source is unavailable"),
	}
	manager := Manager{
		Backend:  backend,
		Config:   DefaultConfig(),
		Platform: Platform{ID: "debian", Version: "10", RequiresExtendedSupport: true},
	}
	_, err := manager.Plan(context.Background(), []string{"packages"})
	var exitError protocol.ExitError
	if !errors.As(err, &exitError) || exitError.Code != protocol.ExitDependency {
		t.Fatalf("Plan() error = %#v, want dependency exit", err)
	}
	if backend.observeCalls != 0 {
		t.Fatalf("observed state after entitlement failure: %d calls", backend.observeCalls)
	}
}

func TestCheckReportsEverySelectedItemAndTargetedRecommendations(t *testing.T) {
	t.Parallel()

	backend := &memoryBackend{
		drift: map[Item]bool{
			ItemPackages: true,
			ItemUsers:    false,
			ItemFirewall: false,
			ItemSSH:      true,
		},
		entitledErr: errors.New("entitlement is missing"),
	}
	config := DefaultConfig()
	config.ManageFirewall = true
	manager := Manager{
		Backend: backend, Config: config,
		Platform: Platform{ID: "debian", Version: "10", RequiresExtendedSupport: true},
		Host:     "fixture",
		Tool:     protocol.Tool{Name: Name, Version: "1.0.0"},
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}
	result := manager.Check(context.Background(), []string{"ssh"})
	if result.Status != protocol.StatusWarning {
		t.Fatalf("status = %q, result=%#v", result.Status, result)
	}
	if len(result.Checks) != 4 {
		t.Fatalf("checks = %#v", result.Checks)
	}
	recommendations, ok := result.Data["recommendations"].([]string)
	if !ok {
		t.Fatalf("recommendations = %#v", result.Data["recommendations"])
	}
	if !reflect.DeepEqual(recommendations, []string{
		"ohtools setup apply packages",
		"ohtools setup apply ssh",
	}) {
		t.Fatalf("recommendations = %#v", recommendations)
	}
	if backend.entitledCalls != 0 {
		t.Fatalf("diagnostic check required entitlement: %d calls", backend.entitledCalls)
	}
}

func TestApplyRedactsFailureAndReportsPartialProgress(t *testing.T) {
	t.Parallel()

	backend := &failureBackend{
		memoryBackend: &memoryBackend{
			drift: map[Item]bool{ItemPackages: true, ItemSysctl: true},
		},
		failItem: ItemSysctl,
		failErr:  errors.New("password=hunter2 token=secret-value"),
	}
	manager := Manager{
		Backend: backend, Config: DefaultConfig(),
		Platform: Platform{ID: "debian", Version: "12"},
		Host:     "fixture",
		Tool:     protocol.Tool{Name: Name, Version: "1.0.0"},
		Now:      func() time.Time { return time.Unix(1_700_000_000, 0).UTC() },
	}
	plan, err := manager.Plan(context.Background(), []string{"sysctl"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != protocol.StatusPartial || len(result.Changes) != 1 ||
		len(result.Errors) != 1 {
		t.Fatalf("result = %#v", result)
	}
	encoded := result.Errors[0].Message
	if strings.Contains(encoded, "hunter2") || strings.Contains(encoded, "secret-value") {
		t.Fatalf("secret leaked in result: %q", encoded)
	}
}

func TestTargetedApplyDoesNotInspectOrMutateUnrelatedItems(t *testing.T) {
	t.Parallel()

	backend := &memoryBackend{
		drift: map[Item]bool{
			ItemPackages: true,
			ItemSysctl:   true,
			ItemSSH:      true,
		},
	}
	manager := Manager{
		Backend: backend, Config: DefaultConfig(),
		Platform: Platform{ID: "ubuntu", Version: "24.04"},
	}
	plan, err := manager.Plan(context.Background(), []string{"sysctl"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(backend.observed, []Item{ItemPackages, ItemSysctl}) {
		t.Fatalf("observed items = %#v", backend.observed)
	}
	if !reflect.DeepEqual(backend.applied, []Item{ItemPackages, ItemSysctl}) {
		t.Fatalf("applied items = %#v", backend.applied)
	}
}

type memoryBackend struct {
	drift         map[Item]bool
	observed      []Item
	applied       []Item
	observeCalls  int
	entitledCalls int
	entitledErr   error
}

func (backend *memoryBackend) Entitled(context.Context, Platform) error {
	backend.entitledCalls++
	return backend.entitledErr
}

func (backend *memoryBackend) Observe(
	_ context.Context,
	item Item,
	_ Profile,
) (Observation, error) {
	backend.observeCalls++
	backend.observed = append(backend.observed, item)
	return Observation{
		Converged: !backend.drift[item],
		Summary:   string(item) + " desired state",
	}, nil
}

type failureBackend struct {
	*memoryBackend
	failItem Item
	failErr  error
}

func (backend *failureBackend) Apply(
	ctx context.Context,
	item Item,
	profile Profile,
) error {
	if item == backend.failItem {
		return backend.failErr
	}
	return backend.memoryBackend.Apply(ctx, item, profile)
}

func (backend *memoryBackend) Apply(
	_ context.Context,
	item Item,
	_ Profile,
) error {
	backend.applied = append(backend.applied, item)
	backend.drift[item] = false
	return nil
}

func (backend *memoryBackend) Verify(
	_ context.Context,
	item Item,
	_ Profile,
) error {
	if backend.drift[item] {
		return errors.New("item is still drifted")
	}
	return nil
}
