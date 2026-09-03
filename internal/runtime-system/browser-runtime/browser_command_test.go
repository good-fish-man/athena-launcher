package browser_runtime

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBrowserCommandEnvironmentUsesShortPrivateSocketDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket paths are not used on Windows")
	}
	t.Setenv(agentBrowserSocketDirectoryEnv, "")
	environment := browserCommandEnvironment(filepath.Join(t.TempDir(), strings.Repeat("nested-path-", 20)))
	value := environmentValue(environment, agentBrowserSocketDirectoryEnv)
	if value == "" || !strings.HasPrefix(value, "/tmp/athena-browser-") || len(value) > 48 {
		t.Fatalf("socket directory is not short and isolated: %q", value)
	}
	info, err := os.Stat(value)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory permissions = %o", info.Mode().Perm())
	}
}

func TestBrowserCommandEnvironmentPreservesExplicitSocketDirectory(t *testing.T) {
	expected := filepath.Join(t.TempDir(), "custom-sockets")
	t.Setenv(agentBrowserSocketDirectoryEnv, expected)
	if got := environmentValue(browserCommandEnvironment(t.TempDir()), agentBrowserSocketDirectoryEnv); got != expected {
		t.Fatalf("socket directory = %q want=%q", got, expected)
	}
}

func TestConfiguredBrowserCommandEnvironmentUsesStablePersistence(t *testing.T) {
	t.Setenv(athenaAgentBrowserHomeEnv, "")
	t.Setenv(agentBrowserHomeEnv, "")
	t.Setenv(agentBrowserEncryptionKeyEnv, "ambient-key")
	dataDir := filepath.Join(t.TempDir(), "browser-data")
	environment := configuredBrowserCommandEnvironment(t.TempDir(), dataDir, "stable-key")
	for key, want := range map[string]string{
		agentBrowserHomeEnv:          dataDir,
		athenaAgentBrowserHomeEnv:    dataDir,
		agentBrowserEncryptionKeyEnv: "stable-key",
	} {
		if got := environmentValue(environment, key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
	}
}

func TestBrowserCommandEnvironmentBoundsManagedDaemonLifetime(t *testing.T) {
	t.Setenv("ATHENA_BROWSER_AUTH_MODE", browserAuthModeIsolated)
	t.Setenv(agentBrowserIdleTimeoutEnv, "")
	if got := environmentValue(browserCommandEnvironment(t.TempDir()), agentBrowserIdleTimeoutEnv); got != managedBrowserIdleTimeout {
		t.Fatalf("managed daemon idle timeout = %q, want %q", got, managedBrowserIdleTimeout)
	}
}

func TestBrowserCommandEnvironmentDoesNotAlterAutoConnectedChromeLifetime(t *testing.T) {
	t.Setenv("ATHENA_BROWSER_AUTH_MODE", browserAuthModeAutoConnect)
	t.Setenv(agentBrowserIdleTimeoutEnv, "")
	if got := environmentValue(browserCommandEnvironment(t.TempDir()), agentBrowserIdleTimeoutEnv); got != "" {
		t.Fatalf("auto-connect idle timeout = %q, want empty", got)
	}
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for index := len(environment) - 1; index >= 0; index-- {
		if strings.HasPrefix(environment[index], prefix) {
			return strings.TrimPrefix(environment[index], prefix)
		}
	}
	return ""
}
