// Package release defines and validates Athena release manifests.
package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	operationsv1 "github.com/good-fish-man/athena-protocol/protocol/operations/v1"
)

const (
	ManifestSchema  = "athena.release-manifest.v1"
	ProtocolVersion = "athena.operations.v1"
)

var semverPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)

type Signature = operationsv1.Signature

type Manifest struct {
	Schema             string        `json:"schema"`
	ReleaseID          string        `json:"release_id"`
	Version            string        `json:"version"`
	ProtocolVersion    string        `json:"protocol_version"`
	MinimumFromVersion string        `json:"minimum_from_version"`
	Development        bool          `json:"development,omitempty"`
	SBOMURL            string        `json:"sbom_url"`
	SBOMSHA256         string        `json:"sbom_sha256"`
	Signature          Signature     `json:"signature"`
	IssuedAt           time.Time     `json:"issued_at"`
	ExpiresAt          time.Time     `json:"expires_at"`
	Database           DatabaseSpec  `json:"database"`
	Browser            *BrowserSpec  `json:"browser,omitempty"`
	Services           []ServiceSpec `json:"services"`
	Frontend           *FrontendSpec `json:"frontend,omitempty"`
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
	URL         string    `json:"url"`
	SHA256      string    `json:"sha256"`
	SBOMSHA256  string    `json:"sbom_sha256"`
	Signature   Signature `json:"signature"`
	CodeSigning string    `json:"code_signing"`
	Format      string    `json:"format,omitempty"`
	Executable  string    `json:"executable"`
}

// Validate verifies that a manifest is complete and safe for platform.
func (m *Manifest) Validate(platform string) error {
	if m == nil {
		return fmt.Errorf("manifest is required")
	}
	if m.Schema != ManifestSchema || strings.TrimSpace(m.ReleaseID) == "" || strings.TrimSpace(m.ProtocolVersion) == "" {
		return fmt.Errorf("manifest schema, release_id, and protocol_version are required")
	}
	if !semverPattern.MatchString(strings.TrimSpace(m.Version)) || !semverPattern.MatchString(strings.TrimSpace(m.MinimumFromVersion)) {
		return fmt.Errorf("manifest version and minimum_from_version must use semantic versioning")
	}
	if m.IssuedAt.IsZero() || !m.ExpiresAt.After(m.IssuedAt) {
		return fmt.Errorf("manifest validity window is invalid")
	}
	if err := validateHTTPSURL(m.SBOMURL); err != nil {
		return fmt.Errorf("manifest sbom_url: %w", err)
	}
	if !validSHA256(m.SBOMSHA256) {
		return fmt.Errorf("manifest sbom_sha256 must be 64 hexadecimal characters")
	}
	if !m.Development && !validSignature(m.Signature) {
		return fmt.Errorf("production manifest signature is required")
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
	if err := validateHTTPSURL(artifact.URL); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if !validSHA256(artifact.SHA256) {
		return fmt.Errorf("sha256 must be 64 hexadecimal characters")
	}
	if !validSHA256(artifact.SBOMSHA256) {
		return fmt.Errorf("sbom_sha256 must be 64 hexadecimal characters")
	}
	if strings.TrimSpace(artifact.CodeSigning) == "" {
		return fmt.Errorf("code_signing status is required")
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

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(strings.TrimSpace(value))
	return err == nil && len(decoded) == sha256Size
}

func validSignature(value Signature) bool {
	return value.Algorithm == operationsv1.SignatureEd25519 && strings.TrimSpace(value.KeyID) != "" && strings.TrimSpace(value.Value) != ""
}

func validateHTTPSURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("must be an absolute HTTPS URL")
	}
	return nil
}

// Sign binds every artifact and the complete manifest to one Ed25519 release
// identity. Artifact hashes still verify downloaded bytes independently.
func (m *Manifest) Sign(privateKey ed25519.PrivateKey) error {
	if len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("release private key must contain %d bytes", ed25519.PrivateKeySize)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID := ReleaseKeyID(publicKey)
	m.Development = false
	m.Signature = Signature{Algorithm: operationsv1.SignatureEd25519, KeyID: keyID}
	sign := func(platform string, artifact Artifact) Artifact {
		artifact.Signature = Signature{Algorithm: operationsv1.SignatureEd25519, KeyID: keyID}
		artifact.Signature.Value = base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, artifactSigningPayload(platform, artifact)))
		return artifact
	}
	for platform, artifact := range m.Database.Artifacts {
		m.Database.Artifacts[platform] = sign(platform, artifact)
	}
	if m.Browser != nil {
		for platform, artifact := range m.Browser.Artifacts {
			m.Browser.Artifacts[platform] = sign(platform, artifact)
		}
	}
	for index := range m.Services {
		for platform, artifact := range m.Services[index].Artifacts {
			m.Services[index].Artifacts[platform] = sign(platform, artifact)
		}
	}
	if m.Frontend != nil {
		for platform, artifact := range m.Frontend.Artifacts {
			m.Frontend.Artifacts[platform] = sign(platform, artifact)
		}
	}
	payload, err := m.signingPayload()
	if err != nil {
		return err
	}
	m.Signature.Value = base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return nil
}

// Verify rejects expired, future-dated, tampered, or wrong-key manifests.
func (m *Manifest) Verify(publicKey ed25519.PublicKey, now time.Time) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("release public key must contain %d bytes", ed25519.PublicKeySize)
	}
	if m.Development {
		return fmt.Errorf("development manifests cannot be remotely trusted")
	}
	if !validSignature(m.Signature) || m.Signature.KeyID != ReleaseKeyID(publicKey) {
		return fmt.Errorf("manifest signature identity is invalid")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if now.Add(5*time.Minute).Before(m.IssuedAt) || now.After(m.ExpiresAt) {
		return fmt.Errorf("manifest is outside its validity window")
	}
	verify := func(platform string, artifact Artifact) error {
		if !validSignature(artifact.Signature) || artifact.Signature.KeyID != m.Signature.KeyID {
			return fmt.Errorf("artifact %s signature identity is invalid", platform)
		}
		value, err := decodeSignature(artifact.Signature.Value)
		if err != nil || !ed25519.Verify(publicKey, artifactSigningPayload(platform, artifact), value) {
			return fmt.Errorf("artifact %s signature verification failed", platform)
		}
		return nil
	}
	for platform, artifact := range m.Database.Artifacts {
		if err := verify(platform, artifact); err != nil {
			return err
		}
	}
	if m.Browser != nil {
		for platform, artifact := range m.Browser.Artifacts {
			if err := verify(platform, artifact); err != nil {
				return err
			}
		}
	}
	for _, service := range m.Services {
		for platform, artifact := range service.Artifacts {
			if err := verify(platform, artifact); err != nil {
				return fmt.Errorf("service %s: %w", service.Name, err)
			}
		}
	}
	if m.Frontend != nil {
		for platform, artifact := range m.Frontend.Artifacts {
			if err := verify(platform, artifact); err != nil {
				return err
			}
		}
	}
	payload, err := m.signingPayload()
	if err != nil {
		return err
	}
	value, err := decodeSignature(m.Signature.Value)
	if err != nil || !ed25519.Verify(publicKey, payload, value) {
		return fmt.Errorf("manifest signature verification failed")
	}
	return nil
}

func (m Manifest) signingPayload() ([]byte, error) {
	m.Signature.Value = ""
	return json.Marshal(m)
}

func artifactSigningPayload(platform string, artifact Artifact) []byte {
	artifact.Signature.Value = ""
	payload, _ := json.Marshal(struct {
		Platform string   `json:"platform"`
		Artifact Artifact `json:"artifact"`
	}{Platform: platform, Artifact: artifact})
	return payload
}

func decodeSignature(value string) ([]byte, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(decoded) != ed25519.SignatureSize {
		return nil, fmt.Errorf("signature is not a valid Ed25519 value")
	}
	return decoded, nil
}

func DecodePublicKey(value string) (ed25519.PublicKey, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	}
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("release public key must be base64-encoded Ed25519 public key bytes")
	}
	return ed25519.PublicKey(decoded), nil
}

func DecodePrivateKey(value string) (ed25519.PrivateKey, error) {
	decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		decoded, err = base64.StdEncoding.DecodeString(strings.TrimSpace(value))
	}
	if err != nil {
		return nil, fmt.Errorf("decode release private key: %w", err)
	}
	if len(decoded) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(decoded), nil
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("release private key must contain an Ed25519 seed or private key")
	}
	return ed25519.PrivateKey(decoded), nil
}

func ReleaseKeyID(publicKey ed25519.PublicKey) string {
	digest := sha256.Sum256(publicKey)
	return hex.EncodeToString(digest[:8])
}

func (m Manifest) AllowsUpgradeFrom(version string) bool {
	return compareSemver(version, m.MinimumFromVersion) >= 0
}

func compareSemver(left, right string) int {
	parse := func(value string) [3]int {
		value = strings.TrimPrefix(strings.TrimSpace(value), "v")
		value = strings.SplitN(value, "-", 2)[0]
		value = strings.SplitN(value, "+", 2)[0]
		parts := strings.Split(value, ".")
		var result [3]int
		for index := 0; index < len(result) && index < len(parts); index++ {
			result[index], _ = strconv.Atoi(parts[index])
		}
		return result
	}
	l, r := parse(left), parse(right)
	for index := range l {
		if l[index] < r[index] {
			return -1
		}
		if l[index] > r[index] {
			return 1
		}
	}
	return 0
}

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
