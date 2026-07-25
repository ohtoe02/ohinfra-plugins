package serversetup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
)

type SystemBackend struct {
	Root   string
	Runner execx.Runner
}

type managedFile struct {
	Path       string
	Content    []byte
	Mode       fs.FileMode
	Validate   *execx.Spec
	Activation *execx.Spec
}

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
		return backend.observeUsers(profile)
	default:
		managed, err := backend.desiredFile(item, profile)
		if err != nil {
			return Observation{}, err
		}
		if managed == nil {
			return Observation{Converged: true, Summary: string(item) + " is disabled"}, nil
		}
		converged, err := backend.fileConverged(*managed)
		if err != nil {
			return Observation{}, err
		}
		summary := string(item) + " configuration differs from the desired state"
		if converged {
			summary = string(item) + " configuration matches the desired state"
		}
		return Observation{Converged: converged, Summary: summary}, nil
	}
}

func (backend SystemBackend) Apply(
	ctx context.Context,
	item Item,
	profile Profile,
) error {
	switch item {
	case ItemPackages:
		return backend.applyPackages(ctx, profile)
	case ItemUsers:
		return backend.applyUsers(ctx, profile)
	default:
		managed, err := backend.desiredFile(item, profile)
		if err != nil {
			return err
		}
		if managed == nil {
			return nil
		}
		return backend.activateManagedFile(ctx, *managed)
	}
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

func (backend SystemBackend) applyPackages(ctx context.Context, profile Profile) error {
	packages := desiredPackages(profile)
	if len(packages) == 0 {
		return nil
	}
	environment := map[string]string{"DEBIAN_FRONTEND": "noninteractive"}
	if _, err := backend.run(ctx, execx.Spec{
		Program: "apt-get", Arguments: []string{"update"}, Environment: environment,
	}); err != nil {
		return err
	}
	arguments := []string{"install", "-y", "--no-install-recommends", "--"}
	arguments = append(arguments, packages...)
	_, err := backend.run(ctx, execx.Spec{
		Program: "apt-get", Arguments: arguments, Environment: environment,
	})
	return err
}

func desiredPackages(profile Profile) []string {
	set := map[string]bool{}
	for _, name := range profile.Config.Packages {
		set[name] = true
	}
	for _, item := range profile.Items {
		switch item {
		case ItemUsers:
			set["sudo"] = true
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

func (backend SystemBackend) observeUsers(profile Profile) (Observation, error) {
	passwd, err := os.ReadFile(backend.path("/etc/passwd"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Observation{}, fmt.Errorf("read passwd: %w", err)
	}
	missing := []string{}
	for _, administrator := range profile.Config.Administrators {
		marker := []byte("\n" + administrator.Name + ":")
		if !bytes.HasPrefix(passwd, []byte(administrator.Name+":")) &&
			!bytes.Contains(passwd, marker) {
			missing = append(missing, administrator.Name)
		}
	}
	return Observation{
		Converged: len(missing) == 0,
		Summary: fmt.Sprintf(
			"%d administrator account(s), %d missing",
			len(profile.Config.Administrators), len(missing),
		),
		Details: map[string]any{"missing": missing},
	}, nil
}

func (backend SystemBackend) applyUsers(ctx context.Context, profile Profile) error {
	for _, administrator := range profile.Config.Administrators {
		output, err := backend.runRaw(ctx, execx.Spec{
			Program: "id", Arguments: []string{"-u", "--", administrator.Name},
		})
		if err != nil {
			return err
		}
		if output.ExitCode != 0 {
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
		}
		if err := backend.installAuthorizedKeys(administrator); err != nil {
			return err
		}
	}
	return nil
}

func (backend SystemBackend) installAuthorizedKeys(administrator Administrator) error {
	if len(administrator.AuthorizedKeySources) == 0 {
		return nil
	}
	keys := []string{}
	for _, source := range administrator.AuthorizedKeySources {
		info, err := os.Lstat(source)
		if err != nil {
			return fmt.Errorf("inspect authorized key source: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			info.Mode().Perm()&0o022 != 0 || info.Size() > 1<<20 {
			return fmt.Errorf("authorized key source %s is unsafe", source)
		}
		content, err := os.ReadFile(source) // #nosec G304 -- validated fixed config path.
		if err != nil {
			return err
		}
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if !validPublicKey(line) {
				return fmt.Errorf("authorized key source %s contains an invalid key", source)
			}
			keys = append(keys, line)
		}
	}
	sort.Strings(keys)
	target := backend.path("/home/" + administrator.Name + "/.ssh/authorized_keys")
	return backend.mergeLines(target, keys, 0o600)
}

func validPublicKey(value string) bool {
	if len(value) > 16<<10 || strings.ContainsAny(value, "\r\x00") {
		return false
	}
	fields := strings.Fields(value)
	if len(fields) < 2 {
		return false
	}
	switch fields[0] {
	case "ssh-ed25519", "ssh-rsa", "ecdsa-sha2-nistp256",
		"ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521":
		return true
	default:
		return false
	}
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
) (returnErr error) {
	target := backend.path(managed.Path)
	if err := secureMkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("managed path %s is unsafe", managed.Path)
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
			!errors.Is(cleanupErr, os.ErrNotExist) && returnErr == nil {
			returnErr = cleanupErr
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
	if err := staged.Close(); err != nil {
		return err
	}
	if managed.Validate != nil {
		spec := expandManagedSpec(*managed.Validate, stagedPath, target)
		if _, err := backend.run(ctx, spec); err != nil {
			return fmt.Errorf("validate %s: %w", managed.Path, err)
		}
	}
	backup := target + ".rollback"
	hadTarget := false
	if _, err := os.Lstat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return fmt.Errorf("backup %s: %w", managed.Path, err)
		}
		hadTarget = true
	}
	if err := os.Rename(stagedPath, target); err != nil {
		if hadTarget {
			_ = os.Rename(backup, target)
		}
		return fmt.Errorf("activate %s: %w", managed.Path, err)
	}
	if err := syncDirectory(filepath.Dir(target)); err != nil {
		return backend.rollbackManaged(target, backup, hadTarget, err)
	}
	if managed.Activation != nil {
		spec := expandManagedSpec(*managed.Activation, stagedPath, target)
		if _, err := backend.run(ctx, spec); err != nil {
			return backend.rollbackManaged(target, backup, hadTarget, err)
		}
	}
	if hadTarget {
		if err := os.Remove(backup); err != nil {
			return fmt.Errorf("remove rollback file: %w", err)
		}
		if err := syncDirectory(filepath.Dir(target)); err != nil {
			return err
		}
	}
	return nil
}

func (backend SystemBackend) rollbackManaged(
	target string,
	backup string,
	hadTarget bool,
	cause error,
) error {
	removeErr := os.Remove(target)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	var restoreErr error
	if hadTarget {
		restoreErr = os.Rename(backup, target)
	}
	syncErr := syncDirectory(filepath.Dir(target))
	return errors.Join(
		fmt.Errorf("activate managed file: %w", cause),
		removeErr,
		restoreErr,
		syncErr,
	)
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

func (backend SystemBackend) mergeLines(target string, additions []string, mode fs.FileMode) error {
	if err := secureMkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	existing := []byte{}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("authorized_keys target is unsafe")
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
	return writeFileAtomic(target, content, mode)
}

func writeFileAtomic(target string, content []byte, mode fs.FileMode) (returnErr error) {
	file, err := os.OpenFile(target+".stage", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	staged := file.Name()
	defer func() {
		if cleanupErr := os.Remove(staged); cleanupErr != nil &&
			!errors.Is(cleanupErr, os.ErrNotExist) && returnErr == nil {
			returnErr = cleanupErr
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
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(staged, target); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(target))
}

func secureMkdirAll(directory string, mode fs.FileMode) error {
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
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, mode); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("directory %s is unsafe", current)
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

func (backend SystemBackend) path(absolute string) string {
	if backend.Root == "" {
		return filepath.FromSlash(absolute)
	}
	return filepath.Join(backend.Root, filepath.FromSlash(strings.TrimPrefix(absolute, "/")))
}
