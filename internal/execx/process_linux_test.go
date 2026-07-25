//go:build linux

package execx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOperationalChildInheritsPluginKillDomainAndDiesWithParent(t *testing.T) {
	command := exec.Command("/bin/true")
	prepareCommand(command)
	if command.SysProcAttr == nil {
		t.Fatal("Linux child process attributes are missing")
	}
	if command.SysProcAttr.Setpgid {
		t.Fatal("operational child escaped the host plugin process group")
	}
	if command.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatalf("Pdeathsig = %v, want SIGKILL", command.SysProcAttr.Pdeathsig)
	}
}

func TestCancelledOperationalCommandLeavesNoDescendantMutation(t *testing.T) {
	program := copyTestExecutable(t)
	marker := filepath.Join(t.TempDir(), "orphan-marker")
	runner := OSRunner{Resolver: Resolver{Directories: []string{filepath.Dir(program)}}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := runner.Run(ctx, Spec{
		Program: filepath.Base(program),
		Environment: map[string]string{
			"GO_WANT_EXECX_HELPER": "spawn-descendant",
			"GO_WANT_EXECX_MARKER": marker,
		},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runner error = %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, statErr := os.Lstat(marker); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("cancelled descendant mutated after cleanup: %v", statErr)
	}
}
