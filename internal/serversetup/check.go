package serversetup

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ohtoe02/ohtools-plugins/internal/execx"
	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/resultbuilder"
)

type Checker struct {
	Root        string
	Runner      execx.Runner
	Host        string
	Now         func() time.Time
	Tool        protocol.Tool
	Identity    FileIdentityReader
	Confinement PathConfinement
}

func (checker Checker) Run(
	ctx context.Context,
	profile Profile,
	settings Config,
) (protocol.Result, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	now := checker.now()
	local := probe.Local{Root: checker.Root, Runner: checker.Runner}
	checks := make([]protocol.Check, 0,
		len(profile.Packages)+len(profile.Groups)+len(profile.Users)+
			len(profile.Directories)+len(profile.ManagedFiles)+len(profile.Sysctls)+len(profile.Units))
	failures := []protocol.StructuredError{}

	packageChecks, failure := checkPackages(ctx, local, profile.Packages)
	if err := fatalContextFailure(ctx, failure); err != nil {
		return protocol.Result{}, err
	}
	checks = append(checks, packageChecks...)
	if failure != nil {
		failures = append(failures, *failure)
	}

	groupChecks, userChecks, accounts, accountFailures := checkAccounts(
		local, profile.Groups, profile.Users, settings.AccountFileLimitBytes,
	)
	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	checks = append(checks, groupChecks...)
	checks = append(checks, userChecks...)
	failures = append(failures, accountFailures...)

	identity := checker.Identity
	if identity == nil {
		identity = systemFileIdentityReader{}
	}
	for _, directory := range profile.Directories {
		if err := ctx.Err(); err != nil {
			return protocol.Result{}, err
		}
		check, failure := checker.checkDirectory(directory, accounts, identity)
		checks = append(checks, check)
		if failure != nil {
			failures = append(failures, *failure)
		}
	}
	for _, file := range profile.ManagedFiles {
		if err := ctx.Err(); err != nil {
			return protocol.Result{}, err
		}
		check, failure := checkManagedFile(
			local, checker.Root, file, settings.ManagedFileLimitBytes, accounts, identity,
		)
		checks = append(checks, check)
		if failure != nil {
			failures = append(failures, *failure)
		}
	}
	for _, sysctl := range profile.Sysctls {
		if err := ctx.Err(); err != nil {
			return protocol.Result{}, err
		}
		check, failure := checkSysctl(local, sysctl)
		checks = append(checks, check)
		if failure != nil {
			failures = append(failures, *failure)
		}
	}
	for _, unit := range profile.Units {
		if err := ctx.Err(); err != nil {
			return protocol.Result{}, err
		}
		check, failure := checkUnit(ctx, local, unit)
		if err := fatalContextFailure(ctx, failure); err != nil {
			return protocol.Result{}, err
		}
		checks = append(checks, check)
		if failure != nil {
			failures = append(failures, *failure)
		}
	}

	return resultbuilder.Build(resultbuilder.Input{
		Command: "setup check", Tool: checker.Tool, Host: checker.Host,
		Started: now, Now: now, Checks: checks,
		Data: map[string]any{
			"profile": profile.ID,
		},
		Errors: failures,
	}), nil
}

func fatalContextFailure(
	ctx context.Context,
	failure *protocol.StructuredError,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if failure == nil {
		return nil
	}
	switch failure.Kind {
	case protocol.ErrorCancelled:
		return context.Canceled
	case protocol.ErrorTimeout:
		return context.DeadlineExceeded
	default:
		return nil
	}
}

func checkPackages(
	ctx context.Context,
	local probe.Local,
	packages []string,
) ([]protocol.Check, *protocol.StructuredError) {
	arguments := []string{"-W", "-f=${binary:Package}\t${db:Status-Status}\n", "--"}
	arguments = append(arguments, packages...)
	output, err := local.Run(ctx, probe.Command{
		Program: "dpkg-query", Arguments: arguments,
		StdoutLimit: 1 << 20, StderrLimit: 64 << 10,
	})
	if err != nil {
		failure := inspectionFailure("dpkg-query", "package_inspection_failed", err)
		return skippedChecks("package:", packages, "package state could not be inspected"), &failure
	}

	installed := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(string(output.Stdout)))
	for scanner.Scan() {
		name, status, found := strings.Cut(scanner.Text(), "\t")
		if found && name != "" {
			installed[name] = strings.TrimSpace(status) == "installed"
		}
	}
	if scanErr := scanner.Err(); scanErr != nil {
		failure := inspectionFailure("dpkg-query", "package_output_invalid", scanErr)
		return skippedChecks("package:", packages, "package state could not be inspected"), &failure
	}

	checks := make([]protocol.Check, 0, len(packages))
	for _, name := range packages {
		status := protocol.StatusPass
		summary := fmt.Sprintf("package %s is installed", name)
		if !installed[name] {
			status = protocol.StatusWarning
			summary = fmt.Sprintf("package %s is not installed", name)
		}
		checks = append(checks, protocol.Check{
			ID: "package:" + name, Status: status, Summary: summary,
		})
	}
	return checks, nil
}

type accountEntry struct {
	id           int
	primaryGroup int
	home         string
	shell        string
}

type accountDatabase struct {
	groups map[string]int
	users  map[string]accountEntry
}

func checkAccounts(
	local probe.Local,
	groups []GroupSpec,
	users []UserSpec,
	limit int64,
) ([]protocol.Check, []protocol.Check, accountDatabase, []protocol.StructuredError) {
	groupData, groupErr := local.Read("etc/group", limit)
	passwdData, passwdErr := local.Read("etc/passwd", limit)
	failures := []protocol.StructuredError{}

	groupIDs := map[string]int{}
	if groupErr == nil {
		groupIDs, groupErr = parseGroups(groupData)
	}
	groupChecks := make([]protocol.Check, 0, len(groups))
	if groupErr != nil {
		failure := inspectionFailure("/etc/group", "group_inspection_failed", groupErr)
		failures = append(failures, failure)
		groupChecks = skippedGroupChecks(groups)
	} else {
		for _, group := range groups {
			id, exists := groupIDs[group.Name]
			status := protocol.StatusPass
			summary := fmt.Sprintf("group %s exists", group.Name)
			if !exists || (group.System && id >= 1000) {
				status = protocol.StatusWarning
				summary = fmt.Sprintf("group %s does not match the compiled profile", group.Name)
			}
			groupChecks = append(groupChecks, protocol.Check{
				ID: "group:" + group.Name, Status: status, Summary: summary,
			})
		}
	}

	accounts := map[string]accountEntry{}
	if passwdErr == nil {
		accounts, passwdErr = parsePasswd(passwdData)
	}
	userChecks := make([]protocol.Check, 0, len(users))
	if passwdErr != nil {
		failure := inspectionFailure("/etc/passwd", "user_inspection_failed", passwdErr)
		failures = append(failures, failure)
		userChecks = skippedUserChecks(users)
	} else {
		for _, user := range users {
			entry, exists := accounts[user.Name]
			groupID, groupExists := groupIDs[user.PrimaryGroup]
			matches := exists && groupExists && entry.primaryGroup == groupID &&
				entry.home == user.Home && entry.shell == user.Shell &&
				(!user.System || entry.id < 1000)
			status := protocol.StatusPass
			summary := fmt.Sprintf("user %s matches the compiled profile", user.Name)
			if !matches {
				status = protocol.StatusWarning
				summary = fmt.Sprintf("user %s does not match the compiled profile", user.Name)
			}
			userChecks = append(userChecks, protocol.Check{
				ID: "user:" + user.Name, Status: status, Summary: summary,
			})
		}
	}
	return groupChecks, userChecks, accountDatabase{
		groups: groupIDs,
		users:  accounts,
	}, failures
}

func parseGroups(content []byte) (map[string]int, error) {
	groups := map[string]int{}
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) != 4 {
			return nil, errors.New("invalid group database line")
		}
		id, err := strconv.Atoi(fields[2])
		if err != nil || id < 0 {
			return nil, errors.New("invalid group identifier")
		}
		if _, duplicate := groups[fields[0]]; duplicate {
			return nil, errors.New("duplicate group name")
		}
		groups[fields[0]] = id
	}
	return groups, scanner.Err()
}

func parsePasswd(content []byte) (map[string]accountEntry, error) {
	accounts := map[string]accountEntry{}
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), ":")
		if len(fields) != 7 {
			return nil, errors.New("invalid passwd database line")
		}
		uid, uidErr := strconv.Atoi(fields[2])
		gid, gidErr := strconv.Atoi(fields[3])
		if uidErr != nil || gidErr != nil || uid < 0 || gid < 0 {
			return nil, errors.New("invalid account identifier")
		}
		if _, duplicate := accounts[fields[0]]; duplicate {
			return nil, errors.New("duplicate account name")
		}
		accounts[fields[0]] = accountEntry{
			id: uid, primaryGroup: gid, home: fields[5], shell: fields[6],
		}
	}
	return accounts, scanner.Err()
}

func (checker Checker) checkDirectory(
	spec DirectorySpec,
	accounts accountDatabase,
	identity FileIdentityReader,
) (protocol.Check, *protocol.StructuredError) {
	id := "directory:" + spec.Path
	confinement := checker.Confinement
	if confinement == nil {
		confinement = localPathConfinement{}
	}
	path, info, err := confinement.Inspect(checker.Root, spec.Path)
	if errors.Is(err, os.ErrNotExist) {
		return warningCheck(id, "directory is missing"), nil
	}
	if errors.Is(err, probe.ErrSymlink) {
		return warningCheck(id, "directory path contains a symbolic link"), nil
	}
	if err != nil {
		failure := inspectionFailure(spec.Path, "directory_inspection_failed", err)
		return skippedCheck(id, "directory could not be inspected"), &failure
	}
	modeMismatch := runtime.GOOS != "windows" && uint32(info.Mode().Perm()) != spec.Mode
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || modeMismatch {
		return warningCheck(id, "directory type or mode does not match the compiled profile"), nil
	}
	identityMatches, err := matchesCompiledIdentity(
		identity, path, info, spec.Owner, spec.Group, accounts,
	)
	if err != nil {
		failure := inspectionFailure(spec.Path, "directory_identity_inspection_failed", err)
		return skippedCheck(id, "directory ownership could not be inspected"), &failure
	}
	if !identityMatches {
		return warningCheck(id, "directory owner or group does not match the compiled profile"), nil
	}
	return passCheck(id, "directory matches the compiled profile"), nil
}

func checkManagedFile(
	local probe.Local,
	root string,
	spec ManagedFileSpec,
	limit int64,
	accounts accountDatabase,
	identity FileIdentityReader,
) (protocol.Check, *protocol.StructuredError) {
	id := "file:" + spec.Path
	relative := strings.TrimPrefix(spec.Path, "/")
	content, err := local.Read(filepath.FromSlash(relative), limit)
	if errors.Is(err, os.ErrNotExist) {
		return warningCheck(id, "managed file is missing"), nil
	}
	if err != nil {
		failure := inspectionFailure(spec.Path, "managed_file_inspection_failed", err)
		return skippedCheck(id, "managed file could not be inspected"), &failure
	}
	path, err := rootedPath(root, spec.Path)
	if err != nil {
		failure := inspectionFailure(spec.Path, "managed_file_path_invalid", err)
		return skippedCheck(id, "managed file could not be inspected"), &failure
	}
	info, err := os.Lstat(path)
	if err != nil {
		failure := inspectionFailure(spec.Path, "managed_file_inspection_failed", err)
		return skippedCheck(id, "managed file could not be inspected"), &failure
	}
	digest := sha256.Sum256(content)
	modeMismatch := runtime.GOOS != "windows" && uint32(info.Mode().Perm()) != spec.Mode
	if hex.EncodeToString(digest[:]) != spec.SHA256 || modeMismatch {
		return warningCheck(id, "managed file digest or mode does not match the compiled profile"), nil
	}
	identityMatches, err := matchesCompiledIdentity(
		identity, path, info, spec.Owner, spec.Group, accounts,
	)
	if err != nil {
		failure := inspectionFailure(spec.Path, "managed_file_identity_inspection_failed", err)
		return skippedCheck(id, "managed file ownership could not be inspected"), &failure
	}
	if !identityMatches {
		return warningCheck(id, "managed file owner or group does not match the compiled profile"), nil
	}
	return passCheck(id, "managed file matches the compiled profile"), nil
}

func checkSysctl(
	local probe.Local,
	spec SysctlSpec,
) (protocol.Check, *protocol.StructuredError) {
	id := "sysctl:" + spec.Key
	relative := "proc/sys/" + strings.ReplaceAll(spec.Key, ".", "/")
	content, err := local.Read(filepath.FromSlash(relative), 4<<10)
	if errors.Is(err, os.ErrNotExist) {
		return warningCheck(id, "sysctl key is unavailable"), nil
	}
	if err != nil {
		failure := inspectionFailure(spec.Key, "sysctl_inspection_failed", err)
		return skippedCheck(id, "sysctl value could not be inspected"), &failure
	}
	if strings.TrimSpace(string(content)) != spec.Value {
		return warningCheck(id, "sysctl value does not match the compiled profile"), nil
	}
	return passCheck(id, "sysctl value matches the compiled profile"), nil
}

func checkUnit(
	ctx context.Context,
	local probe.Local,
	unit string,
) (protocol.Check, *protocol.StructuredError) {
	id := "unit:" + unit
	output, err := local.Run(ctx, probe.Command{
		Program: "systemctl", Arguments: []string{"is-enabled", "--", unit},
		StdoutLimit: 64 << 10, StderrLimit: 64 << 10,
	})
	if err != nil {
		failure := inspectionFailure("systemctl", "unit_inspection_failed", err)
		return skippedCheck(id, "unit state could not be inspected"), &failure
	}
	state := strings.TrimSpace(string(output.Stdout))
	if output.ExitCode != 0 || (state != "enabled" && state != "static" && state != "indirect") {
		return warningCheck(id, "unit is not enabled"), nil
	}
	return passCheck(id, "unit is enabled"), nil
}

func inspectionFailure(dependency, code string, err error) protocol.StructuredError {
	kind := protocol.ErrorGeneral
	switch {
	case errors.Is(err, os.ErrPermission):
		kind = protocol.ErrorPrivilege
	case errors.Is(err, execx.ErrNotFound):
		kind = protocol.ErrorDependency
	case errors.Is(err, ErrFileIdentityUnavailable):
		kind = protocol.ErrorDependency
	case errors.Is(err, context.Canceled):
		kind = protocol.ErrorCancelled
	case errors.Is(err, context.DeadlineExceeded):
		kind = protocol.ErrorTimeout
	}
	return protocol.StructuredError{
		Kind: kind, Code: code, Message: err.Error(), Dependency: dependency,
	}
}

func skippedChecks(prefix string, values []string, summary string) []protocol.Check {
	checks := make([]protocol.Check, 0, len(values))
	for _, value := range values {
		checks = append(checks, skippedCheck(prefix+value, summary))
	}
	return checks
}

func skippedGroupChecks(groups []GroupSpec) []protocol.Check {
	checks := make([]protocol.Check, 0, len(groups))
	for _, group := range groups {
		checks = append(checks, skippedCheck("group:"+group.Name, "group could not be inspected"))
	}
	return checks
}

func skippedUserChecks(users []UserSpec) []protocol.Check {
	checks := make([]protocol.Check, 0, len(users))
	for _, user := range users {
		checks = append(checks, skippedCheck("user:"+user.Name, "user could not be inspected"))
	}
	return checks
}

func passCheck(id, summary string) protocol.Check {
	return protocol.Check{ID: id, Status: protocol.StatusPass, Summary: summary}
}

func warningCheck(id, summary string) protocol.Check {
	return protocol.Check{ID: id, Status: protocol.StatusWarning, Summary: summary}
}

func skippedCheck(id, summary string) protocol.Check {
	return protocol.Check{ID: id, Status: protocol.StatusSkipped, Summary: summary}
}

func rootedPath(root, absolute string) (string, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		!strings.HasPrefix(absolute, "/") || strings.Contains(absolute, `\`) {
		return "", errors.New("invalid compiled path")
	}
	relative := strings.TrimPrefix(absolute, "/")
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("compiled path escapes root")
	}
	return filepath.Join(root, clean), nil
}

func (checker Checker) now() time.Time {
	if checker.Now != nil {
		return checker.Now().UTC()
	}
	return time.Now().UTC()
}
