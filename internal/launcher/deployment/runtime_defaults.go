package deployment

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const (
	defaultManifestName    = "release-manifest.json"
	defaultDatabaseName    = "agent_runtime"
	defaultDatabaseUser    = "athena"
	defaultDatabasePort    = uint32(15432)
	envDatabasePort        = "ATHENA_DATABASE_PORT"
	defaultRuntimeGRPCPort = 18080
	defaultRuntimeHTTPPort = 18081
	defaultClientHTTPPort  = 8090
	defaultFrontendPort    = 3000
	defaultStartupPort     = 17890
	publicManifestURL      = "https://github.com/good-fish-man/athena-launcher/releases/latest/download/release-manifest.json"
	// This is the trust root for source and development builds that consume the
	// official remote manifest. Rotate it only through a reviewed source change.
	pinnedReleasePublicKey = "w0I6sF+Xs3SotTRIJkMtCpuiBvkjeINpJhILEKn/nUE"
)

// Release metadata can be injected at build time with -ldflags.
var (
	LauncherVersion         = "1.0.0"
	DefaultManifestURL      = publicManifestURL
	DefaultReleasePublicKey = pinnedReleasePublicKey
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

func databasePort() uint32 {
	port, err := configuredPort(envDatabasePort, defaultDatabasePort)
	if err != nil {
		return defaultDatabasePort
	}
	return port
}

func validateRuntimeOverrides() error {
	_, err := configuredPort(envDatabasePort, defaultDatabasePort)
	return err
}

func configuredPort(name string, fallback uint32) (uint32, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 16)
	if err != nil || parsed == 0 {
		return 0, fmt.Errorf("%s must be an integer between 1 and 65535", name)
	}
	return uint32(parsed), nil
}
