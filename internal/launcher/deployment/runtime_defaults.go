package deployment

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	defaultManifestName    = "release-manifest.json"
	defaultDatabaseName    = "agent_runtime"
	defaultDatabaseUser    = "athena"
	defaultDatabasePort    = uint32(15432)
	defaultRuntimeGRPCPort = 18080
	defaultRuntimeHTTPPort = 18081
	defaultClientHTTPPort  = 8090
	defaultFrontendPort    = 3000
	defaultStartupPort     = 17890
	publicManifestURL      = "https://github.com/good-fish-man/athena-launcher/releases/latest/download/release-manifest.json"
)

// Release metadata can be injected at build time with -ldflags.
var (
	LauncherVersion         = "0.9.0"
	DefaultManifestURL      = publicManifestURL
	DefaultReleasePublicKey = ""
)

func defaultHome() (string, error) {
	if value := strings.TrimSpace(os.Getenv("ATHENA_HOME")); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".athena"), nil
}

func defaultManifest(home string) string {
	if value := strings.TrimSpace(os.Getenv("ATHENA_MANIFEST_URL")); value != "" {
		return value
	}
	if DefaultManifestURL != "" {
		return DefaultManifestURL
	}
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), defaultManifestName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return filepath.Join(home, defaultManifestName)
}

func platformKey() string { return runtime.GOOS + "-" + runtime.GOARCH }
