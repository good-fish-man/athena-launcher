package deployment

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
	if manifest.Browser != nil {
		artifact := manifest.Browser.Artifacts[platform]
		updates = appendPackageUpdate(updates, "agent-browser", "Agent Browser", filepath.Join(home, "browser"), artifact.SHA256)
	}
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

func checkFrontendUpdates(home string, manifest *Manifest) []packageUpdate {
	if manifest == nil || manifest.Frontend == nil {
		return nil
	}
	artifact := manifest.Frontend.Artifacts[platformKey()]
	return appendPackageUpdate(nil, "frontend", "Athena UI", filepath.Join(home, "frontend"), artifact.SHA256)
}

func installedFrontendUsable(home string, manifest *Manifest) bool {
	if manifest == nil || manifest.Frontend == nil {
		return false
	}
	artifact := manifest.Frontend.Artifacts[platformKey()]
	root := findArtifactRootByMarker(filepath.Join(home, "frontend"), artifact.SHA256)
	if root == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(frontendRoot(root, manifest.Frontend.Root), "index.html"))
	return err == nil
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
	if err := manifest.Validate(platformKey()); err != nil {
		return nil, fmt.Errorf("validate installed manifest: %w", err)
	}
	if !manifest.Development {
		publicKey, err := configuredReleasePublicKey()
		if err != nil {
			return nil, fmt.Errorf("verify installed manifest identity: %w", err)
		}
		if err := manifest.Verify(publicKey, manifest.IssuedAt); err != nil {
			return nil, fmt.Errorf("verify installed manifest: %w", err)
		}
	}
	return &manifest, nil
}

func installedPackagesUsable(home string, manifest *Manifest) bool {
	return validateInstalledPackages(home, manifest) == nil
}

func validateInstalledPackages(home string, manifest *Manifest) error {
	if manifest == nil {
		return fmt.Errorf("installed manifest is unavailable")
	}
	platform := platformKey()
	databaseArtifact := manifest.Database.Artifacts[platform]
	databaseRoot := findArtifactRootByMarker(filepath.Join(home, "postgres"), databaseArtifactMarker(databaseArtifact.SHA256))
	if databaseRoot == "" {
		return fmt.Errorf("installed PostgreSQL artifact is missing or has a different checksum")
	}
	if err := newManagedDatabase(home, databaseRoot, manifest.Database.BinDir, "").validateBinaries(); err != nil {
		return fmt.Errorf("installed PostgreSQL is incomplete: %w", err)
	}
	if manifest.Browser != nil {
		artifact := manifest.Browser.Artifacts[platform]
		root := findArtifactRootByMarker(filepath.Join(home, "browser"), artifact.SHA256)
		if root == "" {
			return fmt.Errorf("installed browser artifact is missing or has a different checksum")
		}
		if _, err := findInstalledExecutable(root, filepath.Base(filepath.FromSlash(artifact.Executable))); err != nil {
			return fmt.Errorf("installed browser executable is unavailable: %w", err)
		}
	}
	for _, service := range manifest.Services {
		artifact := service.Artifacts[platform]
		root := findArtifactRootByMarker(filepath.Join(home, "services", service.Name), artifact.SHA256)
		if root == "" {
			return fmt.Errorf("installed service %s is missing or has a different checksum", service.Name)
		}
		if _, err := findInstalledExecutable(root, filepath.Base(filepath.FromSlash(artifact.Executable))); err != nil {
			return fmt.Errorf("installed service %s executable is unavailable: %w", service.Name, err)
		}
	}
	if manifest.Frontend != nil {
		artifact := manifest.Frontend.Artifacts[platform]
		root := findArtifactRootByMarker(filepath.Join(home, "frontend"), artifact.SHA256)
		if root == "" {
			return fmt.Errorf("installed frontend artifact is missing or has a different checksum")
		}
		if _, err := os.Stat(filepath.Join(frontendRoot(root, manifest.Frontend.Root), "index.html")); err != nil {
			return fmt.Errorf("installed frontend entrypoint is unavailable: %w", err)
		}
	}
	return nil
}

func shortHash(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
