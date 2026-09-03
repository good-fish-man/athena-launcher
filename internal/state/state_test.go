package state

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadRecoversInstallationIdentityAfterStateLoss(t *testing.T) {
	home := t.TempDir()
	first, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(home, first); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(home, "state.json")); err != nil {
		t.Fatal(err)
	}
	recovered, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.DBPassword != first.DBPassword || recovered.BrowserEncryptionKey != first.BrowserEncryptionKey || recovered.BrowserDataDir != first.BrowserDataDir || recovered.BackupEncryptionKey != first.BackupEncryptionKey || recovered.InternalServiceToken != first.InternalServiceToken || recovered.BootstrapAdminPassword != first.BootstrapAdminPassword || recovered.DeviceID != first.DeviceID {
		t.Fatal("one or more protected installation identities changed after state loss")
	}
}

func TestBrowserPersistenceSurvivesPackageUpgrade(t *testing.T) {
	launcherHome := t.TempDir()
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv("ATHENA_AGENT_BROWSER_HOME", "")
	t.Setenv("AGENT_BROWSER_HOME", "")

	before, err := Load(launcherHome)
	if err != nil {
		t.Fatal(err)
	}
	wantDataDir := filepath.Join(userHome, ".agent-browser")
	if before.BrowserDataDir != wantDataDir {
		t.Fatalf("browser data directory = %q, want %q", before.BrowserDataDir, wantDataDir)
	}
	before.Installed["agent-browser"] = "0.33.1"
	if err := Save(launcherHome, before); err != nil {
		t.Fatal(err)
	}

	upgraded, err := Load(launcherHome)
	if err != nil {
		t.Fatal(err)
	}
	upgraded.Installed["agent-browser"] = "0.34.0"
	if err := Save(launcherHome, upgraded); err != nil {
		t.Fatal(err)
	}
	after, err := Load(launcherHome)
	if err != nil {
		t.Fatal(err)
	}
	if after.BrowserDataDir != before.BrowserDataDir {
		t.Fatalf("browser data directory changed across upgrade: before=%q after=%q", before.BrowserDataDir, after.BrowserDataDir)
	}
	if after.BrowserEncryptionKey != before.BrowserEncryptionKey {
		t.Fatal("browser encryption key changed across upgrade")
	}
}

func TestLoadRecoversBrowserDataDirWhenStateAndUserHomeChange(t *testing.T) {
	launcherHome := t.TempDir()
	originalUserHome := t.TempDir()
	t.Setenv("HOME", originalUserHome)
	t.Setenv("ATHENA_AGENT_BROWSER_HOME", "")
	t.Setenv("AGENT_BROWSER_HOME", "")
	before, err := Load(launcherHome)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(launcherHome, before); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(launcherHome, "state.json")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	after, err := Load(launcherHome)
	if err != nil {
		t.Fatal(err)
	}
	if after.BrowserDataDir != before.BrowserDataDir {
		t.Fatalf("persisted browser data directory changed: before=%q after=%q", before.BrowserDataDir, after.BrowserDataDir)
	}
}

func TestSaveKeepsBootstrapAdministratorPasswordOutOfStateJSON(t *testing.T) {
	home := t.TempDir()
	value, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(home, value); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(home, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), value.BootstrapAdminPassword) {
		t.Fatal("bootstrap administrator password leaked into state.json")
	}
	secret, err := os.ReadFile(filepath.Join(home, "secrets", "bootstrap-admin.password"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(secret)) != value.BootstrapAdminPassword {
		t.Fatal("protected bootstrap administrator password was not persisted")
	}
}

func TestLoadRejectsStateAndRecoverySecretConflict(t *testing.T) {
	home := t.TempDir()
	state, err := Load(home)
	if err != nil {
		t.Fatal(err)
	}
	state.BackupEncryptionKey = "different-key"
	if err := Save(home, state); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err == nil {
		t.Fatal("conflicting recovery identity was accepted")
	}
}

func TestLoadRejectsSymlinkedRecoverySecret(t *testing.T) {
	home := t.TempDir()
	secretDir := filepath.Join(home, "secrets")
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, "outside")
	if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(secretDir, "database-password")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Load(home); err == nil {
		t.Fatal("symlinked recovery secret was accepted")
	}
}

func TestLoadRejectsInsecureStatePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions are not available")
	}
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "state.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(home); err == nil {
		t.Fatal("world-readable launcher state was accepted")
	}
}

func TestLoadRejectsSymlinkedState(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(home, "outside-state")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, "state.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Load(home); err == nil {
		t.Fatal("symlinked launcher state was accepted")
	}
}
