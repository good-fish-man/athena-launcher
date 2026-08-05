//go:build !desktop

package main

import (
	"fmt"
	"path/filepath"
	"time"
)

// launchDesktop keeps browser-based behaviour for headless and CLI builds.
// Release installers are compiled with the desktop tag and use Wails instead.
func launchDesktop(opts options) error {
	startupAddress := fmt.Sprintf("http://127.0.0.1:%d/", defaultStartupPort)
	if startupCenterHealthy() {
		requestStartupUpdateCheck()
		return openBrowser(startupAddress)
	}
	if err := startDetached(opts); err != nil {
		return err
	}
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if startupCenterHealthy() {
			return openBrowser(startupAddress)
		}
		select {
		case <-deadline.C:
			return fmt.Errorf("Athena startup center did not open within 30 seconds; inspect %s", filepath.Join(opts.home, "logs", "launcher.log"))
		case <-ticker.C:
		}
	}
}
