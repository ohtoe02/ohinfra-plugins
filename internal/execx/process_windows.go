//go:build windows

package execx

import "os/exec"

func prepareCommand(*exec.Cmd) {}

func killCommand(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
