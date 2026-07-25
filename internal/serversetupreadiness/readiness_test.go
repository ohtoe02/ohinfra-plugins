package serversetupreadiness

import (
	"context"
	"testing"
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
