// Package state persists Athena Launcher machine-local state and secrets.
package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	maxSecretFileSize = 4096
	maxStateFileSize  = 1 << 20
)

type State struct {
	LauncherPID            int               `json:"launcher_pid,omitempty"`
	LauncherVersion        string            `json:"launcher_version,omitempty"`
	ManifestSource         string            `json:"manifest_source,omitempty"`
	Version                string            `json:"version,omitempty"`
	ConnectionMode         string            `json:"connection_mode,omitempty"`
	DeploymentConfigured   bool              `json:"deployment_configured,omitempty"`
	RemoteClientURL        string            `json:"remote_client_url,omitempty"`
	RemoteDeviceToken      string            `json:"remote_device_token,omitempty"`
	DBPassword             string            `json:"db_password"`
	BrowserEncryptionKey   string            `json:"browser_encryption_key"`
	BrowserDataDir         string            `json:"browser_data_dir"`
	BackupEncryptionKey    string            `json:"backup_encryption_key"`
	BrowserAuthMode        string            `json:"browser_auth_mode,omitempty"`
	BrowserProfile         string            `json:"browser_profile,omitempty"`
	InternalServiceToken   string            `json:"internal_service_token"`
	BootstrapAdminPassword string            `json:"-"`
	DeviceID               string            `json:"device_id"`
	Installed              map[string]string `json:"installed,omitempty"`
}

func Load(home string) (*State, error) {
	if strings.TrimSpace(home) == "" {
		return nil, fmt.Errorf("launcher home is required")
	}
	path := filepath.Join(home, "state.json")
	data, exists, err := readProtectedFile(path, maxStateFileSize, "launcher state")
	if err != nil {
		return nil, err
	}
	if !exists {
		data = nil
	}
	value := &State{Installed: make(map[string]string)}
	if len(data) > 0 {
		if err := json.Unmarshal(data, value); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if value.Installed == nil {
		value.Installed = make(map[string]string)
	}
	if strings.TrimSpace(value.BrowserDataDir) != "" {
		value.BrowserDataDir, err = resolveBrowserDataDir(value.BrowserDataDir)
		if err != nil {
			return nil, err
		}
	}
	var reconcileErr error
	value.BrowserDataDir, reconcileErr = reconcileSecret(home, "browser-data.path", value.BrowserDataDir, func() (string, error) {
		return resolveBrowserDataDir("")
	})
	if reconcileErr != nil {
		return nil, reconcileErr
	}
	value.DBPassword, reconcileErr = reconcileSecret(home, "database-password", value.DBPassword, func() (string, error) {
		return randomSecret(24, "database password")
	})
	if reconcileErr != nil {
		return nil, reconcileErr
	}
	value.BrowserEncryptionKey, reconcileErr = reconcileSecret(home, "browser-vault.key", value.BrowserEncryptionKey, func() (string, error) {
		return randomSecret(32, "browser vault encryption key")
	})
	if reconcileErr != nil {
		return nil, reconcileErr
	}
	value.BackupEncryptionKey, reconcileErr = reconcileSecret(home, "backup.key", value.BackupEncryptionKey, func() (string, error) {
		return randomSecret(32, "backup encryption key")
	})
	if reconcileErr != nil {
		return nil, reconcileErr
	}
	value.InternalServiceToken, reconcileErr = reconcileSecret(home, "internal-service.token", value.InternalServiceToken, func() (string, error) {
		return randomSecret(32, "internal service token")
	})
	if reconcileErr != nil {
		return nil, reconcileErr
	}
	value.BootstrapAdminPassword, reconcileErr = reconcileSecret(home, "bootstrap-admin.password", value.BootstrapAdminPassword, func() (string, error) {
		return randomSecret(24, "bootstrap administrator password")
	})
	if reconcileErr != nil {
		return nil, reconcileErr
	}
	value.DeviceID, reconcileErr = reconcileSecret(home, "device-id", value.DeviceID, func() (string, error) {
		secret, err := randomSecret(16, "device id")
		return "device-" + secret, err
	})
	if reconcileErr != nil {
		return nil, reconcileErr
	}
	return value, nil
}

func resolveBrowserDataDir(current string) (string, error) {
	current = strings.TrimSpace(current)
	if current == "" {
		for _, key := range []string{"ATHENA_AGENT_BROWSER_HOME", "AGENT_BROWSER_HOME"} {
			if candidate := strings.TrimSpace(os.Getenv(key)); candidate != "" {
				current = candidate
				break
			}
		}
	}
	if current == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve agent-browser data directory: %w", err)
		}
		current = filepath.Join(home, ".agent-browser")
	}
	absolute, err := filepath.Abs(current)
	if err != nil {
		return "", fmt.Errorf("resolve agent-browser data directory %q: %w", current, err)
	}
	return filepath.Clean(absolute), nil
}

func reconcileSecret(home, name, current string, generate func() (string, error)) (string, error) {
	directory := filepath.Join(home, "secrets")
	path := filepath.Join(directory, name)
	persisted, exists, err := readSecret(path)
	if err != nil {
		return "", fmt.Errorf("read recovery secret %s: %w", name, err)
	}
	current = strings.TrimSpace(current)
	if exists {
		if current != "" && current != persisted {
			return "", fmt.Errorf("state and recovery secret %s disagree; refusing to rotate installation identity", name)
		}
		return persisted, nil
	}
	if current == "" {
		current, err = generate()
		if err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("create recovery secret directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", fmt.Errorf("protect recovery secret directory: %w", err)
	}
	if err := writeSecret(path, current); err != nil {
		return "", fmt.Errorf("persist recovery secret %s: %w", name, err)
	}
	return current, nil
}

func readSecret(path string) (string, bool, error) {
	data, exists, err := readProtectedFile(path, maxSecretFileSize, "recovery secret")
	if err != nil || !exists {
		return "", exists, err
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", false, fmt.Errorf("secret file is empty")
	}
	return value, true, nil
}

func readProtectedFile(path string, limit int64, label string) ([]byte, bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("%s path is not a regular file", label)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, false, fmt.Errorf("%s permissions must not allow group or other access", label)
	}
	if info.Size() > limit {
		return nil, false, fmt.Errorf("%s file exceeds %d bytes", label, limit)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, false, fmt.Errorf("%s changed while it was being opened", label)
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > limit {
		return nil, false, fmt.Errorf("%s file exceeds %d bytes", label, limit)
	}
	return data, true, nil
}

func writeSecret(path, value string) error {
	return writeProtectedFile(path, []byte(value+"\n"))
}

func randomSecret(size int, label string) (string, error) {
	secret := make([]byte, size)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate %s: %w", label, err)
	}
	return hex.EncodeToString(secret), nil
}

func Save(home string, value *State) error {
	if value == nil {
		return fmt.Errorf("launcher state is required")
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(home, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeProtectedFile(filepath.Join(home, "state.json"), data)
}

func writeProtectedFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		directoryFile, err := os.Open(directory)
		if err != nil {
			return err
		}
		syncErr := directoryFile.Sync()
		closeErr := directoryFile.Close()
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
