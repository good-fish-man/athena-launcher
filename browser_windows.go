//go:build windows

package main

import (
	"fmt"
	"os/exec"
)

func openBrowser(address string) error {
	if err := exec.Command("rundll32", "url.dll,FileProtocolHandler", address).Start(); err != nil {
		return fmt.Errorf("open Athena in the default browser: %w", err)
	}
	return nil
}
