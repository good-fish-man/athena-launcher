//go:build windows

package main

import (
	"context"
)

func findPortOwner(ctx context.Context, port int) (portOwner, bool, error) {
	return portOwner{}, false, nil
}

func processStillExists(pid int) bool {
	return false
}
