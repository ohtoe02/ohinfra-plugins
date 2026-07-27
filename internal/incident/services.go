package incident

import (
	"context"
	"sort"

	"github.com/ohtoe02/ohtools-plugins/internal/probe"
	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func executeServices(ctx context.Context, options Options) (protocol.Result, error) {
	local := probe.Local{Root: options.Root, Runner: options.Runner}
	output, err := runBounded(
		ctx, local, "systemctl", failedUnitArguments(), commandOutputLimit,
	)
	if err != nil {
		if contextFailure(err) != nil {
			return protocol.Result{}, err
		}
		return buildResult(options, "incident services",
			map[string]any{"failed_services": []ServiceEvidence{}},
			[]protocol.Check{{
				ID: "incident.services", Status: protocol.StatusPartial,
				Summary: "Failed service evidence is unavailable",
			}},
			[]protocol.StructuredError{
				evidenceFailure("services", "Failed service evidence is unavailable"),
			},
		), nil
	}
	services, err := parseServices(string(output))
	if err != nil {
		return buildResult(options, "incident services",
			map[string]any{"failed_services": []ServiceEvidence{}},
			[]protocol.Check{{
				ID: "incident.services", Status: protocol.StatusPartial,
				Summary: "Failed service evidence is malformed",
			}},
			[]protocol.StructuredError{
				evidenceFailure("services", "Failed service evidence is malformed"),
			},
		), nil
	}
	sort.Slice(services, func(left, right int) bool {
		return services[left].Unit < services[right].Unit
	})
	return buildResult(options, "incident services",
		map[string]any{"failed_services": services},
		[]protocol.Check{{
			ID: "incident.services", Status: protocol.StatusPass,
			Summary: "Failed service evidence collected",
			Details: map[string]any{"failed_count": len(services)},
		}}, nil,
	), nil
}
