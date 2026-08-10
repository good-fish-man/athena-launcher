package deployment

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func normalizeFrontendOverride(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve local frontend directory: %w", err)
	}
	if _, err := os.Stat(filepath.Join(abs, "index.html")); err != nil {
		return "", fmt.Errorf("local frontend directory %s does not contain index.html: %w", abs, err)
	}
	return abs, nil
}

func packageUpdatesForOptions(opts options, manifest *Manifest) []packageUpdate {
	updates := checkPackageUpdates(opts.home, manifest)
	if opts.frontendDir == "" {
		return updates
	}
	return withoutPackageComponent(updates, "frontend")
}

func frontendUpdatesForOptions(opts options, manifest *Manifest) []packageUpdate {
	if opts.frontendDir != "" {
		return nil
	}
	return checkFrontendUpdates(opts.home, manifest)
}

func withoutPackageComponent(updates []packageUpdate, component string) []packageUpdate {
	filtered := make([]packageUpdate, 0, len(updates))
	for _, update := range updates {
		if update.Component != component {
			filtered = append(filtered, update)
		}
	}
	return filtered
}
