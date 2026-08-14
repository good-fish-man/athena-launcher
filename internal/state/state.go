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
	"strings"
)

const maxSecretFileSize = 4096

type State struct {
	LauncherPID          int               `json:"launcher_pid,omitempty"`
	ManifestSource       string            `json:"manifest_source,omitempty"`
	Version              string            `json:"version,omitempty"`
	ConnectionMode       string            `json:"connection_mode,omitempty"`
	RemoteClientURL      string            `json:"remote_client_url,omitempty"`
	RemoteDeviceToken    string            `json:"remote_device_token,omitempty"`
	DBPassword           string            `json:"db_password"`
	BrowserEncryptionKey string            `json:"browser_encryption_key"`
	BackupEncryptionKey  string            `json:"backup_encryption_key"`
	BrowserAuthMode      string            `json:"browser_auth_mode,omitempty"`
	BrowserProfile       string            `json:"browser_profile,omitempty"`
	InternalServiceToken string            `json:"internal_service_token"`
	DeviceID             string            `json:"device_id"`
	Installed            map[string]string `json:"installed,omitempty"`
}

func Load(home string) (*State, error) {
	if strings.TrimSpace(home) == "" {
		return nil, fmt.Errorf("launcher home is required")
	}
	path := filepath.Join(home, "state.json")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
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
	var reconcileErr error
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
	value.DeviceID, reconcileErr = reconcileSecret(home, "device-id", value.DeviceID, func() (string, error) {
		secret, err := randomSecret(16, "device id")
		return "device-" + secret, err
	})
	if reconcileErr != nil {
		return nil, reconcileErr
	}
	return value, nil
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
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("secret path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxSecretFileSize+1))
	if err != nil {
		return "", false, err
	}
	if len(data) > maxSecretFileSize {
		return "", false, fmt.Errorf("secret file exceeds %d bytes", maxSecretFileSize)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", false, fmt.Errorf("secret file is empty")
	}
	return value, true, nil
}

func writeSecret(path, value string) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(value+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func randomSecret(size int, label string) (string, error) {
	secret := make([]byte, size)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate %s: %w", label, err)
	}
	return hex.EncodeToString(secret), nil
}

func Save(home string, value *State) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	temporary := filepath.Join(home, "state.json.tmp")
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(home, "state.json"))
}
