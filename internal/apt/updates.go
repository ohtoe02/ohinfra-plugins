package apt

import (
	"context"
	"errors"
	"strings"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
)

var updateSimulationArguments = []string{
	"--simulate",
	"--no-download",
	"--option", "Debug::NoLocking=true",
	"upgrade",
}

func collectUpdates(ctx context.Context, local probe.Local) (map[string]any, error) {
	output, err := local.Run(ctx, probe.Command{
		Program: "apt-get", Arguments: updateSimulationArguments,
		StdoutLimit: probe.MaxCommandOutputLimit, StderrLimit: probe.MaxCommandOutputLimit,
	})
	if err != nil {
		return nil, err
	}
	if output.ExitCode != 0 {
		return nil, errors.New("apt-get simulation failed")
	}
	updates := []map[string]any{}
	for _, line := range strings.Split(string(output.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "Inst" {
			continue
		}
		item := map[string]any{"package": fields[1]}
		for _, field := range fields[2:] {
			if strings.HasPrefix(field, "[") && strings.HasSuffix(field, "]") {
				item["installed_version"] = strings.Trim(field, "[]")
				break
			}
		}
		if open := strings.Index(line, "("); open >= 0 {
			candidate := strings.Fields(line[open+1:])
			if len(candidate) > 0 {
				item["candidate_version"] = strings.TrimRight(candidate[0], ")")
			}
		}
		updates = append(updates, item)
	}
	return map[string]any{"updates": updates, "count": len(updates)}, nil
}
