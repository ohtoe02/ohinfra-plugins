package gitlabrunner

import (
	"context"
	"errors"
	"os"
	"sort"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

type ExecutorInfo struct {
	Name        string `json:"name"`
	RunnerCount int    `json:"runner_count"`
}

func executeExecutors(ctx context.Context, options Options) (protocol.Result, error) {
	if err := ctx.Err(); err != nil {
		return protocol.Result{}, err
	}
	metadata, err := readConfig(ctx, options)
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return protocol.Result{}, fatal
		}
		if errors.Is(err, os.ErrNotExist) {
			return protocol.Result{}, dependencyFailure(
				"local GitLab Runner configuration is unavailable")
		}
		return protocol.Result{}, configurationFailure(
			"local GitLab Runner configuration is not safe strict TOML")
	}
	counts := make(map[string]int, len(metadata.Runners))
	for _, runner := range metadata.Runners {
		counts[runner.Executor]++
	}
	executors := make([]ExecutorInfo, 0, len(counts))
	for name, count := range counts {
		executors = append(executors, ExecutorInfo{Name: name, RunnerCount: count})
	}
	sort.Slice(executors, func(left, right int) bool {
		return executors[left].Name < executors[right].Name
	})
	status := protocol.StatusPass
	summary := "Configured local GitLab Runner executors are available"
	if len(executors) == 0 {
		status = protocol.StatusInfo
		summary = "No local GitLab Runner executors are configured"
	}
	return buildResult(options, "gitlab-runner executors",
		map[string]any{"executors": executors},
		[]protocol.Check{{
			ID: "gitlab-runner.executors", Status: status, Summary: summary,
		}}, nil), nil
}
