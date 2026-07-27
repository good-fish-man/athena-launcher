//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

func configureDetachedProcess(command *exec.Cmd) {
	const createNewProcessGroup = 0x00000200
	const detachedProcess = 0x00000008
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess}
}

func terminationSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}
