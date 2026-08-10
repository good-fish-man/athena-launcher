// Package state persists Athena Launcher machine-local state and secrets.
package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type State struct {
	LauncherPID          int               `json:"launcher_pid,omitempty"`
	ManifestSource       string            `json:"manifest_source,omitempty"`
	Version              string            `json:"version,omitempty"`
	ConnectionMode       string            `json:"connection_mode,omitempty"`
	RemoteClientURL      string            `json:"remote_client_url,omitempty"`
	RemoteDeviceToken    string            `json:"remote_device_token,omitempty"`
	DBPassword           string            `json:"db_password"`
	BrowserEncryptionKey string            `json:"browser_encryption_key"`
	BrowserAuthMode      string            `json:"browser_auth_mode,omitempty"`
	BrowserProfile       string            `json:"browser_profile,omitempty"`
	InternalServiceToken string            `json:"internal_service_token"`
	DeviceID             string            `json:"device_id"`
	Installed            map[string]string `json:"installed,omitempty"`
}

func Load(home string) (*State, error) {
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
	if value.DBPassword == "" {
		secret, err := randomSecret(24, "database password")
		if err != nil {
			return nil, err
		}
		value.DBPassword = secret
	}
	if value.BrowserEncryptionKey == "" {
		secret, err := randomSecret(32, "browser vault encryption key")
		if err != nil {
			return nil, err
		}
		value.BrowserEncryptionKey = secret
	}
	if value.InternalServiceToken == "" {
		secret, err := randomSecret(32, "internal service token")
		if err != nil {
			return nil, err
		}
		value.InternalServiceToken = secret
	}
	if value.DeviceID == "" {
		secret, err := randomSecret(16, "device id")
		if err != nil {
			return nil, err
		}
		value.DeviceID = "device-" + secret
	}
	return value, nil
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
