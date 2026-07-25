//go:build linux

package execx

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func prepareCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

func killCommand(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	var failures []error
	stopErr := syscall.Kill(command.Process.Pid, syscall.SIGSTOP)
	if stopErr != nil && !errors.Is(stopErr, syscall.ESRCH) {
		failures = append(failures, fmt.Errorf("stop process before cleanup: %w", stopErr))
	}
	descendants, discoveryErr := linuxDescendants(command.Process.Pid)
	if discoveryErr != nil {
		failures = append(failures, discoveryErr)
	}
	for index := len(descendants) - 1; index >= 0; index-- {
		if err := syscall.Kill(descendants[index], syscall.SIGKILL); err != nil &&
			!errors.Is(err, syscall.ESRCH) {
			failures = append(failures, fmt.Errorf(
				"kill descendant %d: %w",
				descendants[index],
				err,
			))
		}
	}
	if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func linuxDescendants(pid int) ([]int, error) {
	childrenPath := filepath.Join(
		"/proc",
		strconv.Itoa(pid),
		"task",
		strconv.Itoa(pid),
		"children",
	)
	content, err := os.ReadFile(childrenPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read process descendants: %w", err)
	}
	descendants := []int{}
	for _, field := range strings.Fields(string(content)) {
		child, parseErr := strconv.Atoi(field)
		if parseErr != nil || child <= 0 {
			return nil, errors.New("invalid process descendant metadata")
		}
		nested, nestedErr := linuxDescendants(child)
		if nestedErr != nil {
			return nil, nestedErr
		}
		descendants = append(descendants, child)
		descendants = append(descendants, nested...)
	}
	return descendants, nil
}
