// Package release defines and validates Athena release manifests.
package release

import (
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

type Manifest struct {
	Version  string        `json:"version"`
	Database DatabaseSpec  `json:"database"`
	Browser  *BrowserSpec  `json:"browser,omitempty"`
	Services []ServiceSpec `json:"services"`
	Frontend *FrontendSpec `json:"frontend,omitempty"`
}

type BrowserSpec struct {
	Version   string              `json:"version"`
	Artifacts map[string]Artifact `json:"artifacts"`
}

type FrontendSpec struct {
	ListenAddr string              `json:"listen_addr,omitempty"`
	Root       string              `json:"root,omitempty"`
	Artifacts  map[string]Artifact `json:"artifacts"`
}

type DatabaseSpec struct {
	Version   string              `json:"version"`
	BinDir    string              `json:"bin_dir,omitempty"`
	Artifacts map[string]Artifact `json:"artifacts"`
}

type ServiceSpec struct {
	Name      string              `json:"name"`
	Order     int                 `json:"order"`
	Args      []string            `json:"args,omitempty"`
	Env       map[string]string   `json:"env,omitempty"`
	HealthURL string              `json:"health_url"`
	Artifacts map[string]Artifact `json:"artifacts"`
}

type Artifact struct {
	URL        string `json:"url"`
	SHA256     string `json:"sha256"`
	Format     string `json:"format,omitempty"`
	Executable string `json:"executable"`
}

// Validate verifies that a manifest is complete and safe for platform.
func (m *Manifest) Validate(platform string) error {
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("manifest version is required")
	}
	if len(m.Services) == 0 {
		return fmt.Errorf("manifest services are required")
	}
	if strings.TrimSpace(platform) == "" {
		return fmt.Errorf("manifest platform is required")
	}
	if m.Database.Version == "" {
		return fmt.Errorf("manifest database version is required")
	}
	databaseArtifact, ok := m.Database.Artifacts[platform]
	if !ok {
		return fmt.Errorf("database does not provide an artifact for %s", platform)
	}
	if databaseArtifact.URL == "" || databaseArtifact.SHA256 == "" {
		return fmt.Errorf("database artifact for %s requires url and sha256", platform)
	}
	if err := validateArtifact(databaseArtifact, false); err != nil {
		return fmt.Errorf("database artifact for %s: %w", platform, err)
	}
	for candidatePlatform, artifact := range m.Database.Artifacts {
		if err := validateArtifact(artifact, false); err != nil {
			return fmt.Errorf("database artifact for %s: %w", candidatePlatform, err)
		}
	}
	if m.Browser != nil {
		if strings.TrimSpace(m.Browser.Version) == "" {
			return fmt.Errorf("manifest browser version is required")
		}
		artifact, ok := m.Browser.Artifacts[platform]
		if !ok {
			return fmt.Errorf("browser does not provide an artifact for %s", platform)
		}
		if err := validateArtifact(artifact, true); err != nil {
			return fmt.Errorf("browser artifact for %s: %w", platform, err)
		}
		for candidatePlatform, candidate := range m.Browser.Artifacts {
			if err := validateArtifact(candidate, true); err != nil {
				return fmt.Errorf("browser artifact for %s: %w", candidatePlatform, err)
			}
		}
	}
	if err := validateRelativePath(m.Database.BinDir, true); err != nil {
		return fmt.Errorf("database bin_dir: %w", err)
	}
	seen := make(map[string]struct{}, len(m.Services))
	for _, service := range m.Services {
		if service.Name == "" {
			return fmt.Errorf("service name is required")
		}
		if _, exists := seen[service.Name]; exists {
			return fmt.Errorf("duplicate service %q", service.Name)
		}
		seen[service.Name] = struct{}{}
		artifact, ok := service.Artifacts[platform]
		if !ok {
			return fmt.Errorf("service %s does not provide an artifact for %s", service.Name, platform)
		}
		if artifact.URL == "" || artifact.SHA256 == "" || artifact.Executable == "" {
			return fmt.Errorf("service %s artifact for %s requires url, sha256, and executable", service.Name, platform)
		}
		if err := validateArtifact(artifact, true); err != nil {
			return fmt.Errorf("service %s artifact for %s: %w", service.Name, platform, err)
		}
		for candidatePlatform, candidate := range service.Artifacts {
			if err := validateArtifact(candidate, true); err != nil {
				return fmt.Errorf("service %s artifact for %s: %w", service.Name, candidatePlatform, err)
			}
		}
	}
	if m.Frontend != nil {
		artifact, ok := m.Frontend.Artifacts[platform]
		if !ok {
			return fmt.Errorf("frontend does not provide an artifact for %s", platform)
		}
		if err := validateArtifact(artifact, false); err != nil {
			return fmt.Errorf("frontend artifact for %s: %w", platform, err)
		}
		for candidatePlatform, candidate := range m.Frontend.Artifacts {
			if err := validateArtifact(candidate, false); err != nil {
				return fmt.Errorf("frontend artifact for %s: %w", candidatePlatform, err)
			}
		}
		if err := validateRelativePath(m.Frontend.Root, true); err != nil {
			return fmt.Errorf("frontend root: %w", err)
		}
	}
	sort.SliceStable(m.Services, func(i, j int) bool { return m.Services[i].Order < m.Services[j].Order })
	return nil
}

func validateArtifact(artifact Artifact, executableRequired bool) error {
	if strings.TrimSpace(artifact.URL) == "" {
		return fmt.Errorf("url is required")
	}
	checksum := strings.TrimSpace(artifact.SHA256)
	decoded, err := hex.DecodeString(checksum)
	if err != nil || len(decoded) != sha256Size {
		return fmt.Errorf("sha256 must be 64 hexadecimal characters")
	}
	if executableRequired {
		if err := validateRelativePath(artifact.Executable, false); err != nil {
			return fmt.Errorf("executable: %w", err)
		}
	}
	switch strings.ToLower(strings.TrimSpace(artifact.Format)) {
	case "", "raw", "zip", "tar.gz", "tgz":
	default:
		return fmt.Errorf("unsupported format %q", artifact.Format)
	}
	return nil
}

const sha256Size = 32

func validateRelativePath(value string, allowEmpty bool) error {
	value = filepath.FromSlash(strings.TrimSpace(value))
	if value == "" && allowEmpty {
		return nil
	}
	clean := filepath.Clean(value)
	if clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("must be a safe relative path")
	}
	return nil
}
