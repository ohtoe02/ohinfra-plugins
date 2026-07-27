package java

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
	"github.com/ohtoe02/ohtools-plugins/internal/redact"
)

const (
	maxProcEntries = 32_768
	commLimit      = 256
	cmdlineLimit   = 64 << 10
)

var credentialURL = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)([^/@\s]+)@`)

type ProcessInfo struct {
	PID         int      `json:"pid"`
	Executable  string   `json:"executable"`
	CommandLine []string `json:"command_line"`
}

type procEnumeration struct {
	PIDs          []int
	Truncated     bool
	UnsafeEntries int
}

func collectJavaProcesses(
	ctx context.Context,
	local probe.Local,
) ([]ProcessInfo, []protocol.StructuredError, error) {
	enumeration, err := enumeratePIDs(ctx, local.Root)
	if err != nil {
		return nil, nil, err
	}
	processes := make([]ProcessInfo, 0)
	issues := make([]protocol.StructuredError, 0)
	if enumeration.Truncated {
		issues = append(issues, processIssue(
			"process_list_truncated", "Local process list exceeded the safe entry limit", 0,
		))
	}
	if enumeration.UnsafeEntries > 0 {
		issues = append(issues, processIssue(
			"unsafe_proc_entry", "Symbolic process entries were ignored", 0,
		))
	}
	for _, pid := range enumeration.PIDs {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		process, javaProcess, processErr := readProcess(local, pid)
		if processErr != nil {
			if errors.Is(processErr, os.ErrNotExist) {
				continue
			}
			issues = append(issues, processIssue(
				"process_metadata_unavailable", "Local process metadata was ignored", pid,
			))
			continue
		}
		if javaProcess {
			processes = append(processes, process)
		}
	}
	return processes, issues, nil
}

func enumeratePIDs(ctx context.Context, root string) (procEnumeration, error) {
	if err := ctx.Err(); err != nil {
		return procEnumeration{}, err
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return procEnumeration{}, probe.ErrInvalidRoot
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return procEnumeration{}, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return procEnumeration{}, probe.ErrInvalidRoot
	}
	procPath := filepath.Join(root, "proc")
	procInfo, err := os.Lstat(procPath)
	if err != nil {
		return procEnumeration{}, err
	}
	if procInfo.Mode()&os.ModeSymlink != 0 {
		return procEnumeration{}, probe.ErrSymlink
	}
	if !procInfo.IsDir() {
		return procEnumeration{}, probe.ErrNotRegular
	}
	directory, err := os.Open(procPath)
	if err != nil {
		return procEnumeration{}, err
	}
	defer func() { _ = directory.Close() }()
	entries, readErr := directory.ReadDir(maxProcEntries + 1)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return procEnumeration{}, readErr
	}

	output := procEnumeration{Truncated: len(entries) > maxProcEntries}
	if output.Truncated {
		entries = entries[:maxProcEntries]
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return procEnumeration{}, err
		}
		pid, parseErr := strconv.ParseUint(entry.Name(), 10, 32)
		if parseErr != nil || pid == 0 || pid > maxPID {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			output.UnsafeEntries++
			continue
		}
		entryInfo, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		if entryInfo.Mode()&os.ModeSymlink != 0 {
			output.UnsafeEntries++
			continue
		}
		if !entryInfo.IsDir() {
			continue
		}
		output.PIDs = append(output.PIDs, int(pid))
	}
	sort.Ints(output.PIDs)
	return output, nil
}

func readProcess(local probe.Local, pid int) (ProcessInfo, bool, error) {
	prefix := filepath.ToSlash(filepath.Join("proc", strconv.Itoa(pid)))
	commBytes, commErr := local.Read(prefix+"/comm", commLimit)
	cmdlineBytes, cmdlineErr := local.Read(prefix+"/cmdline", cmdlineLimit)
	if commErr != nil && cmdlineErr != nil {
		if errors.Is(commErr, os.ErrNotExist) && errors.Is(cmdlineErr, os.ErrNotExist) {
			return ProcessInfo{}, false, os.ErrNotExist
		}
		return ProcessInfo{}, false, errors.New("local process metadata is unavailable")
	}
	if commErr != nil && !errors.Is(commErr, os.ErrNotExist) {
		return ProcessInfo{}, false, errors.New("local process name is unavailable")
	}
	if cmdlineErr != nil && !errors.Is(cmdlineErr, os.ErrNotExist) {
		return ProcessInfo{}, false, errors.New("local process command line is unavailable")
	}
	if !utf8.Valid(commBytes) || !utf8.Valid(cmdlineBytes) {
		return ProcessInfo{}, false, errors.New("local process metadata is not UTF-8")
	}
	comm := strings.TrimSpace(string(commBytes))
	arguments := splitCommandLine(cmdlineBytes)
	executable := ""
	if len(arguments) > 0 {
		executable = filepath.Base(arguments[0])
	}
	if executable == "" {
		executable = comm
	}
	if !isJavaExecutable(comm) && !isJavaExecutable(executable) {
		return ProcessInfo{}, false, nil
	}
	arguments = redactJavaArguments(arguments)
	return ProcessInfo{
		PID: pid, Executable: executable, CommandLine: arguments,
	}, true, nil
}

func redactJavaArguments(values []string) []string {
	output := make([]string, len(values))
	maskNext := false
	for index, value := range values {
		if maskNext {
			output[index] = redact.Mask
			maskNext = false
			continue
		}
		output[index] = redactJavaArgument(value)
		optionName := strings.TrimLeft(value, "-")
		if strings.HasPrefix(optionName, "D") {
			optionName = optionName[1:]
		}
		if strings.HasPrefix(value, "-") &&
			!strings.Contains(optionName, "=") && sensitiveJavaKey(optionName) {
			maskNext = true
		}
	}
	return output
}

func redactJavaArgument(value string) string {
	if strings.HasPrefix(value, "-D") {
		if separator := strings.IndexByte(value, '='); separator > 2 &&
			sensitiveJavaKey(value[2:separator]) {
			return value[:separator+1] + redact.Mask
		}
	}
	if separator := strings.IndexByte(value, '='); separator > 0 &&
		sensitiveJavaKey(strings.TrimLeft(value[:separator], "-")) {
		return value[:separator+1] + redact.Mask
	}
	value = credentialURL.ReplaceAllString(value, `${1}`+redact.Mask+"@")
	return redact.String(value)
}

func sensitiveJavaKey(value string) bool {
	normalized := strings.NewReplacer(
		".", "", "_", "", "-", "", " ", "",
	).Replace(strings.ToLower(value))
	for _, marker := range []string{
		"password", "passwd", "pwd", "token", "secret", "credential",
		"authorization", "apikey", "accesskey", "privatekey",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func splitCommandLine(value []byte) []string {
	parts := strings.Split(strings.TrimRight(string(value), "\x00"), "\x00")
	if len(parts) == 1 && parts[0] == "" {
		return []string{}
	}
	return parts
}

func isJavaExecutable(value string) bool {
	return value == "java" || value == "javaw"
}

func processIssue(code, message string, pid int) protocol.StructuredError {
	details := map[string]any{}
	if pid > 0 {
		details["pid"] = pid
	}
	return protocol.StructuredError{
		Kind: protocol.ErrorGeneral, Code: code, Message: message,
		Details: details,
	}
}
