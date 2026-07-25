package serversetupreadiness

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/ohtoe02/ohtools-plugins/internal/protocol"
)

func TestRunExercisesIdempotencyDriftRepairAndSafeUpgrade(t *testing.T) {
	report, err := Run(
		context.Background(),
		[]byte("ID=debian\nVERSION_ID=\"12\"\n"),
		t.TempDir(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateReport(report, "debian", "12"); err != nil {
		t.Fatal(err)
	}
	if report.InitialApplyChanges == 0 ||
		report.SecondApplyChanges != 0 ||
		report.DriftRepairChanges != 1 ||
		report.UpgradeChanges == 0 ||
		report.SecondUpgradeChanges != 0 ||
		!report.ExactUpgradeApplied ||
		report.SystemApplyStatus != "pass" ||
		report.SystemApplyChanges == 0 ||
		report.SystemSecondApplyStatus != "pass" ||
		report.SystemSecondApplyChanges != 0 ||
		!report.DependencyProbesPassed {
		t.Fatalf("report = %#v", report)
	}
}

func TestSevenImageWorkflowExecutesRealBinaryApplyTwice(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(content)
	for _, required := range []string{
		"binary-smoke /work/server-setup-base_linux_amd64",
		"--tmpfs /etc/ohtools:",
		"--tmpfs /etc/profile.d:",
		"server-setup-entitlement-fixture_linux_amd64:/usr/bin/pro:ro",
	} {
		if !strings.Contains(workflow, required) {
			t.Fatalf("seven-image workflow is missing %q", required)
		}
	}
}

func TestValidateBinarySmokeRequiresFirstChangeAndSecondNoop(t *testing.T) {
	t.Parallel()

	initial := protocol.Result{
		Status: protocol.StatusPass,
		Changes: []protocol.Change{{
			Object: "shell-history", Action: "converge", Status: "completed",
		}},
	}
	second := protocol.Result{Status: protocol.StatusPass, Changes: []protocol.Change{}}
	if err := validateBinarySmoke(initial, second); err != nil {
		t.Fatal(err)
	}
	second.Changes = append(second.Changes, protocol.Change{
		Object: "shell-history", Action: "converge", Status: "completed",
	})
	if err := validateBinarySmoke(initial, second); err == nil {
		t.Fatal("second binary mutation was accepted as idempotent")
	}
}
