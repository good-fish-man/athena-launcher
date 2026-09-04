//go:build windows

package deployment

import (
	"context"

	"golang.org/x/sys/windows"
)

func findPortOwner(ctx context.Context, port int) (portOwner, bool, error) {
	return portOwner{}, false, nil
}

func processStillExists(pid int) bool {
	const stillActive = 259
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode == stillActive
}
