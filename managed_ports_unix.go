//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func findPortOwner(ctx context.Context, port int) (portOwner, bool, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(requestCtx, "lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-FpPc").CombinedOutput()
	if err != nil {
		if exitErr := new(exec.ExitError); errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return portOwner{}, false, nil
		}
		return portOwner{}, false, fmt.Errorf("lsof: %w: %s", err, strings.TrimSpace(string(output)))
	}
	owner := parseLsofOwner(string(output))
	if owner.PID == 0 {
		return portOwner{}, false, nil
	}
	owner.Args = processArgs(ctx, owner.PID)
	return owner, true, nil
}

func parseLsofOwner(output string) portOwner {
	var owner portOwner
	for _, line := range strings.Split(output, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ := strconv.Atoi(strings.TrimSpace(line[1:]))
			if owner.PID == 0 {
				owner.PID = pid
			}
		case 'c':
			if owner.Command == "" {
				owner.Command = strings.TrimSpace(line[1:])
			}
		}
	}
	return owner
}

func processArgs(ctx context.Context, pid int) string {
	requestCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	output, err := exec.CommandContext(requestCtx, "ps", "-p", strconv.Itoa(pid), "-o", "args=").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func processStillExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
