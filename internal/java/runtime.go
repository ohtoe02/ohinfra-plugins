package java

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
)

const runtimeOutputLimit int64 = 64 << 10

var javaVersionLine = regexp.MustCompile(`^(?:openjdk|java) version "([^"]+)"(?: .*)?$`)

type RuntimeInfo struct {
	Version string `json:"version"`
	Runtime string `json:"runtime,omitempty"`
	VM      string `json:"vm,omitempty"`
}

func collectRuntime(ctx context.Context, local probe.Local) (RuntimeInfo, error) {
	output, err := local.Run(ctx, probe.Command{
		Program: "java", Arguments: []string{"-version"},
		StdoutLimit: runtimeOutputLimit, StderrLimit: runtimeOutputLimit,
	})
	if err != nil {
		return RuntimeInfo{}, err
	}
	if output.ExitCode != 0 || output.StdoutTruncated || output.StderrTruncated {
		return RuntimeInfo{}, errors.New("local java version probe failed")
	}
	raw := append([]byte(nil), output.Stdout...)
	if len(raw) > 0 && len(output.Stderr) > 0 {
		raw = append(raw, '\n')
	}
	raw = append(raw, output.Stderr...)
	if !utf8.Valid(raw) {
		return RuntimeInfo{}, errors.New("local java version output is not UTF-8")
	}
	return parseRuntimeVersion(string(raw))
}

func parseRuntimeVersion(raw string) (RuntimeInfo, error) {
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(raw), "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		return RuntimeInfo{}, errors.New("local java version output is empty")
	}
	match := javaVersionLine.FindStringSubmatch(strings.TrimSpace(lines[0]))
	if len(match) != 2 {
		return RuntimeInfo{}, errors.New("local java version output is malformed")
	}
	info := RuntimeInfo{Version: match[1]}
	if len(lines) > 1 {
		info.Runtime = strings.TrimSpace(lines[1])
	}
	if len(lines) > 2 {
		info.VM = strings.TrimSpace(lines[2])
	}
	return info, nil
}
