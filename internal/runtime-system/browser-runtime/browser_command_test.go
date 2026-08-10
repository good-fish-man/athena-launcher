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

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for index := len(environment) - 1; index >= 0; index-- {
		if strings.HasPrefix(environment[index], prefix) {
			return strings.TrimPrefix(environment[index], prefix)
		}
	}
	return ""
}
