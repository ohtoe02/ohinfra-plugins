package java

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/redact"
)

const (
	jcmdOutputLimit int64 = 256 << 10
	jcmdErrorLimit  int64 = 64 << 10
)

var errAttachDenied = errors.New("local Java attach permission denied")

type SystemProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type InspectInfo struct {
	PID              int              `json:"pid"`
	Version          string           `json:"version"`
	CommandLine      []string         `json:"command_line"`
	SystemProperties []SystemProperty `json:"system_properties"`
}

func inspectProcess(ctx context.Context, local probe.Local, pid int) (InspectInfo, error) {
	versionOutput, err := runJcmd(ctx, local, pid, "VM.version")
	if err != nil {
		return InspectInfo{}, err
	}
	commandOutput, err := runJcmd(ctx, local, pid, "VM.command_line")
	if err != nil {
		return InspectInfo{}, err
	}
	propertiesOutput, err := runJcmd(ctx, local, pid, "VM.system_properties")
	if err != nil {
		return InspectInfo{}, err
	}
	version, err := parseJcmdVersion(versionOutput, pid)
	if err != nil {
		return InspectInfo{}, err
	}
	commandLine, err := parseJcmdCommandLine(commandOutput, pid)
	if err != nil {
		return InspectInfo{}, err
	}
	properties, err := parseJcmdProperties(propertiesOutput, pid)
	if err != nil {
		return InspectInfo{}, err
	}
	return InspectInfo{
		PID: pid, Version: version,
		CommandLine: commandLine, SystemProperties: properties,
	}, nil
}

func runJcmd(
	ctx context.Context,
	local probe.Local,
	pid int,
	operation string,
) ([]byte, error) {
	switch operation {
	case "VM.version", "VM.command_line", "VM.system_properties":
	default:
		return nil, errors.New("unsupported local jcmd operation")
	}
	output, err := local.Run(ctx, probe.Command{
		Program: "jcmd", Arguments: []string{strconv.Itoa(pid), operation},
		StdoutLimit: jcmdOutputLimit, StderrLimit: jcmdErrorLimit,
	})
	if err != nil {
		return nil, err
	}
	if output.ExitCode != 0 {
		diagnostic := strings.ToLower(string(append(
			append([]byte(nil), output.Stdout...), output.Stderr...,
		)))
		for _, marker := range []string{
			"permission denied", "operation not permitted", "access denied",
		} {
			if strings.Contains(diagnostic, marker) {
				return nil, errAttachDenied
			}
		}
		return nil, errors.New("local jcmd query failed")
	}
	if output.StdoutTruncated || output.StderrTruncated {
		return nil, errors.New("local jcmd output exceeded the safe limit")
	}
	if !utf8.Valid(output.Stdout) {
		return nil, errors.New("local jcmd output is not UTF-8")
	}
	return append([]byte(nil), output.Stdout...), nil
}

func parseJcmdVersion(input []byte, pid int) (string, error) {
	lines, err := jcmdPayloadLines(input, pid)
	if err != nil || len(lines) == 0 {
		return "", errors.New("local jcmd version output is malformed")
	}
	return strings.Join(lines, "\n"), nil
}

func parseJcmdCommandLine(input []byte, pid int) ([]string, error) {
	lines, err := jcmdPayloadLines(input, pid)
	if err != nil {
		return nil, errors.New("local jcmd command-line output is malformed")
	}
	output := make([]string, 0, len(lines))
	for _, line := range lines {
		if line == "VM Arguments:" {
			continue
		}
		fields := redactJavaArguments(strings.Fields(line))
		if len(fields) > 0 {
			output = append(output, strings.Join(fields, " "))
		}
	}
	return output, nil
}

func parseJcmdProperties(input []byte, pid int) ([]SystemProperty, error) {
	lines, err := jcmdPayloadLines(input, pid)
	if err != nil {
		return nil, errors.New("local jcmd system-properties output is malformed")
	}
	output := make([]SystemProperty, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, "#") {
			continue
		}
		separator := strings.IndexByte(line, '=')
		if separator < 1 {
			return nil, errors.New("local jcmd system-properties output is malformed")
		}
		name := strings.TrimSpace(line[:separator])
		value := line[separator+1:]
		if !safePropertyName(name) {
			return nil, errors.New("local jcmd system-properties output is malformed")
		}
		if sensitiveJavaKey(name) {
			value = redact.Mask
		} else {
			value = redactJavaArgument(value)
		}
		output = append(output, SystemProperty{Name: name, Value: value})
	}
	sort.Slice(output, func(left, right int) bool {
		return output[left].Name < output[right].Name
	})
	return output, nil
}

func jcmdPayloadLines(input []byte, pid int) ([]string, error) {
	text := strings.ReplaceAll(strings.TrimSpace(string(input)), "\r\n", "\n")
	if text == "" {
		return nil, errors.New("local jcmd output is empty")
	}
	lines := strings.Split(text, "\n")
	if strings.TrimSpace(lines[0]) != fmt.Sprintf("%d:", pid) {
		return nil, errors.New("local jcmd output has an unexpected process header")
	}
	output := make([]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line != "" {
			output = append(output, line)
		}
	}
	return output, nil
}

func safePropertyName(value string) bool {
	if len(value) == 0 || len(value) > 512 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) ||
			character == '=' {
			return false
		}
	}
	return true
}
