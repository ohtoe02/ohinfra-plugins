package monitoring

import (
	"context"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func executeStatus(
	ctx context.Context,
	options Options,
	local probe.Local,
) (protocol.Result, error) {
	inventory, checks, failures, err := collectInventory(ctx, local)
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return protocol.Result{}, fatal
		}
		return protocol.Result{}, dependencyFailure("local monitoring status is unavailable")
	}
	configs, configIssues, err := collectConfigs(ctx, local)
	if err != nil {
		if fatal := contextFailure(err); fatal != nil {
			return protocol.Result{}, fatal
		}
		return protocol.Result{}, dependencyFailure("local monitoring config status is unavailable")
	}
	failures = append(failures, configFailures(configIssues)...)
	for _, issue := range configIssues {
		checks = append(checks, protocol.Check{
			ID:     "monitoring." + issue.productID + "." + issue.code,
			Status: protocol.StatusPartial, Summary: issue.message,
		})
	}
	return buildResult(options, "monitoring status",
		map[string]any{"products": inventory, "configs": configs},
		checks, failures), nil
}
