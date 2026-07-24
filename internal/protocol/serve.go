package protocol

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ohtoe02/ohinfra-plugins/internal/strictjson"
)

type Definition struct {
	Manifest Manifest
	Plan     func(context.Context, Invocation) (Plan, error)
	Execute  func(context.Context, Invocation) (Result, error)
}

type ExitError struct {
	Code int
	Err  error
}

func (failure ExitError) Error() string {
	if failure.Err == nil {
		return fmt.Sprintf("plugin failed with exit code %d", failure.Code)
	}
	return failure.Err.Error()
}

func (failure ExitError) Unwrap() error {
	return failure.Err
}

func Serve(
	definition Definition,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) int {
	if len(args) != 3 || args[2] != "--protocol=1" {
		_, _ = fmt.Fprintln(stderr, "usage: <plugin> <manifest|plan|execute> --protocol=1")
		return ExitArguments
	}
	if err := ValidateManifest(definition.Manifest); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return ExitConfiguration
	}
	encoder := json.NewEncoder(stdout)
	switch args[1] {
	case "manifest":
		if err := encoder.Encode(definition.Manifest); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return ExitGeneral
		}
		return ExitOK
	case "plan", "execute":
		encoded, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return ExitArguments
		}
		var invocation Invocation
		if err := strictjson.Decode(encoded, &invocation); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return ExitArguments
		}
		if invocation.ProtocolVersion != ProtocolVersion {
			_, _ = fmt.Fprintln(stderr, "unsupported protocol version")
			return ExitArguments
		}
		if invocation.Arguments == nil {
			invocation.Arguments = []string{}
		}
		if invocation.Options == nil {
			invocation.Options = map[string]any{}
		}
		ctx, cancel, err := invocationContext(invocation)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return ExitArguments
		}
		defer cancel()
		if args[1] == "plan" {
			if definition.Plan == nil {
				_, _ = fmt.Fprintln(stderr, "plan is not supported")
				return ExitArguments
			}
			plan, planErr := definition.Plan(ctx, invocation)
			if planErr != nil {
				return writeFailure(stderr, planErr)
			}
			normalizePlan(&plan)
			if err := encoder.Encode(plan); err != nil {
				_, _ = fmt.Fprintln(stderr, err)
				return ExitGeneral
			}
			return ExitOK
		}
		if definition.Execute == nil {
			_, _ = fmt.Fprintln(stderr, "execute is not supported")
			return ExitArguments
		}
		result, executeErr := definition.Execute(ctx, invocation)
		if executeErr != nil {
			return writeFailure(stderr, executeErr)
		}
		result = Normalize(result)
		if err := encoder.Encode(result); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return ExitGeneral
		}
		return ExitOK
	default:
		_, _ = fmt.Fprintln(stderr, "unsupported protocol verb")
		return ExitArguments
	}
}

func PlanDigest(plan Plan) (string, error) {
	normalizePlan(&plan)
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", fmt.Errorf("encode operation plan: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func normalizePlan(plan *Plan) {
	if plan.Checks == nil {
		plan.Checks = []Check{}
	}
	if plan.Changes == nil {
		plan.Changes = []Change{}
	}
	if plan.Risks == nil {
		plan.Risks = []string{}
	}
}

func invocationContext(invocation Invocation) (context.Context, context.CancelFunc, error) {
	if invocation.Deadline == "" {
		ctx, cancel := context.WithCancel(context.Background())
		return ctx, cancel, nil
	}
	deadline, err := time.Parse(time.RFC3339Nano, invocation.Deadline)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid invocation deadline: %w", err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	return ctx, cancel, nil
}

func writeFailure(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintln(stderr, err)
	var failure ExitError
	if errors.As(err, &failure) && failure.Code >= ExitGeneral && failure.Code <= ExitConfiguration {
		return failure.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ExitTimeout
	}
	if errors.Is(err, context.Canceled) {
		return ExitCancelled
	}
	if strings.TrimSpace(err.Error()) == "" {
		return ExitGeneral
	}
	return ExitGeneral
}
