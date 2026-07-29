package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const installedManifestName = "installed-manifest.json"

var errUpdateRequested = errors.New("approved package update requested")

type packageUpdate struct {
	Component   string `json:"component"`
	DisplayName string `json:"displayName"`
	CurrentHash string `json:"currentHash"`
	RemoteHash  string `json:"remoteHash"`
}

func checkPackageUpdates(home string, manifest *Manifest) []packageUpdate {
	if manifest == nil {
		return nil
	}
	platform := platformKey()
	updates := make([]packageUpdate, 0)
	databaseArtifact := manifest.Database.Artifacts[platform]
	updates = appendPackageUpdate(updates, "postgres", "PostgreSQL", filepath.Join(home, "postgres"), databaseArtifactMarker(databaseArtifact.SHA256))
	for _, service := range manifest.Services {
		artifact := service.Artifacts[platform]
		updates = appendPackageUpdate(updates, service.Name, service.Name, filepath.Join(home, "services", service.Name), artifact.SHA256)
	}
	if manifest.Frontend != nil {
		artifact := manifest.Frontend.Artifacts[platform]
		updates = appendPackageUpdate(updates, "frontend", "Athena UI", filepath.Join(home, "frontend"), artifact.SHA256)
	}
	return updates
}

func appendPackageUpdate(updates []packageUpdate, component, displayName, root, remoteHash string) []packageUpdate {
	remoteHash = strings.ToLower(strings.TrimSpace(remoteHash))
	markers, _ := filepath.Glob(filepath.Join(root, "*", ".artifact-sha256"))
	currentHash := ""
	for _, marker := range markers {
		data, err := os.ReadFile(marker)
		if err != nil {
			continue
		}
		value := strings.ToLower(strings.TrimSpace(string(data)))
		if value == remoteHash {
			return updates
		}
		if currentHash == "" {
			currentHash = value
		}
	}
	if currentHash == "" {
		return updates
	}
	return append(updates, packageUpdate{Component: component, DisplayName: displayName, CurrentHash: currentHash, RemoteHash: remoteHash})
}

func saveInstalledManifest(home string, manifest *Manifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(home, installedManifestName)
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("save installed manifest: %w", err)
	}
	return nil
}

func loadInstalledManifest(home string) (*Manifest, error) {
	path := filepath.Join(home, installedManifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read installed manifest: %w", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse installed manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
		return nil, fmt.Errorf("validate installed manifest: %w", err)
	}
	return &manifest, nil
}

func installedPackagesUsable(home string, manifest *Manifest) bool {
	if manifest == nil {
		return false
	}
	platform := platformKey()
	databaseArtifact := manifest.Database.Artifacts[platform]
	databaseRoot := findArtifactRootByMarker(filepath.Join(home, "postgres"), databaseArtifactMarker(databaseArtifact.SHA256))
	if databaseRoot == "" {
		return false
	}
	if err := newManagedDatabase(home, databaseRoot, manifest.Database.BinDir, "").validateBinaries(); err != nil {
		return false
	}
	for _, service := range manifest.Services {
		artifact := service.Artifacts[platform]
		root := findArtifactRootByMarker(filepath.Join(home, "services", service.Name), artifact.SHA256)
		if root == "" {
			return false
		}
		if _, err := findInstalledExecutable(root, filepath.Base(filepath.FromSlash(artifact.Executable))); err != nil {
			return false
		}
	}
	if manifest.Frontend != nil {
		artifact := manifest.Frontend.Artifacts[platform]
		root := findArtifactRootByMarker(filepath.Join(home, "frontend"), artifact.SHA256)
		if root == "" {
			return false
		}
		if _, err := os.Stat(filepath.Join(frontendRoot(root, manifest.Frontend.Root), "index.html")); err != nil {
			return false
		}
	}
	return true
}

func shortHash(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
