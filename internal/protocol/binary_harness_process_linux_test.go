//go:build linux

package protocol_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

const signaledProcessSentinel = "OHTOOLS_PROTOCOL_TEST_SIGKILL"

func TestSignaledFixtureProcessIsRecognizedAsReapedOnLinux(t *testing.T) {
	if os.Getenv(signaledProcessSentinel) == "1" {
		terminateCurrentProcessForTest()
		t.Fatal("process survived SIGKILL")
	}

	command := exec.CommandContext(
		context.Background(),
		os.Args[0],
		"-test.run=^TestSignaledFixtureProcessIsRecognizedAsReapedOnLinux$",
	)
	command.Env = append(os.Environ(), signaledProcessSentinel+"=1")
	configureFixtureCommand(command)
	if err := command.Run(); err == nil {
		t.Fatal("signaled subprocess returned success")
	}
	if command.ProcessState == nil {
		t.Fatal("signaled subprocess was not waited")
	}
	if command.ProcessState.Exited() {
		t.Fatal("regression requires Unix signal termination")
	}
	if err := verifyFixtureCommandReaped(command); err != nil {
		t.Fatal(err)
	}
}

func configureFixtureCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}

func verifyFixtureCommandReaped(command *exec.Cmd) error {
	if command.Process == nil || command.ProcessState == nil {
		return errors.New("command did not record a waited process")
	}
	pid := command.ProcessState.Pid()
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("process %d remains after Wait: %v", pid, err)
	}
	if err := syscall.Kill(-pid, 0); !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("process group %d remains after Wait: %v", pid, err)
	}
	return nil
}

func terminateCurrentProcessForTest() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
}
