//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"runtime"
)

func openBrowser(address string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	if err := exec.Command(command, address).Start(); err != nil {
		return fmt.Errorf("open Athena in the default browser: %w", err)
	}
	return nil
}
