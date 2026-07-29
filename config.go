package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

const (
	defaultManifestName    = "release-manifest.json"
	defaultDatabaseName    = "agent_runtime"
	defaultDatabaseUser    = "athena"
	defaultDatabasePort    = uint32(15432)
	defaultRuntimeGRPCPort = 18080
	defaultRuntimeHTTPPort = 18081
	defaultClientHTTPPort  = 8090
	defaultFrontendPort    = 3000
	defaultStartupPort     = 17890
)

// Release metadata can be injected at build time with -ldflags.
var (
	launcherVersion    = "0.1.1"
	defaultManifestURL string
)

type Manifest struct {
	Version  string        `json:"version"`
	Database DatabaseSpec  `json:"database"`
	Services []ServiceSpec `json:"services"`
	Frontend *FrontendSpec `json:"frontend,omitempty"`
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

type launcherState struct {
	LauncherPID    int               `json:"launcher_pid,omitempty"`
	ManifestSource string            `json:"manifest_source,omitempty"`
	Version        string            `json:"version,omitempty"`
	DBPassword     string            `json:"db_password"`
	Installed      map[string]string `json:"installed,omitempty"`
}

type options struct {
	command        string
	home           string
	manifestSource string
	foreground     bool
}

func defaultHome() (string, error) {
	if value := strings.TrimSpace(os.Getenv("ATHENA_HOME")); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".athena"), nil
}

func defaultManifest(home string) string {
	if value := strings.TrimSpace(os.Getenv("ATHENA_MANIFEST_URL")); value != "" {
		return value
	}
	if defaultManifestURL != "" {
		return defaultManifestURL
	}
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), defaultManifestName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return filepath.Join(home, defaultManifestName)
}

func platformKey() string { return runtime.GOOS + "-" + runtime.GOARCH }

func (m *Manifest) validate() error {
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("manifest version is required")
	}
	if len(m.Services) == 0 {
		return fmt.Errorf("manifest services are required")
	}
	key := platformKey()
	if m.Database.Version == "" {
		return fmt.Errorf("manifest database version is required")
	}
	databaseArtifact, ok := m.Database.Artifacts[key]
	if !ok {
		return fmt.Errorf("database does not provide an artifact for %s", key)
	}
	if databaseArtifact.URL == "" || databaseArtifact.SHA256 == "" {
		return fmt.Errorf("database artifact for %s requires url and sha256", key)
	}
	if err := validateArtifact(databaseArtifact, false); err != nil {
		return fmt.Errorf("database artifact for %s: %w", key, err)
	}
	for platform, artifact := range m.Database.Artifacts {
		if err := validateArtifact(artifact, false); err != nil {
			return fmt.Errorf("database artifact for %s: %w", platform, err)
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
		artifact, ok := service.Artifacts[key]
		if !ok {
			return fmt.Errorf("service %s does not provide an artifact for %s", service.Name, key)
		}
		if artifact.URL == "" || artifact.SHA256 == "" || artifact.Executable == "" {
			return fmt.Errorf("service %s artifact for %s requires url, sha256, and executable", service.Name, key)
		}
		if err := validateArtifact(artifact, true); err != nil {
			return fmt.Errorf("service %s artifact for %s: %w", service.Name, key, err)
		}
		for platform, candidate := range service.Artifacts {
			if err := validateArtifact(candidate, true); err != nil {
				return fmt.Errorf("service %s artifact for %s: %w", service.Name, platform, err)
			}
		}
	}
	if m.Frontend != nil {
		artifact, ok := m.Frontend.Artifacts[key]
		if !ok {
			return fmt.Errorf("frontend does not provide an artifact for %s", key)
		}
		if err := validateArtifact(artifact, false); err != nil {
			return fmt.Errorf("frontend artifact for %s: %w", key, err)
		}
		for platform, candidate := range m.Frontend.Artifacts {
			if err := validateArtifact(candidate, false); err != nil {
				return fmt.Errorf("frontend artifact for %s: %w", platform, err)
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

func loadState(home string) (*launcherState, error) {
	path := filepath.Join(home, "state.json")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	state := &launcherState{Installed: make(map[string]string)}
	if len(data) > 0 {
		if err := json.Unmarshal(data, state); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if state.Installed == nil {
		state.Installed = make(map[string]string)
	}
	if state.DBPassword == "" {
		secret := make([]byte, 24)
		if _, err := rand.Read(secret); err != nil {
			return nil, fmt.Errorf("generate database password: %w", err)
		}
		state.DBPassword = hex.EncodeToString(secret)
	}
	return state, nil
}

func saveState(home string, state *launcherState) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary := filepath.Join(home, "state.json.tmp")
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, filepath.Join(home, "state.json"))
}
