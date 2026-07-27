package security

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

const identityFileLimit int64 = 256 << 10

type accountMetadata struct {
	Name           string `json:"name"`
	UID            int    `json:"uid"`
	GID            int    `json:"gid"`
	LoginShell     bool   `json:"login_shell"`
	Locked         *bool  `json:"locked,omitempty"`
	HomePresent    bool   `json:"home_present"`
	HomeUnsafeMode bool   `json:"home_unsafe_mode"`
}

type groupMetadata struct {
	Name        string `json:"name"`
	GID         int    `json:"gid"`
	MemberCount int    `json:"member_count"`
}

func collectAccounts(ctx context.Context, options Options) collection {
	if err := ctx.Err(); err != nil {
		return collection{Fatal: err}
	}
	passwdLines, err := options.Local.Lines("etc/passwd", identityFileLimit)
	if err != nil {
		return collection{
			Errors: []protocol.StructuredError{structuredError(
				protocol.ErrorDependency, "passwd_unavailable", "local account database is unavailable", "passwd",
			)},
			ExitCode: protocol.ExitDependency,
		}
	}

	shadow := readShadowMetadata(options)
	accounts, parseErrors := parsePasswd(options.Root, passwdLines, shadow.Locked)
	groups, groupErrors := readGroups(options)
	errors := append(parseErrors, groupErrors...)
	errors = append(errors, shadow.Errors...)
	checks := accountChecks(accounts)
	return collection{
		Data: map[string]any{
			"accounts":          accounts,
			"groups":            groups,
			"shadow_accessible": shadow.Accessible,
		},
		Checks: checks, Errors: errors, Usable: true,
	}
}

type shadowMetadata struct {
	Accessible bool
	Locked     map[string]bool
	Errors     []protocol.StructuredError
}

func readShadowMetadata(options Options) shadowMetadata {
	lines, err := options.Local.Lines("etc/shadow", identityFileLimit)
	if err != nil {
		return shadowMetadata{
			Locked: map[string]bool{},
			Errors: []protocol.StructuredError{structuredError(
				protocol.ErrorPrivilege, "shadow_unavailable",
				"password lock metadata is unavailable", "shadow",
			)},
		}
	}
	locked := make(map[string]bool, len(lines))
	for _, line := range lines {
		fields := strings.Split(line, ":")
		if len(fields) < 2 || fields[0] == "" {
			continue
		}
		locked[fields[0]] = strings.HasPrefix(fields[1], "!") || strings.HasPrefix(fields[1], "*")
	}
	return shadowMetadata{Accessible: true, Locked: locked}
}

func parsePasswd(
	root string,
	lines []string,
	locked map[string]bool,
) ([]accountMetadata, []protocol.StructuredError) {
	accounts := make([]accountMetadata, 0, len(lines))
	errors := []protocol.StructuredError{}
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 7 {
			errors = append(errors, structuredError(
				protocol.ErrorGeneral, "malformed_passwd_entry",
				"a malformed local account entry was ignored", "passwd",
			))
			continue
		}
		uid, uidErr := strconv.Atoi(fields[2])
		gid, gidErr := strconv.Atoi(fields[3])
		if fields[0] == "" || uidErr != nil || gidErr != nil || uid < 0 || gid < 0 {
			errors = append(errors, structuredError(
				protocol.ErrorGeneral, "malformed_passwd_entry",
				"a malformed local account entry was ignored", "passwd",
			))
			continue
		}
		item := accountMetadata{
			Name: fields[0], UID: uid, GID: gid,
			LoginShell: loginShell(fields[6]),
		}
		if value, ok := locked[item.Name]; ok {
			item.Locked = new(bool)
			*item.Locked = value
		}
		item.HomePresent, item.HomeUnsafeMode = inspectHome(root, fields[5])
		accounts = append(accounts, item)
	}
	sort.Slice(accounts, func(left, right int) bool {
		if accounts[left].UID == accounts[right].UID {
			return accounts[left].Name < accounts[right].Name
		}
		return accounts[left].UID < accounts[right].UID
	})
	return accounts, errors
}

func readGroups(options Options) ([]groupMetadata, []protocol.StructuredError) {
	lines, err := options.Local.Lines("etc/group", identityFileLimit)
	if err != nil {
		return []groupMetadata{}, []protocol.StructuredError{structuredError(
			protocol.ErrorDependency, "group_unavailable",
			"local group database is unavailable", "group",
		)}
	}
	groups := make([]groupMetadata, 0, len(lines))
	errors := []protocol.StructuredError{}
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 4 {
			errors = append(errors, structuredError(
				protocol.ErrorGeneral, "malformed_group_entry",
				"a malformed local group entry was ignored", "group",
			))
			continue
		}
		gid, err := strconv.Atoi(fields[2])
		if fields[0] == "" || err != nil || gid < 0 {
			continue
		}
		count := 0
		if strings.TrimSpace(fields[3]) != "" {
			count = len(strings.Split(fields[3], ","))
		}
		groups = append(groups, groupMetadata{Name: fields[0], GID: gid, MemberCount: count})
	}
	sort.Slice(groups, func(left, right int) bool {
		if groups[left].GID == groups[right].GID {
			return groups[left].Name < groups[right].Name
		}
		return groups[left].GID < groups[right].GID
	})
	return groups, errors
}

func inspectHome(root, home string) (bool, bool) {
	if !strings.HasPrefix(home, "/") || strings.ContainsRune(home, '\x00') {
		return false, false
	}
	clean := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(home, "/")))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return false, false
	}
	path := filepath.Join(root, clean)
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false, false
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, false
	}
	return true, info.Mode().Perm()&0o022 != 0
}

func accountChecks(accounts []accountMetadata) []protocol.Check {
	uidZero := []string{}
	unsafeHomes := []string{}
	for _, account := range accounts {
		if account.UID == 0 {
			uidZero = append(uidZero, account.Name)
		}
		if account.HomeUnsafeMode {
			unsafeHomes = append(unsafeHomes, account.Name)
		}
	}
	checks := []protocol.Check{}
	if len(uidZero) > 1 {
		checks = append(checks, protocol.Check{
			ID: "accounts.duplicate-uid-zero", Status: protocol.StatusWarning,
			Summary: "Multiple local accounts have UID 0",
			Details: map[string]any{"account_count": len(uidZero), "accounts": uidZero},
		})
	} else {
		checks = append(checks, protocol.Check{
			ID: "accounts.uid-zero", Status: protocol.StatusPass,
			Summary: "No duplicate UID 0 account was found",
		})
	}
	if len(unsafeHomes) > 0 {
		checks = append(checks, protocol.Check{
			ID: "accounts.home-permissions", Status: protocol.StatusWarning,
			Summary: "Some local home directories are group- or world-writable",
			Details: map[string]any{"account_count": len(unsafeHomes), "accounts": unsafeHomes},
		})
	}
	return checks
}

func loginShell(shell string) bool {
	base := filepath.Base(strings.TrimSpace(shell))
	switch base {
	case "", "false", "nologin":
		return false
	default:
		return true
	}
}
