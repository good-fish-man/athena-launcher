package state

import (
	"os"
	"path/filepath"
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
	if recovered.DBPassword != first.DBPassword || recovered.BrowserEncryptionKey != first.BrowserEncryptionKey || recovered.BackupEncryptionKey != first.BackupEncryptionKey || recovered.InternalServiceToken != first.InternalServiceToken || recovered.DeviceID != first.DeviceID {
		t.Fatalf("installation identity changed after state loss\nfirst=%+v\nrecovered=%+v", first, recovered)
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
