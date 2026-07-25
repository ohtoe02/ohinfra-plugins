package serversetup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/strictjson"
)

type SystemBackend struct {
	Root            string
	Runner          execx.Runner
	HTTPClient      HTTPDoer
	rollbackTimeout time.Duration
}

type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type managedFile struct {
	Path       string
	Content    []byte
	Mode       fs.FileMode
	Validate   *execx.Spec
	Activation *execx.Spec
}

type managedTransactionJournal struct {
	SchemaVersion string `json:"schema_version"`
	Phase         string `json:"phase"`
	HadTarget     bool   `json:"had_target"`
}

const rollbackCommandTimeout = 10 * time.Second

func (backend SystemBackend) Entitled(ctx context.Context, platform Platform) error {
	if !platform.RequiresExtendedSupport {
		return nil
	}
	switch platform.ID {
	case "debian":
		found, err := backend.hasDebianELTS()
		if err != nil {
			return err
		}
		if !found {
			return errors.New("Debian ELTS source is unavailable")
		}
		return nil
	case "ubuntu":
		output, err := backend.run(ctx, execx.Spec{
			Program: "pro", Arguments: []string{"status", "--format", "json"},
		})
		if err != nil {
			return fmt.Errorf("inspect Ubuntu Pro entitlement: %w", err)
		}
		var status struct {
			Attached bool `json:"attached"`
		}
		if err := json.Unmarshal(output.Stdout, &status); err != nil {
			return fmt.Errorf("decode Ubuntu Pro status: %w", err)
		}
		if !status.Attached {
			return errors.New("Ubuntu Pro/ESM entitlement is unavailable")
		}
		return nil
	default:
		return fmt.Errorf("extended support is undefined for %s", platform.ID)
	}
}

func (backend SystemBackend) Observe(
	ctx context.Context,
	item Item,
	profile Profile,
) (Observation, error) {
	switch item {
	case ItemPackages:
		return backend.observePackages(ctx, profile)
	case ItemUsers:
		return backend.observeUsers(ctx, profile)
	case ItemSSH:
		return backend.observeSSH(ctx, profile)
	case ItemFirewall:
		return backend.observeFirewall(ctx, profile)
	default:
		managed, err := backend.desiredFile(item, profile)
		if err != nil {
			return Observation{}, err
		}
		if managed == nil {
			return Observation{Converged: true, Summary: string(item) + " is disabled"}, nil
		}
		persistent, err := backend.fileConverged(*managed)
		if err != nil {
			return Observation{}, err
		}
		effective := false
		if persistent {
			effective, err = backend.effectiveStateConverged(ctx, item, profile)
			if err != nil {
				return Observation{}, err
			}
		}
		converged := persistent && effective
		summary := string(item) + " configuration differs from the desired state"
		if converged {
			summary = string(item) + " configuration matches the desired state"
		}
		return Observation{Converged: converged, Summary: summary}, nil
	}
}

func (backend SystemBackend) PrepareDesiredState(
	_ context.Context,
	profile *Profile,
) (map[string]string, error) {
	material := make(map[string]string)
	profile.DesiredAuthorizedKeys = make(map[string][]string)
	if !slices.Contains(profile.Items, ItemUsers) {
		return material, nil
	}
	for _, administrator := range profile.Config.Administrators {
		if len(administrator.AuthorizedKeySources) == 0 {
			continue
		}
		keys, err := backend.desiredAuthorizedKeys(administrator)
		if err != nil {
			return nil, err
		}
		profile.DesiredAuthorizedKeys[administrator.Name] =
			append([]string(nil), keys...)
		encoded, err := json.Marshal(keys)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(encoded)
		material["administrator:"+administrator.Name+":authorized_keys"] =
			hex.EncodeToString(sum[:])
	}
	return material, nil
}

func (backend SystemBackend) Apply(
	ctx context.Context,
	item Item,
	profile Profile,
) error {
	return backend.apply(ctx, item, profile, nil)
}

func (backend SystemBackend) ApplyApproved(
	ctx context.Context,
	item Item,
	profile Profile,
	approved ApprovedObservation,
) error {
	return backend.apply(ctx, item, profile, &approved)
}

func (backend SystemBackend) apply(
	ctx context.Context,
	item Item,
	profile Profile,
	approved *ApprovedObservation,
) error {
	switch item {
	case ItemPackages:
		return backend.applyPackages(ctx, profile, approved)
	case ItemUsers:
		return backend.applyUsers(ctx, profile)
	case ItemSSH:
		return backend.applySSH(ctx, profile)
	case ItemFirewall:
		return backend.applyFirewall(ctx, profile)
	default:
		managed, err := backend.desiredFile(item, profile)
		if err != nil {
			return err
		}
		if managed == nil {
			return nil
		}
		if err := backend.recoverManagedTransaction(
			ctx,
			backend.path(managed.Path),
			managed.Activation,
		); err != nil {
			return err
		}
		persistent, err := backend.fileConverged(*managed)
		if err != nil {
			return err
		}
		effective := false
		if persistent {
			effective, err = backend.effectiveStateConverged(ctx, item, profile)
			if err != nil {
				return err
			}
		}
		if persistent && effective {
			return nil
		}
		service := managedServiceForItem(item)
		var serviceChange serviceMutation
		if service != "" {
			serviceChange, err = backend.ensureManagedService(
				ctx,
				service,
				managedServiceRequiresEnable(item),
			)
			if err != nil {
				return err
			}
		}
		withServiceRollback := func(cause error) error {
			if cause == nil || service == "" {
				return cause
			}
			return errors.Join(
				cause,
				backend.restoreManagedService(ctx, service, serviceChange),
			)
		}
		verify := func() error {
			if item == ItemCronPermissions {
				if err := backend.repairCronPermissions(); err != nil {
					return err
				}
			}
			converged, verifyErr := backend.managedFileContentConverged(*managed)
			if verifyErr != nil {
				return verifyErr
			}
			if !converged {
				return fmt.Errorf("%s persistent state did not converge", item)
			}
			converged, verifyErr = backend.effectiveStateConverged(ctx, item, profile)
			if verifyErr != nil {
				return verifyErr
			}
			if !converged {
				return fmt.Errorf("%s effective state did not converge", item)
			}
			return nil
		}
		if !persistent {
			return withServiceRollback(
				backend.activateManagedFileWithPostVerify(ctx, *managed, verify),
			)
		}
		if item == ItemCronPermissions {
			return verify()
		}
		if managed.Activation == nil {
			return fmt.Errorf("%s has no effective-state activation", item)
		}
		if _, err := backend.run(
			ctx,
			expandManagedSpec(*managed.Activation, "", backend.path(managed.Path)),
		); err != nil {
			return withServiceRollback(err)
		}
		return withServiceRollback(verify())
	}
}

func (backend SystemBackend) effectiveStateConverged(
	ctx context.Context,
	item Item,
	_ Profile,
) (bool, error) {
	if service := managedServiceForItem(item); service != "" {
		state, err := backend.captureManagedService(ctx, service)
		if err != nil {
			return false, err
		}
		if !state.active ||
			managedServiceRequiresEnable(item) && !state.enabled {
			return false, nil
		}
	}
	switch item {
	case ItemCronPermissions:
		return backend.cronPermissionsConverged()
	case ItemFail2Ban:
		return backend.commandConverged(ctx, execx.Spec{
			Program: "fail2ban-client", Arguments: []string{"status", "sshd"},
		})
	case ItemTimeSync:
		output, err := backend.runRaw(ctx, execx.Spec{
			Program:   "timedatectl",
			Arguments: []string{"show", "--property=NTPSynchronized", "--value"},
		})
		return err == nil && output.ExitCode == 0 &&
			strings.EqualFold(strings.TrimSpace(string(output.Stdout)), "yes"), err
	case ItemLogging:
		return true, nil
	case ItemAuditd:
		return backend.commandConverged(ctx, execx.Spec{
			Program: "augenrules", Arguments: []string{"--check"},
		})
	case ItemSysctl:
		output, err := backend.runRaw(ctx, execx.Spec{
			Program: "sysctl",
			Arguments: []string{
				"-n",
				"kernel.kptr_restrict",
				"kernel.dmesg_restrict",
				"fs.protected_hardlinks",
				"fs.protected_symlinks",
			},
		})
		if err != nil || output.ExitCode != 0 {
			return false, err
		}
		return strings.Fields(string(output.Stdout)) != nil &&
			slices.Equal(
				strings.Fields(string(output.Stdout)),
				[]string{"2", "1", "1", "1"},
			), nil
	case ItemZabbix:
		output, err := backend.runRaw(ctx, execx.Spec{
			Program: "zabbix_agent2", Arguments: []string{"-t", "agent.ping"},
		})
		return err == nil && output.ExitCode == 0 &&
			strings.Contains(string(output.Stdout), "[t|1]"), err
	default:
		return true, nil
	}
}

func (backend SystemBackend) commandConverged(
	ctx context.Context,
	spec execx.Spec,
) (bool, error) {
	output, err := backend.runRaw(ctx, spec)
	if err != nil {
		return false, err
	}
	return output.ExitCode == 0, nil
}

func (backend SystemBackend) cronPermissionsConverged() (bool, error) {
	directory := backend.path("/etc/cron.d")
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, fmt.Errorf("cron path %s is unsafe", path)
		}
		if err := validateManagedOwner(
			path,
			info,
			backend.Root != "",
		); err != nil {
			return false, err
		}
		if info.Mode().Perm()&0o022 != 0 {
			return false, nil
		}
	}
	return true, nil
}

func (backend SystemBackend) repairCronPermissions() error {
	directory := backend.path("/etc/cron.d")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("cron path %s is unsafe", path)
		}
		if err := validateManagedOwner(
			path,
			info,
			backend.Root != "",
		); err != nil {
			return err
		}
		if info.Mode().Perm()&0o022 != 0 {
			if err := os.Chmod(path, info.Mode().Perm()&^0o022); err != nil {
				return err
			}
		}
	}
	return syncDirectory(directory)
}

const sshIncludeDirective = "Include /etc/ssh/sshd_config.d/*.conf"

type sshIncludeChange struct {
	target  string
	backup  string
	changed bool
}

type fileSnapshot struct {
	path    string
	content []byte
	mode    fs.FileMode
	existed bool
}

type firewallState struct {
	enabled     bool
	unitActive  bool
	rulesActive bool
	activeRules []byte
}

type serviceState struct {
	enabled bool
	active  bool
}

type serviceMutation struct {
	previous       serviceState
	changedEnabled bool
	changedActive  bool
}

func managedServiceForItem(item Item) string {
	switch item {
	case ItemFail2Ban:
		return "fail2ban.service"
	case ItemTimeSync:
		return "systemd-timesyncd.service"
	case ItemLogging:
		return "systemd-journald.service"
	case ItemAuditd:
		return "auditd.service"
	case ItemZabbix:
		return "zabbix-agent2.service"
	default:
		return ""
	}
}

func managedServiceRequiresEnable(item Item) bool {
	return item != ItemLogging
}

func (backend SystemBackend) ensureManagedService(
	ctx context.Context,
	name string,
	requireEnabled bool,
) (serviceMutation, error) {
	previous, err := backend.captureManagedService(ctx, name)
	if err != nil {
		return serviceMutation{}, err
	}
	change := serviceMutation{previous: previous}
	if requireEnabled && !previous.enabled {
		if _, err := backend.run(ctx, execx.Spec{
			Program: "systemctl", Arguments: []string{"enable", "--", name},
		}); err != nil {
			return change, errors.Join(
				err,
				backend.restoreManagedService(ctx, name, change),
			)
		}
		change.changedEnabled = true
	}
	if !previous.active {
		if _, err := backend.run(ctx, execx.Spec{
			Program: "systemctl", Arguments: []string{"start", "--", name},
		}); err != nil {
			return change, errors.Join(
				err,
				backend.restoreManagedService(ctx, name, change),
			)
		}
		change.changedActive = true
	}
	return change, nil
}

func (backend SystemBackend) captureManagedService(
	ctx context.Context,
	name string,
) (serviceState, error) {
	state := serviceState{}
	enabled, err := backend.runRaw(ctx, execx.Spec{
		Program: "systemctl", Arguments: []string{"is-enabled", "--", name},
	})
	if err != nil {
		return state, err
	}
	if enabled.ExitCode != 0 && enabled.ExitCode != 1 {
		return state, fmt.Errorf("inspect %s enablement: exit %d", name, enabled.ExitCode)
	}
	state.enabled = enabled.ExitCode == 0
	active, err := backend.runRaw(ctx, execx.Spec{
		Program: "systemctl", Arguments: []string{"is-active", "--", name},
	})
	if err != nil {
		return state, err
	}
	if active.ExitCode != 0 && active.ExitCode != 3 {
		return state, fmt.Errorf("inspect %s state: exit %d", name, active.ExitCode)
	}
	state.active = active.ExitCode == 0
	return state, nil
}

func (backend SystemBackend) restoreManagedService(
	ctx context.Context,
	name string,
	change serviceMutation,
) error {
	var activeErr error
	if change.changedActive {
		activeAction := "stop"
		if change.previous.active {
			activeAction = "start"
		}
		_, activeErr = backend.runCleanup(ctx, execx.Spec{
			Program: "systemctl", Arguments: []string{activeAction, "--", name},
		})
	}
	var enableErr error
	if change.changedEnabled {
		enableAction := "disable"
		if change.previous.enabled {
			enableAction = "enable"
		}
		_, enableErr = backend.runCleanup(ctx, execx.Spec{
			Program: "systemctl", Arguments: []string{enableAction, "--", name},
		})
	}
	return errors.Join(activeErr, enableErr)
}

func (backend SystemBackend) applySSH(
	ctx context.Context,
	profile Profile,
) error {
	if profile.Config.ManageFirewall {
		if err := backend.ensureNoForeignInputBaseChain(ctx); err != nil {
			return err
		}
	}
	if err := backend.ensureSSHAdministratorAccess(ctx, profile); err != nil {
		return err
	}
	managed, err := backend.desiredFile(ItemSSH, profile)
	if err != nil {
		return err
	}
	snapshot, err := backend.captureFileSnapshot(backend.path(managed.Path))
	if err != nil {
		return err
	}
	include, err := backend.beginSSHInclude(ctx)
	if err != nil {
		return err
	}
	managed.Activation = nil
	if err := backend.activateManagedFile(ctx, *managed); err != nil {
		return errors.Join(err, include.rollback())
	}
	effective, err := backend.effectiveSSHConverged(ctx, profile.Config.SSHPort)
	if err != nil || !effective {
		if err == nil {
			err = errors.New(
				"foreign SSH configuration conflicts with the managed hardening profile",
			)
		}
		return errors.Join(
			err,
			backend.rollbackSSH(ctx, snapshot, include, false),
		)
	}
	if _, err := backend.run(ctx, execx.Spec{
		Program: "systemctl", Arguments: []string{"reload", "ssh.service"},
	}); err != nil {
		return errors.Join(
			err,
			backend.rollbackSSH(ctx, snapshot, include, true),
		)
	}
	return include.commit()
}

func (backend SystemBackend) rollbackSSH(
	ctx context.Context,
	snapshot fileSnapshot,
	include sshIncludeChange,
	reactivate bool,
) error {
	restoreErr := errors.Join(restoreFileSnapshot(snapshot), include.rollback())
	if restoreErr != nil || !reactivate {
		return restoreErr
	}
	_, reloadErr := backend.runCleanup(ctx, execx.Spec{
		Program: "systemctl", Arguments: []string{"reload", "ssh.service"},
	})
	return reloadErr
}

func (backend SystemBackend) ensureSSHAdministratorAccess(
	ctx context.Context,
	profile Profile,
) error {
	for _, administrator := range profile.Config.Administrators {
		if len(administrator.AuthorizedKeySources) == 0 {
			continue
		}
		state, err := backend.observeAdministrator(ctx, administrator, profile)
		if err != nil {
			return err
		}
		if state.Exists && !state.MissingAuthorizedKeys {
			return nil
		}
	}
	return errors.New(
		"SSH hardening requires a usable administrator login with an installed validated key",
	)
}

func (backend SystemBackend) captureFileSnapshot(path string) (fileSnapshot, error) {
	snapshot := fileSnapshot{path: path}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return snapshot, nil
	}
	if err != nil {
		return snapshot, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Size() > 1<<20 {
		return snapshot, fmt.Errorf("managed path %s is unsafe", path)
	}
	if err := validateManagedPath(path, info, false, backend.Root != ""); err != nil {
		return snapshot, err
	}
	content, err := os.ReadFile(path) // #nosec G304 -- fixed managed path.
	if err != nil {
		return snapshot, err
	}
	snapshot.content = content
	snapshot.mode = info.Mode().Perm()
	snapshot.existed = true
	return snapshot, nil
}

func restoreFileSnapshot(snapshot fileSnapshot) error {
	if !snapshot.existed {
		if err := os.Remove(snapshot.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return syncDirectory(filepath.Dir(snapshot.path))
	}
	return writeFileAtomic(snapshot.path, snapshot.content, snapshot.mode)
}

func (backend SystemBackend) observeFirewall(
	ctx context.Context,
	profile Profile,
) (Observation, error) {
	files, err := backend.firewallFiles(profile)
	if err != nil {
		return Observation{}, err
	}
	for _, managed := range files {
		converged, err := backend.fileConverged(managed)
		if err != nil {
			return Observation{}, err
		}
		if !converged {
			return Observation{
				Converged: false,
				Summary:   "firewall persistence differs from the desired state",
			}, nil
		}
	}
	enabled, err := backend.runRaw(ctx, execx.Spec{
		Program: "systemctl",
		Arguments: []string{
			"is-enabled", "--", "ohtools-server-setup-firewall.service",
		},
	})
	if err != nil {
		return Observation{}, err
	}
	if enabled.ExitCode != 0 && enabled.ExitCode != 1 {
		return Observation{}, fmt.Errorf(
			"inspect firewall service enablement: exit %d",
			enabled.ExitCode,
		)
	}
	unitActive, err := backend.runRaw(ctx, execx.Spec{
		Program: "systemctl",
		Arguments: []string{
			"is-active", "--", "ohtools-server-setup-firewall.service",
		},
	})
	if err != nil {
		return Observation{}, err
	}
	if unitActive.ExitCode != 0 && unitActive.ExitCode != 3 {
		return Observation{}, fmt.Errorf(
			"inspect firewall service active state: exit %d",
			unitActive.ExitCode,
		)
	}
	active, err := backend.runRaw(ctx, execx.Spec{
		Program: "nft",
		Arguments: []string{
			"list", "table", "inet", "ohtools_server_setup",
		},
	})
	if err != nil {
		return Observation{}, err
	}
	if active.ExitCode != 0 && active.ExitCode != 1 {
		return Observation{}, fmt.Errorf(
			"inspect managed firewall table: exit %d",
			active.ExitCode,
		)
	}
	rules, err := backend.desiredFile(ItemFirewall, profile)
	if err != nil {
		return Observation{}, err
	}
	converged := enabled.ExitCode == 0 && unitActive.ExitCode == 0 &&
		active.ExitCode == 0 &&
		firewallRulesEqual(active.Stdout, rules.Content)
	summary := "firewall service or active rules differ from the desired state"
	if converged {
		summary = "firewall persistence and active rules match the desired state"
	}
	return Observation{Converged: converged, Summary: summary}, nil
}

func (backend SystemBackend) applyFirewall(
	ctx context.Context,
	profile Profile,
) error {
	if err := backend.ensureNoForeignInputBaseChain(ctx); err != nil {
		return err
	}
	previousState, err := backend.captureFirewallState(ctx)
	if err != nil {
		return err
	}
	files, err := backend.firewallFiles(profile)
	if err != nil {
		return err
	}
	type candidate struct {
		managed   managedFile
		snapshot  fileSnapshot
		converged bool
	}
	candidates := make([]candidate, 0, len(files))
	snapshots := make([]fileSnapshot, 0, len(files))
	unitChanged := false
	for _, managed := range files {
		if err := backend.recoverManagedTransaction(
			ctx,
			backend.path(managed.Path),
			managed.Activation,
		); err != nil {
			return err
		}
		snapshot, err := backend.captureFileSnapshot(backend.path(managed.Path))
		if err != nil {
			return err
		}
		converged, err := backend.fileConverged(managed)
		if err != nil {
			return err
		}
		if !converged && managed.Validate != nil {
			if _, err := backend.run(ctx, execx.Spec{
				Program:   managed.Validate.Program,
				Arguments: []string{"-c", "-f", "-"},
				Stdin:     append([]byte(nil), managed.Content...),
			}); err != nil {
				return fmt.Errorf("validate %s: %w", managed.Path, err)
			}
		}
		if !converged &&
			managed.Path == "/etc/systemd/system/ohtools-server-setup-firewall.service" {
			unitChanged = true
		}
		candidates = append(candidates, candidate{
			managed: managed, snapshot: snapshot, converged: converged,
		})
		snapshots = append(snapshots, snapshot)
	}
	for _, candidate := range candidates {
		if candidate.converged {
			continue
		}
		candidate.managed.Activation = nil
		if err := backend.activateManagedFile(ctx, candidate.managed); err != nil {
			return errors.Join(
				err,
				backend.rollbackFirewall(ctx, snapshots, unitChanged, previousState),
			)
		}
	}
	if unitChanged {
		if _, err := backend.run(ctx, execx.Spec{
			Program: "systemctl", Arguments: []string{"daemon-reload"},
		}); err != nil {
			return errors.Join(
				err,
				backend.rollbackFirewall(ctx, snapshots, unitChanged, previousState),
			)
		}
	}
	if _, err := backend.run(ctx, execx.Spec{
		Program: "systemctl",
		Arguments: []string{
			"enable", "--", "ohtools-server-setup-firewall.service",
		},
	}); err != nil {
		return errors.Join(
			err,
			backend.rollbackFirewall(ctx, snapshots, unitChanged, previousState),
		)
	}
	if _, err := backend.run(ctx, execx.Spec{
		Program: "systemctl",
		Arguments: []string{
			"restart", "--", "ohtools-server-setup-firewall.service",
		},
	}); err != nil {
		return errors.Join(
			err,
			backend.rollbackFirewall(ctx, snapshots, unitChanged, previousState),
		)
	}
	observation, err := backend.observeFirewall(ctx, profile)
	if err != nil || !observation.Converged {
		if err == nil {
			err = errors.New("firewall did not converge")
		}
		return errors.Join(
			err,
			backend.rollbackFirewall(ctx, snapshots, unitChanged, previousState),
		)
	}
	return nil
}

func (backend SystemBackend) rollbackFirewall(
	ctx context.Context,
	snapshots []fileSnapshot,
	unitChanged bool,
	previous firewallState,
) error {
	restoreErr := restoreFileSnapshots(snapshots)
	var reloadErr error
	if unitChanged {
		_, reloadErr = backend.runCleanup(ctx, execx.Spec{
			Program: "systemctl", Arguments: []string{"daemon-reload"},
		})
	}
	action := "disable"
	if previous.enabled {
		action = "enable"
	}
	_, enablementErr := backend.runCleanup(ctx, execx.Spec{
		Program: "systemctl",
		Arguments: []string{
			action, "--", "ohtools-server-setup-firewall.service",
		},
	})
	activeAction := "stop"
	if previous.unitActive {
		activeAction = "start"
	}
	_, unitActiveErr := backend.runCleanup(ctx, execx.Spec{
		Program: "systemctl",
		Arguments: []string{
			activeAction, "--", "ohtools-server-setup-firewall.service",
		},
	})
	rulesErr := backend.restoreFirewallActiveState(ctx, previous)
	return errors.Join(
		restoreErr,
		reloadErr,
		enablementErr,
		unitActiveErr,
		rulesErr,
	)
}

func (backend SystemBackend) captureFirewallState(ctx context.Context) (firewallState, error) {
	state := firewallState{}
	enabled, err := backend.runRaw(ctx, execx.Spec{
		Program: "systemctl",
		Arguments: []string{
			"is-enabled", "--", "ohtools-server-setup-firewall.service",
		},
	})
	if err != nil {
		return state, err
	}
	if enabled.ExitCode != 0 && enabled.ExitCode != 1 {
		return state, fmt.Errorf("inspect firewall enablement: exit %d", enabled.ExitCode)
	}
	state.enabled = enabled.ExitCode == 0
	unitActive, err := backend.runRaw(ctx, execx.Spec{
		Program: "systemctl",
		Arguments: []string{
			"is-active", "--", "ohtools-server-setup-firewall.service",
		},
	})
	if err != nil {
		return state, err
	}
	if unitActive.ExitCode != 0 && unitActive.ExitCode != 3 {
		return state, fmt.Errorf(
			"inspect firewall unit active state: exit %d",
			unitActive.ExitCode,
		)
	}
	state.unitActive = unitActive.ExitCode == 0
	active, err := backend.runRaw(ctx, execx.Spec{
		Program: "nft",
		Arguments: []string{
			"list", "table", "inet", "ohtools_server_setup",
		},
	})
	if err != nil {
		return state, err
	}
	if active.ExitCode != 0 && active.ExitCode != 1 {
		return state, fmt.Errorf("inspect active firewall rules: exit %d", active.ExitCode)
	}
	state.rulesActive = active.ExitCode == 0
	state.activeRules = append([]byte(nil), active.Stdout...)
	return state, nil
}

func (backend SystemBackend) restoreFirewallActiveState(
	ctx context.Context,
	previous firewallState,
) error {
	if previous.rulesActive {
		batch := append(
			[]byte("delete table inet ohtools_server_setup\n"),
			previous.activeRules...,
		)
		_, err := backend.runCleanup(ctx, execx.Spec{
			Program:   "nft",
			Arguments: []string{"-f", "-"},
			Stdin:     batch,
		})
		return err
	}
	deleted, err := backend.runRawCleanup(ctx, execx.Spec{
		Program: "nft",
		Arguments: []string{
			"delete", "table", "inet", "ohtools_server_setup",
		},
	})
	if err != nil {
		return err
	}
	if deleted.ExitCode != 0 && deleted.ExitCode != 1 {
		return fmt.Errorf("remove failed active firewall state: exit %d", deleted.ExitCode)
	}
	return nil
}

func (backend SystemBackend) ensureNoForeignInputBaseChain(ctx context.Context) error {
	if backend.Root != "" {
		return nil
	}
	output, err := backend.run(ctx, execx.Spec{
		Program: "nft", Arguments: []string{"--json", "list", "ruleset"},
	})
	if err != nil {
		return fmt.Errorf("inspect nftables input chains: %w", err)
	}
	return rejectForeignInputBaseChains(output.Stdout)
}

func rejectForeignInputBaseChains(input []byte) error {
	var ruleset struct {
		NFTables []struct {
			Chain *struct {
				Family string `json:"family"`
				Table  string `json:"table"`
				Name   string `json:"name"`
				Hook   string `json:"hook"`
			} `json:"chain"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal(input, &ruleset); err != nil {
		return fmt.Errorf("decode nftables ruleset: %w", err)
	}
	if ruleset.NFTables == nil {
		return errors.New("nftables ruleset is missing nftables array")
	}
	for _, object := range ruleset.NFTables {
		if object.Chain != nil && object.Chain.Hook == "input" &&
			(object.Chain.Family != "inet" ||
				object.Chain.Table != "ohtools_server_setup") {
			return fmt.Errorf(
				"foreign nftables input base chain %s/%s/%s can block the managed SSH port",
				object.Chain.Family,
				object.Chain.Table,
				object.Chain.Name,
			)
		}
	}
	return nil
}

func (backend SystemBackend) firewallFiles(profile Profile) ([]managedFile, error) {
	rules, err := backend.desiredFile(ItemFirewall, profile)
	if err != nil {
		return nil, err
	}
	service := managedFile{
		Path: "/etc/systemd/system/ohtools-server-setup-firewall.service",
		Mode: 0o644,
		Content: []byte(
			"[Unit]\n" +
				"Description=ohtools managed firewall rules\n" +
				"After=network-pre.target\n" +
				"Before=network.target\n\n" +
				"[Service]\n" +
				"Type=oneshot\n" +
				"RemainAfterExit=yes\n" +
				"ExecStartPre=-/usr/sbin/nft delete table inet ohtools_server_setup\n" +
				"ExecStart=/usr/sbin/nft -f /etc/nftables.d/ohtools-server-setup.nft\n\n" +
				"[Install]\n" +
				"WantedBy=multi-user.target\n",
		),
	}
	return []managedFile{*rules, service}, nil
}

func restoreFileSnapshots(snapshots []fileSnapshot) error {
	var failures []error
	for index := len(snapshots) - 1; index >= 0; index-- {
		if err := restoreFileSnapshot(snapshots[index]); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (backend SystemBackend) observeSSH(
	ctx context.Context,
	profile Profile,
) (Observation, error) {
	managed, err := backend.desiredFile(ItemSSH, profile)
	if err != nil {
		return Observation{}, err
	}
	fileMatches, err := backend.fileConverged(*managed)
	if err != nil {
		return Observation{}, err
	}
	includePresent, err := backend.sshIncludePresent()
	if err != nil {
		return Observation{}, err
	}
	if !fileMatches || !includePresent {
		return Observation{
			Converged: false,
			Summary:   "ssh configuration differs from the desired state",
		}, nil
	}
	effective, err := backend.effectiveSSHConverged(ctx, profile.Config.SSHPort)
	if err != nil {
		return Observation{}, err
	}
	if !effective {
		return Observation{}, errors.New(
			"foreign SSH configuration conflicts with the managed hardening profile",
		)
	}
	return Observation{
		Converged: true,
		Summary:   "ssh configuration matches the desired effective state",
	}, nil
}

func (backend SystemBackend) sshIncludePresent() (bool, error) {
	target := backend.path("/etc/ssh/sshd_config")
	recoverable, err := backend.sshIncludeRecoveryPending(target)
	if err != nil {
		return false, err
	}
	if recoverable {
		return false, nil
	}
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Size() > 1<<20 ||
		runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return false, errors.New("main SSH configuration is unsafe")
	}
	if err := validateManagedPath(target, info, false, backend.Root != ""); err != nil {
		return false, err
	}
	content, err := os.ReadFile(target) // #nosec G304 -- fixed system configuration path.
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return strings.EqualFold(line, sshIncludeDirective), nil
	}
	return false, nil
}

func (backend SystemBackend) sshIncludeRecoveryPending(target string) (bool, error) {
	stageExists, err := backend.trustedRecoveryArtifactExists(
		target + ".ohtools-include.stage",
	)
	if err != nil {
		return false, err
	}
	backupExists, err := backend.trustedRecoveryArtifactExists(
		target + ".ohtools-include.rollback",
	)
	if err != nil {
		return false, err
	}
	if !stageExists && !backupExists {
		return false, nil
	}
	targetExists, err := backend.trustedRecoveryArtifactExists(target)
	if err != nil {
		return false, err
	}
	if stageExists && !targetExists && !backupExists {
		return false, errors.New(
			"SSH include transaction lost both original and rollback files",
		)
	}
	if stageExists && targetExists && backupExists {
		return false, errors.New("SSH include transaction has impossible artifacts")
	}
	return true, nil
}

func (backend SystemBackend) beginSSHInclude(
	ctx context.Context,
) (change sshIncludeChange, returnErr error) {
	target := backend.path("/etc/ssh/sshd_config")
	change.target = target
	if err := backend.recoverSSHInclude(target); err != nil {
		return change, err
	}
	includePresent, err := backend.sshIncludePresent()
	if err != nil {
		return change, err
	}
	if includePresent {
		return change, nil
	}
	info, err := os.Lstat(target)
	if err != nil {
		return change, fmt.Errorf("inspect main SSH configuration: %w", err)
	}
	if err := validateManagedPath(target, info, false, backend.Root != ""); err != nil {
		return change, err
	}
	original, err := os.ReadFile(target) // #nosec G304 -- fixed system configuration path.
	if err != nil {
		return change, err
	}
	directory := filepath.Dir(target)
	if err := secureMkdirAll(
		backend.path("/etc/ssh/sshd_config.d"),
		0o755,
		backend.Root != "",
	); err != nil {
		return change, err
	}
	staged, err := os.OpenFile(
		target+".ohtools-include.stage",
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		0o600,
	)
	if err != nil {
		return change, fmt.Errorf("stage main SSH configuration: %w", err)
	}
	stagedPath := staged.Name()
	defer func() {
		if cleanupErr := os.Remove(stagedPath); cleanupErr != nil &&
			!errors.Is(cleanupErr, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()
	content := append([]byte(sshIncludeDirective+"\n"), original...)
	if _, err := staged.Write(content); err != nil {
		_ = staged.Close()
		return change, err
	}
	if err := staged.Sync(); err != nil {
		_ = staged.Close()
		return change, err
	}
	if err := staged.Chmod(info.Mode().Perm()); err != nil {
		_ = staged.Close()
		return change, err
	}
	if err := staged.Sync(); err != nil {
		_ = staged.Close()
		return change, err
	}
	if err := staged.Close(); err != nil {
		return change, err
	}
	if _, err := backend.run(ctx, execx.Spec{
		Program: "sshd", Arguments: []string{"-t", "-f", stagedPath},
	}); err != nil {
		return change, fmt.Errorf("validate main SSH configuration: %w", err)
	}
	change.backup = target + ".ohtools-include.rollback"
	if _, err := os.Lstat(change.backup); err == nil {
		return change, errors.New("stale SSH include rollback file requires recovery")
	} else if !errors.Is(err, os.ErrNotExist) {
		return change, err
	}
	if err := os.Rename(target, change.backup); err != nil {
		return change, err
	}
	change.changed = true
	if err := syncDirectory(directory); err != nil {
		return change, errors.Join(err, change.rollback())
	}
	if err := os.Rename(stagedPath, target); err != nil {
		return change, errors.Join(err, change.rollback())
	}
	if err := syncDirectory(directory); err != nil {
		return change, errors.Join(err, change.rollback())
	}
	return change, nil
}

func (backend SystemBackend) recoverSSHInclude(target string) error {
	backup := target + ".ohtools-include.rollback"
	backupExists, err := backend.trustedRecoveryArtifactExists(backup)
	if err != nil {
		return err
	}
	if backupExists {
		targetExists, err := backend.trustedRecoveryArtifactExists(target)
		if err != nil {
			return err
		}
		if targetExists {
			if err := os.Remove(target); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(target)); err != nil {
				return err
			}
		}
		if err := os.Rename(backup, target); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(target)); err != nil {
			return err
		}
	}
	return backend.removeTrustedRecoveryArtifact(
		target + ".ohtools-include.stage",
	)
}

func (change sshIncludeChange) rollback() error {
	if !change.changed {
		return nil
	}
	var failures []error
	if err := os.Remove(change.target); err != nil && !errors.Is(err, os.ErrNotExist) {
		failures = append(failures, err)
	}
	if len(failures) == 0 {
		if err := syncDirectory(filepath.Dir(change.target)); err != nil {
			failures = append(failures, err)
		}
	}
	if err := os.Rename(change.backup, change.target); err != nil {
		failures = append(failures, err)
	}
	if err := syncDirectory(filepath.Dir(change.target)); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (change sshIncludeChange) commit() error {
	if !change.changed {
		return nil
	}
	if err := os.Remove(change.backup); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(change.target))
}

func (backend SystemBackend) effectiveSSHConverged(
	ctx context.Context,
	port int,
) (bool, error) {
	output, err := backend.run(ctx, execx.Spec{
		Program: "sshd",
		Arguments: []string{
			"-T", "-f", backend.path("/etc/ssh/sshd_config"),
		},
	})
	if err != nil {
		return false, fmt.Errorf("inspect effective SSH configuration: %w", err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) >= 2 {
			values[strings.ToLower(fields[0])] = strings.ToLower(fields[1])
		}
	}
	rootLogin := values["permitrootlogin"]
	return values["port"] == fmt.Sprintf("%d", port) &&
		(rootLogin == "prohibit-password" || rootLogin == "without-password") &&
		values["passwordauthentication"] == "no" &&
		values["kbdinteractiveauthentication"] == "no" &&
		values["pubkeyauthentication"] == "yes", nil
}

func (backend SystemBackend) Verify(
	ctx context.Context,
	item Item,
	profile Profile,
) error {
	observation, err := backend.Observe(ctx, item, profile)
	if err != nil {
		return err
	}
	if !observation.Converged {
		return fmt.Errorf("%s did not converge", item)
	}
	return nil
}

func (backend SystemBackend) observePackages(
	ctx context.Context,
	profile Profile,
) (Observation, error) {
	packages := desiredPackages(profile)
	if len(packages) == 0 {
		return Observation{Converged: true, Summary: "no packages are required"}, nil
	}
	arguments := []string{"--show", "--showformat=${binary:Package}\\t${db:Status}\\n", "--"}
	arguments = append(arguments, packages...)
	output, err := backend.runRaw(ctx, execx.Spec{Program: "dpkg-query", Arguments: arguments})
	if err != nil {
		return Observation{}, err
	}
	if output.ExitCode != 0 && output.ExitCode != 1 {
		return Observation{}, fmt.Errorf(
			"dpkg-query exited with %d: %s",
			output.ExitCode,
			strings.TrimSpace(string(output.Stderr)),
		)
	}
	installed := map[string]bool{}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		name, status, found := strings.Cut(line, "\t")
		if found && strings.TrimSpace(status) == "install ok installed" {
			installed[strings.TrimSpace(name)] = true
		}
	}
	missing := []string{}
	for _, name := range packages {
		if !installed[name] {
			missing = append(missing, name)
		}
	}
	return Observation{
		Converged: len(missing) == 0,
		Summary:   fmt.Sprintf("%d required package(s), %d missing", len(packages), len(missing)),
		Details:   map[string]any{"missing": missing},
	}, nil
}

func (backend SystemBackend) applyPackages(
	ctx context.Context,
	profile Profile,
	approved *ApprovedObservation,
) error {
	packages := desiredPackages(profile)
	if len(packages) == 0 {
		return nil
	}
	observation, err := backend.observePackages(ctx, profile)
	if err != nil {
		return err
	}
	missing, ok := observation.Details["missing"].([]string)
	if !ok {
		return errors.New("package observation did not return an exact missing set")
	}
	if approved != nil {
		expected, err := approvedMissingPackages(approved.Details)
		if err != nil {
			return err
		}
		if !slices.Equal(missing, expected) {
			return fmt.Errorf(
				"%w: approved missing packages %v, current %v",
				ErrApprovedStateChanged,
				expected,
				missing,
			)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	installTargets := append([]string(nil), missing...)
	if slices.Contains(profile.Items, ItemZabbix) &&
		slices.Contains(installTargets, "zabbix-release") {
		if err := backend.installZabbixRepository(ctx, profile.Config); err != nil {
			return err
		}
		installTargets = slices.DeleteFunc(
			installTargets,
			func(name string) bool { return name == "zabbix-release" },
		)
	}
	if len(installTargets) > 0 {
		environment := map[string]string{"DEBIAN_FRONTEND": "noninteractive"}
		if _, err := backend.run(ctx, execx.Spec{
			Program: "apt-get", Arguments: []string{"update"}, Environment: environment,
		}); err != nil {
			return err
		}
		arguments := []string{
			"install", "-y", "--no-install-recommends", "--no-upgrade", "--",
		}
		arguments = append(arguments, installTargets...)
		if _, err := backend.run(ctx, execx.Spec{
			Program: "apt-get", Arguments: arguments, Environment: environment,
		}); err != nil {
			return err
		}
	}
	verified, err := backend.observePackages(ctx, profile)
	if err != nil {
		return err
	}
	if !verified.Converged {
		return errors.New("required packages did not converge after installation")
	}
	return nil
}

func approvedMissingPackages(details map[string]any) ([]string, error) {
	raw, present := details["missing"]
	if !present {
		return nil, errors.New("approved package observation is missing exact state")
	}
	var values []string
	switch typed := raw.(type) {
	case []string:
		values = append([]string(nil), typed...)
	case []any:
		values = make([]string, 0, len(typed))
		for _, value := range typed {
			name, ok := value.(string)
			if !ok {
				return nil, errors.New("approved package missing set is invalid")
			}
			values = append(values, name)
		}
	default:
		return nil, errors.New("approved package missing set is invalid")
	}
	if !sort.StringsAreSorted(values) {
		return nil, errors.New("approved package missing set is not canonical")
	}
	for index, name := range values {
		if !packageName.MatchString(name) ||
			index > 0 && values[index-1] == name {
			return nil, errors.New("approved package missing set is invalid")
		}
	}
	return values, nil
}

func (backend SystemBackend) packageInstalled(
	ctx context.Context,
	name string,
) (bool, error) {
	output, err := backend.runRaw(ctx, execx.Spec{
		Program: "dpkg-query",
		Arguments: []string{
			"--show", "--showformat=${binary:Package}\\t${db:Status}\\n", "--", name,
		},
	})
	if err != nil {
		return false, err
	}
	if output.ExitCode != 0 && output.ExitCode != 1 {
		return false, fmt.Errorf(
			"dpkg-query exited with %d: %s",
			output.ExitCode,
			strings.TrimSpace(string(output.Stderr)),
		)
	}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		packageName, status, found := strings.Cut(line, "\t")
		if found && packageName == name &&
			strings.TrimSpace(status) == "install ok installed" {
			return true, nil
		}
	}
	return false, nil
}

func (backend SystemBackend) installZabbixRepository(
	ctx context.Context,
	config Config,
) (returnErr error) {
	if config.Zabbix == nil || !config.Zabbix.Enabled {
		return errors.New("zabbix repository metadata is unavailable")
	}
	client := backend.HTTPClient
	if client == nil {
		client = pinnedHTTPClient()
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		config.Zabbix.RepositoryPackageURL,
		nil,
	)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download Zabbix repository package: %w", err)
	}
	defer func() {
		returnErr = errors.Join(returnErr, response.Body.Close())
	}()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download Zabbix repository package: HTTP %d", response.StatusCode)
	}
	if response.ContentLength >= 0 &&
		response.ContentLength != config.Zabbix.RepositoryPackageSize {
		return errors.New("Zabbix repository package size does not match")
	}
	content, err := io.ReadAll(io.LimitReader(
		response.Body,
		config.Zabbix.RepositoryPackageSize+1,
	))
	if err != nil {
		return err
	}
	if int64(len(content)) != config.Zabbix.RepositoryPackageSize {
		return errors.New("Zabbix repository package size does not match")
	}
	expected, err := hex.DecodeString(config.Zabbix.RepositoryPackageSHA256)
	if err != nil {
		return errors.New("Zabbix repository package SHA-256 is invalid")
	}
	actual := sha256.Sum256(content)
	if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
		return errors.New("Zabbix repository package SHA-256 does not match")
	}
	directory := backend.path("/var/cache/ohtools/server-setup/downloads")
	if err := secureMkdirAll(directory, 0o700, backend.Root != ""); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, "zabbix-release-*.deb")
	if err != nil {
		return err
	}
	path := file.Name()
	defer func() {
		removeErr := os.Remove(path)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		var syncErr error
		if removeErr == nil {
			syncErr = syncDirectory(directory)
		}
		returnErr = errors.Join(returnErr, removeErr, syncErr)
	}()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	_, err = backend.run(ctx, execx.Spec{
		Program: "dpkg", Arguments: []string{"--install", "--", path},
	})
	return err
}

func pinnedHTTPClient() *http.Client {
	transport := &http.Transport{
		Proxy:              nil,
		DisableCompression: true,
		TLSClientConfig:    &tls.Config{MinVersion: tls.VersionTLS12},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 || request.URL.Scheme != "https" ||
				request.URL.Hostname() != "repo.zabbix.com" ||
				request.URL.Port() != "" || request.URL.User != nil {
				return errors.New("unsafe Zabbix repository redirect")
			}
			return nil
		},
	}
}

func desiredPackages(profile Profile) []string {
	set := map[string]bool{}
	for _, name := range profile.Config.Packages {
		set[name] = true
	}
	for _, item := range profile.Items {
		switch item {
		case ItemUsers:
			if len(profile.Config.Administrators) > 0 {
				set["sudo"] = true
			}
		case ItemCronPermissions:
			set["cron"] = true
		case ItemSSH:
			set["openssh-server"] = true
		case ItemFail2Ban:
			set["fail2ban"] = true
		case ItemTimeSync:
			set["systemd-timesyncd"] = true
		case ItemSecurityUpdates:
			set["unattended-upgrades"] = true
		case ItemLogging:
			set["rsyslog"] = true
		case ItemAuditd:
			set["auditd"] = true
		case ItemFirewall:
			set["nftables"] = true
		case ItemSysctl:
			set["procps"] = true
		case ItemZabbix:
			set["zabbix-release"] = true
			set["zabbix-agent2"] = true
		}
	}
	output := make([]string, 0, len(set))
	for name := range set {
		output = append(output, name)
	}
	sort.Strings(output)
	return output
}

func (backend SystemBackend) observeUsers(
	ctx context.Context,
	profile Profile,
) (Observation, error) {
	missingUsers := []string{}
	missingGroups := map[string]any{}
	missingKeys := []string{}
	for _, administrator := range profile.Config.Administrators {
		state, err := backend.observeAdministrator(ctx, administrator, profile)
		if err != nil {
			return Observation{}, err
		}
		if !state.Exists {
			missingUsers = append(missingUsers, administrator.Name)
			continue
		}
		if len(state.MissingGroups) > 0 {
			missingGroups[administrator.Name] = state.MissingGroups
		}
		if state.MissingAuthorizedKeys {
			missingKeys = append(missingKeys, administrator.Name)
		}
	}
	converged := len(missingUsers) == 0 && len(missingGroups) == 0 && len(missingKeys) == 0
	return Observation{
		Converged: converged,
		Summary: fmt.Sprintf(
			"%d administrator account(s), %d drifted",
			len(profile.Config.Administrators),
			len(missingUsers)+len(missingGroups)+len(missingKeys),
		),
		Details: map[string]any{
			"missing_users":           missingUsers,
			"missing_groups":          missingGroups,
			"missing_authorized_keys": missingKeys,
		},
	}, nil
}

func (backend SystemBackend) applyUsers(ctx context.Context, profile Profile) error {
	for _, administrator := range profile.Config.Administrators {
		state, err := backend.observeAdministrator(ctx, administrator, profile)
		if err != nil {
			return err
		}
		if !state.Exists {
			arguments := []string{"--create-home", "--shell", "/bin/bash"}
			if len(administrator.Groups) > 0 {
				groups := append([]string(nil), administrator.Groups...)
				sort.Strings(groups)
				arguments = append(arguments, "--groups", strings.Join(groups, ","))
			}
			arguments = append(arguments, "--", administrator.Name)
			if _, err := backend.run(ctx, execx.Spec{
				Program: "useradd", Arguments: arguments,
			}); err != nil {
				return err
			}
			if len(administrator.AuthorizedKeySources) > 0 {
				state, err = backend.observeAdministrator(ctx, administrator, profile)
				if err != nil {
					return err
				}
				if !state.Exists {
					return fmt.Errorf(
						"administrator %s was not created",
						administrator.Name,
					)
				}
			}
		} else if len(state.MissingGroups) > 0 {
			if _, err := backend.run(ctx, execx.Spec{
				Program: "usermod",
				Arguments: []string{
					"--append", "--groups", strings.Join(state.MissingGroups, ","),
					"--", administrator.Name,
				},
			}); err != nil {
				return err
			}
		}
		if err := backend.installAuthorizedKeys(administrator, state, profile); err != nil {
			return err
		}
	}
	return nil
}

type administratorState struct {
	Exists                bool
	MissingGroups         []string
	MissingAuthorizedKeys bool
	Home                  string
	Shell                 string
	UID                   uint32
	GID                   uint32
}

func (backend SystemBackend) observeAdministrator(
	ctx context.Context,
	administrator Administrator,
	profile Profile,
) (administratorState, error) {
	output, err := backend.runRaw(ctx, execx.Spec{
		Program: "id", Arguments: []string{"-u", "--", administrator.Name},
	})
	if err != nil {
		return administratorState{}, err
	}
	if output.ExitCode == 1 {
		return administratorState{}, nil
	}
	if output.ExitCode != 0 {
		return administratorState{}, fmt.Errorf(
			"id exited with %d: %s",
			output.ExitCode,
			strings.TrimSpace(string(output.Stderr)),
		)
	}
	state := administratorState{Exists: true}
	account, err := backend.runRaw(ctx, execx.Spec{
		Program: "getent", Arguments: []string{"passwd", "--", administrator.Name},
	})
	if err != nil {
		return administratorState{}, err
	}
	if account.ExitCode != 0 {
		return administratorState{}, fmt.Errorf(
			"inspect account for %s: getent exited with %d",
			administrator.Name,
			account.ExitCode,
		)
	}
	fields := strings.Split(strings.TrimSpace(string(account.Stdout)), ":")
	if len(fields) != 7 || fields[0] != administrator.Name {
		return administratorState{}, fmt.Errorf(
			"administrator %s has an invalid account record",
			administrator.Name,
		)
	}
	uid, uidErr := strconv.ParseUint(fields[2], 10, 32)
	gid, gidErr := strconv.ParseUint(fields[3], 10, 32)
	state.Home, state.Shell = fields[5], fields[6]
	if !validAdministratorHome(state.Home) || !validLoginShell(state.Shell) {
		return administratorState{}, fmt.Errorf(
			"administrator %s does not have a usable home and login shell",
			administrator.Name,
		)
	}
	if uidErr != nil || gidErr != nil {
		return administratorState{}, fmt.Errorf(
			"administrator %s has invalid numeric identity metadata",
			administrator.Name,
		)
	}
	state.UID, state.GID = uint32(uid), uint32(gid)
	if err := backend.validateAdministratorHome(state); err != nil {
		return administratorState{}, fmt.Errorf(
			"administrator %s does not have a usable home: %w",
			administrator.Name,
			err,
		)
	}
	if len(administrator.Groups) > 0 {
		output, err = backend.runRaw(ctx, execx.Spec{
			Program: "id", Arguments: []string{"-nG", "--", administrator.Name},
		})
		if err != nil {
			return administratorState{}, err
		}
		if output.ExitCode != 0 {
			return administratorState{}, fmt.Errorf(
				"inspect groups for %s: id exited with %d",
				administrator.Name,
				output.ExitCode,
			)
		}
		actual := map[string]bool{}
		for _, group := range strings.Fields(string(output.Stdout)) {
			actual[group] = true
		}
		for _, group := range administrator.Groups {
			if !actual[group] {
				state.MissingGroups = append(state.MissingGroups, group)
			}
		}
		sort.Strings(state.MissingGroups)
	}
	if len(administrator.AuthorizedKeySources) > 0 {
		converged, err := backend.authorizedKeysConverged(administrator, state, profile)
		if err != nil {
			return administratorState{}, err
		}
		state.MissingAuthorizedKeys = !converged
	}
	return state, nil
}

func (backend SystemBackend) installAuthorizedKeys(
	administrator Administrator,
	state administratorState,
	profile Profile,
) error {
	if len(administrator.AuthorizedKeySources) == 0 {
		return nil
	}
	keys, err := backend.desiredAuthorizedKeysForProfile(administrator, profile)
	if err != nil {
		return err
	}
	directory, err := backend.ensureAdministratorSSHDirectory(state)
	if err != nil {
		return err
	}
	target := filepath.Join(directory, "authorized_keys")
	return backend.mergeLines(target, keys, 0o600, state)
}

func (backend SystemBackend) desiredAuthorizedKeys(
	administrator Administrator,
) ([]string, error) {
	keys := []string{}
	for _, source := range administrator.AuthorizedKeySources {
		trustedPath := backend.path(source)
		if err := validateExistingDirectoryChain(
			filepath.Dir(trustedPath),
			backend.Root != "",
		); err != nil {
			return nil, fmt.Errorf("validate authorized key source directory: %w", err)
		}
		file, err := openTrustedKeySource(trustedPath)
		if err != nil {
			return nil, fmt.Errorf("open authorized key source without following links: %w", err)
		}
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 ||
			info.Size() > 1<<20 {
			_ = file.Close()
			return nil, fmt.Errorf("authorized key source %s is unsafe", source)
		}
		if err := validateTrustedKeySource(
			trustedPath,
			info,
			backend.Root != "",
		); err != nil {
			_ = file.Close()
			return nil, err
		}
		content, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		if err != nil {
			_ = file.Close()
			return nil, err
		}
		if len(content) > 1<<20 {
			_ = file.Close()
			return nil, fmt.Errorf("authorized key source %s is oversized", source)
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !validPublicKey(line) {
				return nil, fmt.Errorf("authorized key source %s contains an invalid key", source)
			}
			keys = append(keys, line)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

func (backend SystemBackend) desiredAuthorizedKeysForProfile(
	administrator Administrator,
	profile Profile,
) ([]string, error) {
	if keys, ok := profile.DesiredAuthorizedKeys[administrator.Name]; ok {
		return append([]string(nil), keys...), nil
	}
	return backend.desiredAuthorizedKeys(administrator)
}

func (backend SystemBackend) authorizedKeysConverged(
	administrator Administrator,
	state administratorState,
	profile Profile,
) (bool, error) {
	desired, err := backend.desiredAuthorizedKeysForProfile(administrator, profile)
	if err != nil {
		return false, err
	}
	target := backend.path(state.Home + "/.ssh/authorized_keys")
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Size() > 1<<20 ||
		runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return false, errors.New("authorized_keys target is unsafe")
	}
	if err := validateAdministratorPath(
		target,
		info,
		state.UID,
		false,
		backend.Root != "",
	); err != nil {
		return false, err
	}
	content, err := os.ReadFile(target) // #nosec G304 -- derived fixed user path.
	if err != nil {
		return false, err
	}
	actual := map[string]bool{}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			actual[line] = true
		}
	}
	for _, key := range desired {
		if !actual[key] {
			return false, nil
		}
	}
	return true, nil
}

func validAdministratorHome(home string) bool {
	return path.IsAbs(home) && path.Clean(home) == home && home != "/"
}

func validLoginShell(shell string) bool {
	if !path.IsAbs(shell) || path.Clean(shell) != shell {
		return false
	}
	switch shell {
	case "/bin/false", "/usr/bin/false", "/sbin/nologin", "/usr/sbin/nologin":
		return false
	default:
		return true
	}
}

func (backend SystemBackend) validateAdministratorHome(
	state administratorState,
) error {
	target := backend.path(state.Home)
	if err := validateExistingDirectoryChain(
		filepath.Dir(target),
		backend.Root != "",
	); err != nil {
		return err
	}
	info, err := os.Lstat(target)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("home path is not a safe directory")
	}
	return validateAdministratorPath(
		target,
		info,
		state.UID,
		true,
		backend.Root != "",
	)
}

func validateExistingDirectoryChain(
	directory string,
	allowCurrentOwner bool,
) error {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(absolute, current)
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		if segment == "" {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("directory %s is unsafe", current)
		}
		if err := validateManagedPath(
			current,
			info,
			true,
			allowCurrentOwner,
		); err != nil {
			return err
		}
	}
	return nil
}

func (backend SystemBackend) ensureAdministratorSSHDirectory(
	state administratorState,
) (string, error) {
	if err := backend.validateAdministratorHome(state); err != nil {
		return "", err
	}
	directory := backend.path(state.Home + "/.ssh")
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return "", err
		}
		if err := setAdministratorOwner(
			directory,
			state.UID,
			state.GID,
			backend.Root != "",
		); err != nil {
			return "", err
		}
		info, err = os.Lstat(directory)
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() ||
		runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("administrator .ssh directory is unsafe")
	}
	if err := validateAdministratorPath(
		directory,
		info,
		state.UID,
		true,
		backend.Root != "",
	); err != nil {
		return "", err
	}
	return directory, nil
}

func validPublicKey(value string) bool {
	if len(value) > 16<<10 || strings.ContainsAny(value, "\r\x00") {
		return false
	}
	fields := strings.Fields(value)
	if len(fields) < 2 {
		return false
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(fields[1])
	if err != nil || len(blob) == 0 || len(blob) > 16<<10 {
		return false
	}
	keyType, rest, ok := readSSHWireString(blob)
	if !ok || string(keyType) != fields[0] {
		return false
	}
	switch fields[0] {
	case "ssh-ed25519":
		key, trailing, valid := readSSHWireString(rest)
		return valid && len(key) == 32 && len(trailing) == 0
	case "ssh-rsa":
		exponent, rest, valid := readSSHWireString(rest)
		if !valid || len(exponent) == 0 {
			return false
		}
		modulus, trailing, valid := readSSHWireString(rest)
		return valid && len(modulus) >= 128 && len(trailing) == 0
	case "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521":
		curve, rest, valid := readSSHWireString(rest)
		if !valid || string(curve) != strings.TrimPrefix(fields[0], "ecdsa-sha2-") {
			return false
		}
		point, trailing, valid := readSSHWireString(rest)
		expectedLength := map[string]int{
			"ecdsa-sha2-nistp256": 65,
			"ecdsa-sha2-nistp384": 97,
			"ecdsa-sha2-nistp521": 133,
		}[fields[0]]
		return valid && len(point) == expectedLength && point[0] == 4 &&
			len(trailing) == 0
	default:
		return false
	}
}

func readSSHWireString(input []byte) ([]byte, []byte, bool) {
	if len(input) < 4 {
		return nil, nil, false
	}
	length := binary.BigEndian.Uint32(input[:4])
	if uint64(length) > uint64(len(input)-4) {
		return nil, nil, false
	}
	end := 4 + int(length)
	return input[4:end], input[end:], true
}

func firewallRulesEqual(actual []byte, expected []byte) bool {
	normalize := func(input []byte) string {
		replacer := strings.NewReplacer(
			" ", "", "\t", "", "\r", "", "\n", "",
			";", "",
			"priorityfilter", "priority0",
		)
		return replacer.Replace(string(input))
	}
	return normalize(actual) == normalize(expected)
}

func (backend SystemBackend) desiredFile(item Item, profile Profile) (*managedFile, error) {
	var managed managedFile
	managed.Mode = 0o644
	switch item {
	case ItemShellHistory:
		managed.Path = "/etc/profile.d/ohtools-server-setup-history.sh"
		managed.Content = []byte("HISTSIZE=10000\nHISTFILESIZE=20000\nHISTTIMEFORMAT='%F %T '\nreadonly HISTTIMEFORMAT\nexport HISTSIZE HISTFILESIZE HISTTIMEFORMAT\n")
	case ItemCronPermissions:
		managed.Path = "/etc/cron.d/ohtools-server-setup-permissions"
		managed.Content = []byte("SHELL=/bin/sh\nPATH=/usr/sbin:/usr/bin:/sbin:/bin\n17 * * * * root /usr/bin/find /etc/cron.d -type f -perm /022 -exec /bin/chmod go-w -- {} +\n")
	case ItemSSH:
		if profile.Config.SSHPort < 1 {
			return nil, errors.New("ssh_port is not configured")
		}
		managed.Path = "/etc/ssh/sshd_config.d/60-ohtools-server-setup.conf"
		managed.Content = []byte(fmt.Sprintf(
			"Port %d\nPermitRootLogin prohibit-password\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\n",
			profile.Config.SSHPort,
		))
		managed.Validate = &execx.Spec{Program: "sshd", Arguments: []string{"-t", "-f", "{staged}"}}
		managed.Activation = &execx.Spec{
			Program: "systemctl", Arguments: []string{"reload", "ssh.service"},
		}
	case ItemFail2Ban:
		managed.Path = "/etc/fail2ban/jail.d/ohtools-server-setup.local"
		managed.Content = []byte("[sshd]\nenabled = true\nbackend = systemd\nbantime = 1h\nfindtime = 10m\nmaxretry = 5\n")
		managed.Activation = &execx.Spec{
			Program: "systemctl", Arguments: []string{"reload", "fail2ban.service"},
		}
	case ItemTimeSync:
		managed.Path = "/etc/systemd/timesyncd.conf.d/60-ohtools-server-setup.conf"
		managed.Content = []byte("[Time]\nNTP=pool.ntp.org\nFallbackNTP=time.cloudflare.com\n")
		managed.Activation = &execx.Spec{
			Program: "systemctl", Arguments: []string{"restart", "systemd-timesyncd.service"},
		}
	case ItemSecurityUpdates:
		managed.Path = "/etc/apt/apt.conf.d/52ohtools-security-updates"
		managed.Content = []byte("APT::Periodic::Enable \"1\";\nAPT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n")
	case ItemLogging:
		managed.Path = "/etc/systemd/journald.conf.d/60-ohtools-server-setup.conf"
		managed.Content = []byte("[Journal]\nStorage=persistent\nCompress=yes\nSeal=yes\n")
		managed.Activation = &execx.Spec{
			Program: "systemctl", Arguments: []string{"restart", "systemd-journald.service"},
		}
	case ItemAuditd:
		managed.Path = "/etc/audit/rules.d/ohtools-server-setup.rules"
		managed.Content = []byte("-w /etc/passwd -p wa -k identity\n-w /etc/group -p wa -k identity\n-w /etc/sudoers -p wa -k scope\n")
		managed.Activation = &execx.Spec{Program: "augenrules", Arguments: []string{"--load"}}
	case ItemFirewall:
		if !profile.Config.ManageFirewall {
			return nil, errors.New("firewall management is not enabled")
		}
		managed.Path = "/etc/nftables.d/ohtools-server-setup.nft"
		managed.Content = []byte(fmt.Sprintf(
			"table inet ohtools_server_setup {\n chain input { type filter hook input priority 0; policy accept; tcp dport %d accept; }\n}\n",
			profile.Config.SSHPort,
		))
		managed.Validate = &execx.Spec{Program: "nft", Arguments: []string{"-c", "-f", "{staged}"}}
		managed.Activation = &execx.Spec{Program: "nft", Arguments: []string{"-f", "{target}"}}
	case ItemSysctl:
		managed.Path = "/etc/sysctl.d/60-ohtools-server-setup.conf"
		managed.Content = []byte("kernel.kptr_restrict = 2\nkernel.dmesg_restrict = 1\nfs.protected_hardlinks = 1\nfs.protected_symlinks = 1\n")
		managed.Activation = &execx.Spec{Program: "sysctl", Arguments: []string{"--system"}}
	case ItemZabbix:
		if profile.Config.Zabbix == nil || !profile.Config.Zabbix.Enabled {
			return nil, errors.New("zabbix management is not enabled")
		}
		managed.Path = "/etc/zabbix/zabbix_agent2.d/ohtools-server-setup.conf"
		managed.Content = []byte(fmt.Sprintf(
			"Server=%s\nServerActive=%s\nHostname=%s\n",
			profile.Config.Zabbix.Server,
			profile.Config.Zabbix.Server,
			profile.Config.Zabbix.Hostname,
		))
		managed.Activation = &execx.Spec{
			Program: "systemctl", Arguments: []string{"restart", "zabbix-agent2.service"},
		}
	default:
		return nil, fmt.Errorf("setup item %q has no managed-file implementation", item)
	}
	return &managed, nil
}

func (backend SystemBackend) fileConverged(managed managedFile) (bool, error) {
	target := backend.path(managed.Path)
	recoverable, err := backend.managedRecoveryPending(target)
	if err != nil {
		return false, err
	}
	if recoverable {
		return false, nil
	}
	return backend.managedFileContentConverged(managed)
}

func (backend SystemBackend) managedFileContentConverged(
	managed managedFile,
) (bool, error) {
	target := backend.path(managed.Path)
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return false, fmt.Errorf("managed file %s is unsafe", managed.Path)
	}
	if err := validateManagedPath(
		target,
		info,
		false,
		backend.Root != "",
	); err != nil {
		return false, err
	}
	content, err := os.ReadFile(target) // #nosec G304 -- fixed managed path.
	if err != nil {
		return false, err
	}
	modeMatches := runtime.GOOS == "windows" || info.Mode().Perm() == managed.Mode
	return bytes.Equal(content, managed.Content) && modeMatches, nil
}

func (backend SystemBackend) activateManagedFile(
	ctx context.Context,
	managed managedFile,
) error {
	return backend.activateManagedFileWithPostVerify(ctx, managed, nil)
}

func (backend SystemBackend) activateManagedFileWithPostVerify(
	ctx context.Context,
	managed managedFile,
	postVerify func() error,
) (returnErr error) {
	target := backend.path(managed.Path)
	if err := backend.recoverManagedTransaction(
		ctx,
		target,
		managed.Activation,
	); err != nil {
		return err
	}
	if err := secureMkdirAll(
		filepath.Dir(target),
		0o755,
		backend.Root != "",
	); err != nil {
		return err
	}
	hadTarget := false
	if info, err := os.Lstat(target); err == nil {
		hadTarget = true
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("managed path %s is unsafe", managed.Path)
		}
		if err := validateManagedPath(
			target,
			info,
			false,
			backend.Root != "",
		); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	staged, err := os.OpenFile(
		target+".stage", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600,
	)
	if err != nil {
		return fmt.Errorf("create staged file: %w", err)
	}
	stagedPath := staged.Name()
	defer func() {
		if cleanupErr := os.Remove(stagedPath); cleanupErr != nil &&
			!errors.Is(cleanupErr, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()
	if _, err := staged.Write(managed.Content); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Sync(); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Chmod(managed.Mode); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Sync(); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return err
	}
	journal := managedTransactionJournal{
		SchemaVersion: "1",
		Phase:         "staged",
		HadTarget:     hadTarget,
	}
	if err := writeManagedTransactionJournal(target, journal); err != nil {
		return err
	}
	if managed.Validate != nil {
		spec := expandManagedSpec(*managed.Validate, stagedPath, target)
		if _, err := backend.run(ctx, spec); err != nil {
			return fmt.Errorf("validate %s: %w", managed.Path, err)
		}
	}
	backup := target + ".rollback"
	if hadTarget {
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("backup %s: %w", managed.Path, err)
		}
		if err := syncDirectory(filepath.Dir(target)); err != nil {
			return errors.Join(
				err,
				backend.recoverManagedTransaction(ctx, target, managed.Activation),
			)
		}
		journal.Phase = "backed_up"
		if err := writeManagedTransactionJournal(target, journal); err != nil {
			return errors.Join(
				err,
				backend.recoverManagedTransaction(ctx, target, managed.Activation),
			)
		}
	}
	if err := os.Rename(stagedPath, target); err != nil {
		return errors.Join(
			fmt.Errorf("activate %s: %w", managed.Path, err),
			backend.recoverManagedTransaction(ctx, target, managed.Activation),
		)
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return backend.rollbackManaged(ctx, target, backup, hadTarget, err, nil)
	}
	journal.Phase = "activated"
	if err := writeManagedTransactionJournal(target, journal); err != nil {
		return backend.rollbackManaged(ctx, target, backup, hadTarget, err, nil)
	}
	if managed.Activation != nil {
		spec := expandManagedSpec(*managed.Activation, stagedPath, target)
		if _, err := backend.run(ctx, spec); err != nil {
			return backend.rollbackManaged(
				ctx,
				target,
				backup,
				hadTarget,
				err,
				managed.Activation,
			)
		}
	}
	if postVerify != nil {
		if err := postVerify(); err != nil {
			return backend.rollbackManaged(
				ctx,
				target,
				backup,
				hadTarget,
				err,
				managed.Activation,
			)
		}
	}
	journal.Phase = "verified"
	if err := writeManagedTransactionJournal(target, journal); err != nil {
		return backend.rollbackManaged(
			ctx,
			target,
			backup,
			hadTarget,
			err,
			managed.Activation,
		)
	}
	if hadTarget {
		if err := os.Remove(backup); err != nil {
			return fmt.Errorf("remove rollback file: %w", err)
		}
		if err := syncDirectory(filepath.Dir(target)); err != nil {
			return err
		}
	}
	return removeManagedTransactionJournal(target)
}

func ensureNoManagedTransactionArtifacts(target string) error {
	for _, suffix := range []string{
		".stage",
		".rollback",
		".transaction-v1.json",
		".transaction-v1.json.stage",
	} {
		artifact := target + suffix
		if _, err := os.Lstat(artifact); err == nil {
			return fmt.Errorf(
				"stale managed transaction artifact %s requires recovery",
				artifact,
			)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (backend SystemBackend) managedRecoveryPending(target string) (bool, error) {
	journalTempExists, err := backend.trustedRecoveryArtifactExists(
		target + ".transaction-v1.json.stage",
	)
	if err != nil {
		return false, err
	}
	journalPath := target + ".transaction-v1.json"
	journalExists, err := backend.trustedRecoveryArtifactExists(journalPath)
	if err != nil {
		return false, err
	}
	stageExists, err := backend.trustedRecoveryArtifactExists(target + ".stage")
	if err != nil {
		return false, err
	}
	backupExists, err := backend.trustedRecoveryArtifactExists(target + ".rollback")
	if err != nil {
		return false, err
	}
	if !journalExists {
		return journalTempExists || stageExists || backupExists, nil
	}
	journal, err := backend.readManagedTransactionJournal(journalPath)
	if err != nil {
		return false, err
	}
	targetExists, err := backend.trustedRecoveryArtifactExists(target)
	if err != nil {
		return false, err
	}
	if err := validateManagedRecoveryState(
		journal,
		targetExists,
		stageExists,
		backupExists,
	); err != nil {
		return false, err
	}
	return true, nil
}

func validateManagedRecoveryState(
	journal managedTransactionJournal,
	targetExists bool,
	stageExists bool,
	backupExists bool,
) error {
	switch journal.Phase {
	case "staged":
		if journal.HadTarget {
			beforeBackup := targetExists && stageExists && !backupExists
			afterBackup := !targetExists && stageExists && backupExists
			if !beforeBackup && !afterBackup {
				return errors.New("managed staged transaction state is incomplete")
			}
			return nil
		}
		beforeActivation := !targetExists && stageExists && !backupExists
		afterActivation := targetExists && !stageExists && !backupExists
		if !beforeActivation && !afterActivation {
			return errors.New("managed staged transaction state is incomplete")
		}
	case "backed_up":
		beforeActivation := !targetExists && stageExists && backupExists
		afterActivation := targetExists && !stageExists && backupExists
		if !journal.HadTarget || !beforeActivation && !afterActivation {
			return errors.New("managed transaction rollback file is missing")
		}
	case "activated":
		if !targetExists || stageExists ||
			journal.HadTarget && !backupExists ||
			!journal.HadTarget && backupExists {
			return errors.New("managed activated transaction state is incomplete")
		}
	case "verified":
		if !targetExists || stageExists ||
			!journal.HadTarget && backupExists {
			return errors.New("verified managed transaction target is missing")
		}
	case "rolling_back":
		if stageExists || !journal.HadTarget && backupExists {
			return errors.New("managed rollback has an unexpected backup file")
		}
		if journal.HadTarget && !backupExists && !targetExists {
			return errors.New("managed rollback original target is missing")
		}
	default:
		return errors.New("invalid managed transaction journal phase")
	}
	return nil
}

func (backend SystemBackend) rollbackManaged(
	ctx context.Context,
	target string,
	backup string,
	hadTarget bool,
	cause error,
	activation *execx.Spec,
) error {
	journalErr := writeManagedTransactionJournal(target, managedTransactionJournal{
		SchemaVersion: "1",
		Phase:         "rolling_back",
		HadTarget:     hadTarget,
	})
	if journalErr != nil {
		return errors.Join(
			fmt.Errorf("activate managed file: %w", cause),
			fmt.Errorf("record managed rollback: %w", journalErr),
		)
	}
	removeErr := os.Remove(target)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	removeSyncErr := error(nil)
	if removeErr == nil {
		removeSyncErr = syncDirectory(filepath.Dir(target))
	}
	var restoreErr error
	if hadTarget && removeSyncErr == nil {
		restoreErr = os.Rename(backup, target)
	}
	syncErr := syncDirectory(filepath.Dir(target))
	var activationErr error
	if removeErr == nil && removeSyncErr == nil &&
		restoreErr == nil && syncErr == nil && activation != nil {
		spec := expandManagedSpec(*activation, "", target)
		_, activationErr = backend.runCleanup(ctx, spec)
	}
	var removeJournalErr error
	if removeErr == nil && removeSyncErr == nil &&
		restoreErr == nil && syncErr == nil && activationErr == nil {
		removeJournalErr = removeManagedTransactionJournal(target)
	}
	return errors.Join(
		fmt.Errorf("activate managed file: %w", cause),
		removeErr,
		removeSyncErr,
		restoreErr,
		syncErr,
		activationErr,
		removeJournalErr,
	)
}

func writeManagedTransactionJournal(
	target string,
	journal managedTransactionJournal,
) error {
	encoded, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return writeFileAtomic(target+".transaction-v1.json", encoded, 0o600)
}

func removeManagedTransactionJournal(target string) error {
	journal := target + ".transaction-v1.json"
	if err := os.Remove(journal); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func (backend SystemBackend) recoverManagedTransaction(
	ctx context.Context,
	target string,
	activation *execx.Spec,
) error {
	journalPath := target + ".transaction-v1.json"
	journalTemp := journalPath + ".stage"
	if err := backend.removeTrustedRecoveryArtifact(journalTemp); err != nil {
		return err
	}
	info, err := os.Lstat(journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return backend.recoverLegacyManagedArtifacts(target)
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Size() > 4096 ||
		runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		return errors.New("managed transaction journal is unsafe")
	}
	if err := validateManagedPath(
		journalPath,
		info,
		false,
		backend.Root != "",
	); err != nil {
		return err
	}
	journal, err := backend.readManagedTransactionJournal(journalPath)
	if err != nil {
		return err
	}
	targetExists, err := backend.trustedRecoveryArtifactExists(target)
	if err != nil {
		return err
	}
	stageExists, err := backend.trustedRecoveryArtifactExists(target + ".stage")
	if err != nil {
		return err
	}
	backupExists, err := backend.trustedRecoveryArtifactExists(target + ".rollback")
	if err != nil {
		return err
	}
	if err := validateManagedRecoveryState(
		journal,
		targetExists,
		stageExists,
		backupExists,
	); err != nil {
		return err
	}
	if journal.Phase == "verified" {
		if !targetExists {
			return errors.New("verified managed transaction target is missing")
		}
		if err := backend.removeTrustedRecoveryArtifact(target + ".stage"); err != nil {
			return err
		}
		if err := backend.removeTrustedRecoveryArtifact(target + ".rollback"); err != nil {
			return err
		}
		return removeManagedTransactionJournal(target)
	}
	restoreEffectiveState := activation != nil &&
		(journal.Phase == "activated" || journal.Phase == "rolling_back")
	if journal.Phase != "rolling_back" {
		journal.Phase = "rolling_back"
		if err := writeManagedTransactionJournal(target, journal); err != nil {
			return fmt.Errorf("record recovered managed rollback: %w", err)
		}
	}
	if err := backend.rollbackInterruptedManagedTransaction(target, journal); err != nil {
		return fmt.Errorf("restore recovered managed file: %w", err)
	}
	if restoreEffectiveState {
		spec := expandManagedSpec(*activation, "", target)
		if _, err := backend.runCleanup(ctx, spec); err != nil {
			return fmt.Errorf("restore recovered effective state: %w", err)
		}
	}
	return removeManagedTransactionJournal(target)
}

func (backend SystemBackend) readManagedTransactionJournal(
	journalPath string,
) (managedTransactionJournal, error) {
	content, err := os.ReadFile(journalPath) // #nosec G304 -- derived trusted path.
	if err != nil {
		return managedTransactionJournal{}, err
	}
	var journal managedTransactionJournal
	if err := strictjson.Decode(content, &journal); err != nil {
		return managedTransactionJournal{}, fmt.Errorf(
			"decode managed transaction journal: %w",
			err,
		)
	}
	if journal.SchemaVersion != "1" {
		return managedTransactionJournal{}, errors.New(
			"unsupported managed transaction journal schema",
		)
	}
	switch journal.Phase {
	case "staged", "backed_up", "activated", "verified", "rolling_back":
	default:
		return managedTransactionJournal{}, errors.New(
			"invalid managed transaction journal phase",
		)
	}
	if journal.Phase == "backed_up" && !journal.HadTarget {
		return managedTransactionJournal{}, errors.New(
			"managed transaction journal has an impossible state",
		)
	}
	return journal, nil
}

func (backend SystemBackend) rollbackInterruptedManagedTransaction(
	target string,
	journal managedTransactionJournal,
) error {
	stage := target + ".stage"
	backup := target + ".rollback"
	backupExists, err := backend.trustedRecoveryArtifactExists(backup)
	if err != nil {
		return err
	}
	targetExists, err := backend.trustedRecoveryArtifactExists(target)
	if err != nil {
		return err
	}
	if journal.Phase == "rolling_back" {
		if !journal.HadTarget && backupExists {
			return errors.New("managed rollback has an unexpected backup file")
		}
		if journal.HadTarget && !backupExists && !targetExists {
			return errors.New("managed rollback original target is missing")
		}
		if !journal.HadTarget && targetExists {
			if err := os.Remove(target); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(target)); err != nil {
				return err
			}
		}
	}
	if backupExists {
		if targetExists {
			if err := os.Remove(target); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(target)); err != nil {
				return err
			}
		}
		if err := os.Rename(backup, target); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(target)); err != nil {
			return err
		}
	} else if journal.HadTarget {
		if journal.Phase == "backed_up" || journal.Phase == "activated" {
			return errors.New("managed transaction rollback file is missing")
		}
		if journal.Phase == "staged" && !targetExists {
			return errors.New("managed transaction original target is missing")
		}
	} else if (journal.Phase == "staged" || journal.Phase == "activated") &&
		targetExists {
		if err := os.Remove(target); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(target)); err != nil {
			return err
		}
	}
	return backend.removeTrustedRecoveryArtifact(stage)
}

func (backend SystemBackend) recoverLegacyManagedArtifacts(target string) error {
	backup := target + ".rollback"
	backupExists, err := backend.trustedRecoveryArtifactExists(backup)
	if err != nil {
		return err
	}
	if backupExists {
		targetExists, err := backend.trustedRecoveryArtifactExists(target)
		if err != nil {
			return err
		}
		if targetExists {
			if err := os.Remove(target); err != nil {
				return err
			}
			if err := syncDirectory(filepath.Dir(target)); err != nil {
				return err
			}
		}
		if err := os.Rename(backup, target); err != nil {
			return err
		}
		if err := syncDirectory(filepath.Dir(target)); err != nil {
			return err
		}
	}
	return backend.removeTrustedRecoveryArtifact(target + ".stage")
}

func (backend SystemBackend) trustedRecoveryArtifactExists(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("managed recovery artifact %s is unsafe", path)
	}
	if err := validateManagedPath(
		path,
		info,
		false,
		backend.Root != "",
	); err != nil {
		return false, err
	}
	return true, nil
}

func (backend SystemBackend) removeTrustedRecoveryArtifact(path string) error {
	exists, err := backend.trustedRecoveryArtifactExists(path)
	if err != nil || !exists {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func expandManagedSpec(spec execx.Spec, staged string, target string) execx.Spec {
	output := spec
	output.Arguments = append([]string(nil), spec.Arguments...)
	for index, value := range output.Arguments {
		switch value {
		case "{staged}":
			output.Arguments[index] = staged
		case "{target}":
			output.Arguments[index] = target
		}
	}
	return output
}

func (backend SystemBackend) mergeLines(
	target string,
	additions []string,
	mode fs.FileMode,
	state administratorState,
) error {
	existing := []byte{}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return errors.New("authorized_keys target is unsafe")
		}
		if err := validateAdministratorPath(
			target,
			info,
			state.UID,
			false,
			backend.Root != "",
		); err != nil {
			return err
		}
		existing, err = os.ReadFile(target) // #nosec G304 -- derived fixed user path.
		if err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	seen := map[string]bool{}
	lines := []string{}
	for _, line := range append(strings.Split(string(existing), "\n"), additions...) {
		line = strings.TrimSpace(line)
		if line != "" && !seen[line] {
			seen[line] = true
			lines = append(lines, line)
		}
	}
	content := []byte(strings.Join(lines, "\n") + "\n")
	return writeFileAtomicForAdministrator(
		target,
		content,
		mode,
		state.UID,
		state.GID,
		backend.Root != "",
	)
}

func writeFileAtomicForAdministrator(
	target string,
	content []byte,
	mode fs.FileMode,
	uid uint32,
	gid uint32,
	allowCurrentOwner bool,
) (returnErr error) {
	file, err := os.OpenFile(target+".stage", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	staged := file.Name()
	defer func() {
		if cleanupErr := os.Remove(staged); cleanupErr != nil &&
			!errors.Is(cleanupErr, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if err := setOpenFileAdministratorOwner(
		file,
		uid,
		gid,
		allowCurrentOwner,
	); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := replaceFile(staged, target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func writeFileAtomic(target string, content []byte, mode fs.FileMode) (returnErr error) {
	file, err := os.OpenFile(target+".stage", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	staged := file.Name()
	defer func() {
		if cleanupErr := os.Remove(staged); cleanupErr != nil &&
			!errors.Is(cleanupErr, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, cleanupErr)
		}
	}()
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := replaceFile(staged, target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func secureMkdirAll(
	directory string,
	mode fs.FileMode,
	allowCurrentOwner ...bool,
) error {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(absolute)
	current := volume + string(filepath.Separator)
	relative := strings.TrimPrefix(absolute, current)
	allowCurrent := len(allowCurrentOwner) > 0 && allowCurrentOwner[0]
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		if segment == "" {
			continue
		}
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, mode); err != nil {
				return err
			}
			info, err = os.Lstat(current)
			if err != nil {
				return err
			}
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("directory %s is unsafe", current)
		}
		if err := validateManagedPath(current, info, true, allowCurrent); err != nil {
			return err
		}
	}
	return nil
}

func (backend SystemBackend) hasDebianELTS() (bool, error) {
	paths := []string{backend.path("/etc/apt/sources.list")}
	matches, err := filepath.Glob(backend.path("/etc/apt/sources.list.d/*"))
	if err != nil {
		return false, err
	}
	paths = append(paths, matches...)
	for _, source := range paths {
		info, err := os.Lstat(source)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			info.Size() > 1<<20 {
			continue
		}
		content, err := os.ReadFile(source) // #nosec G304 -- bounded APT source path.
		if err != nil {
			return false, err
		}
		text := strings.ToLower(string(content))
		if strings.Contains(text, "deb.freexian.com/extended-lts") &&
			strings.Contains(text, "buster-lts") {
			return true, nil
		}
	}
	return false, nil
}

func (backend SystemBackend) run(ctx context.Context, spec execx.Spec) (execx.Output, error) {
	output, err := backend.runRaw(ctx, spec)
	if err != nil {
		return output, err
	}
	if output.ExitCode != 0 {
		return output, fmt.Errorf(
			"%s exited with %d: %s",
			spec.Program,
			output.ExitCode,
			strings.TrimSpace(string(output.Stderr)),
		)
	}
	return output, nil
}

func (backend SystemBackend) runRaw(ctx context.Context, spec execx.Spec) (execx.Output, error) {
	if backend.Runner == nil {
		return execx.Output{}, execx.ErrNotFound
	}
	if spec.StdoutLimit == 0 {
		spec.StdoutLimit = 10 << 20
	}
	if spec.StderrLimit == 0 {
		spec.StderrLimit = 1 << 20
	}
	output, err := backend.Runner.Run(ctx, spec)
	if err != nil {
		return output, err
	}
	if output.StdoutTruncated || output.StderrTruncated {
		return output, fmt.Errorf("%s output exceeded its limit", spec.Program)
	}
	return output, nil
}

func (backend SystemBackend) runCleanup(
	ctx context.Context,
	spec execx.Spec,
) (execx.Output, error) {
	cleanupContext, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		backend.cleanupTimeout(),
	)
	defer cancel()
	return backend.run(cleanupContext, spec)
}

func (backend SystemBackend) runRawCleanup(
	ctx context.Context,
	spec execx.Spec,
) (execx.Output, error) {
	cleanupContext, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		backend.cleanupTimeout(),
	)
	defer cancel()
	return backend.runRaw(cleanupContext, spec)
}

func (backend SystemBackend) cleanupTimeout() time.Duration {
	if backend.rollbackTimeout > 0 {
		return backend.rollbackTimeout
	}
	return rollbackCommandTimeout
}

func (backend SystemBackend) path(absolute string) string {
	if backend.Root == "" {
		return filepath.FromSlash(absolute)
	}
	return filepath.Join(backend.Root, filepath.FromSlash(strings.TrimPrefix(absolute, "/")))
}
