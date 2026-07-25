//go:build !linux

package protocol_test

import (
	"errors"
	"os/exec"
)

func configureFixtureCommand(_ *exec.Cmd) {}

func verifyFixtureCommandReaped(command *exec.Cmd) error {
	if command.Process == nil || command.ProcessState == nil {
		return errors.New("command did not record a waited process")
	}
	return nil
}
