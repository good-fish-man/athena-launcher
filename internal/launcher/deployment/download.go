package deployment

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	releasepkg "athena-launcher/internal/release"
	ga "github.com/good-fish-man/athena-protocol/protocol/ga/v1"
)

var downloadClient = &http.Client{
	Timeout:       30 * time.Minute,
	CheckRedirect: validateDownloadRedirect,
}

const (
	databaseArtifactMarkerVersion = "2"
	maxArtifactDownloadBytes      = int64(64 << 30)
	maxArchiveEntries             = 100_000
	maxArchiveFileBytes           = int64(16 << 30)
	maxArchiveExpandedBytes       = int64(128 << 30)
)

func loadManifest(ctx context.Context, source string) (*Manifest, error) {
	data, err := readSource(ctx, source, 4<<20)
	if err != nil {
		return nil, fmt.Errorf("load release manifest %s: %w", source, err)
	}
	var manifest Manifest
	if err := decodeStrictJSON(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse release manifest: %w", err)
	}
	if err := manifest.Validate(platformKey()); err != nil {
		return nil, err
	}
	if err := manifest.ValidateGA(); err != nil {
		return nil, err
	}
	remote := remoteManifestSource(source)
	if remote && manifest.Development {
		return nil, fmt.Errorf("remote release manifest cannot use development mode")
	}
	if !manifest.Development {
		publicKey, err := configuredReleasePublicKey()
		if err != nil {
			return nil, err
		}
		if err := manifest.Verify(publicKey, time.Now().UTC()); err != nil {
			return nil, fmt.Errorf("verify release manifest: %w", err)
		}
		if remote {
			if err := verifyReleaseSBOM(ctx, &manifest); err != nil {
				return nil, err
			}
			if compareReleaseSemver(manifest.Version, ga.ReleaseVersion) >= 0 {
				if err := verifyReleaseCompatibility(ctx, &manifest); err != nil {
					return nil, err
				}
			}
		}
	}
	return &manifest, nil
}

func verifyReleaseCompatibility(ctx context.Context, manifest *Manifest) error {
	data, err := readSource(ctx, manifest.CompatibilityURL, 4<<20)
	if err != nil {
		return fmt.Errorf("download release compatibility matrix: %w", err)
	}
	return verifyReleaseCompatibilityData(data, manifest)
}

func verifyReleaseCompatibilityData(data []byte, manifest *Manifest) error {
	digest := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), manifest.CompatibilitySHA256) {
		return fmt.Errorf("release compatibility matrix SHA-256 mismatch")
	}
	var matrix ga.CompatibilityMatrix
	if err := decodeStrictJSON(data, &matrix); err != nil {
		return fmt.Errorf("parse release compatibility matrix: %w", err)
	}
	if err := matrix.Validate(); err != nil {
		return fmt.Errorf("validate release compatibility matrix: %w", err)
	}
	if matrix.ReleaseVersion != manifest.Version || matrix.ProtocolVersion != manifest.ProtocolVersion || matrix.MinimumUpgradeVersion != strings.TrimPrefix(manifest.MinimumFromVersion, "v") {
		return fmt.Errorf("release compatibility matrix does not match manifest versions")
	}
	componentVersions := map[string]string{
		"athena-protocol": manifest.ProtocolVersion,
		"athena-launcher": manifest.Version,
	}
	for _, service := range manifest.Services {
		if _, duplicate := componentVersions[service.Name]; duplicate {
			return fmt.Errorf("manifest component %s is duplicated", service.Name)
		}
		componentVersions[service.Name] = strings.TrimPrefix(service.Version, "v")
	}
	if manifest.Frontend != nil {
		componentVersions["agent-ui"] = strings.TrimPrefix(manifest.Frontend.Version, "v")
	}
	for _, component := range matrix.Components {
		actual, ok := componentVersions[component.Component]
		if !ok {
			return fmt.Errorf("manifest is missing compatibility component %s", component.Component)
		}
		if strings.TrimPrefix(component.Version, "v") != actual {
			return fmt.Errorf("component %s version %s does not match manifest version %s", component.Component, component.Version, actual)
		}
	}
	return nil
}

func decodeStrictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func compareReleaseSemver(left, right string) int {
	parse := func(value string) [3]int {
		value = strings.TrimPrefix(strings.TrimSpace(value), "v")
		value = strings.SplitN(value, "-", 2)[0]
		value = strings.SplitN(value, "+", 2)[0]
		parts := strings.Split(value, ".")
		var result [3]int
		for i := 0; i < len(result) && i < len(parts); i++ {
			fmt.Sscan(parts[i], &result[i])
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

func configuredReleasePublicKey() (ed25519.PublicKey, error) {
	value := strings.TrimSpace(DefaultReleasePublicKey)
	if value == "" {
		value = strings.TrimSpace(os.Getenv("ATHENA_RELEASE_PUBLIC_KEY"))
	}
	if value == "" {
		return nil, fmt.Errorf("release public key is not configured")
	}
	return releasepkg.DecodePublicKey(value)
}

func verifyReleaseSBOM(ctx context.Context, manifest *Manifest) error {
	data, err := readSource(ctx, manifest.SBOMURL, 16<<20)
	if err != nil {
		return fmt.Errorf("download release SBOM: %w", err)
	}
	digest := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(digest[:]), manifest.SBOMSHA256) {
		return fmt.Errorf("release SBOM SHA-256 mismatch")
	}
	return nil
}

func remoteManifestSource(source string) bool {
	parsed, err := url.Parse(strings.TrimSpace(source))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}

func readSource(ctx context.Context, source string, limit int64) ([]byte, error) {
	parsed, err := url.Parse(source)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		if err := validateDownloadURL(parsed); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, err
		}
		resp, err := downloadClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
		}
		if resp.ContentLength > limit {
			return nil, fmt.Errorf("download content length %d exceeds limit %d", resp.ContentLength, limit)
		}
		return readLimited(resp.Body, limit)
	}
	if parsed != nil && parsed.Scheme == "file" {
		source = parsed.Path
	}
	file, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info, statErr := file.Stat(); statErr != nil {
		return nil, statErr
	} else if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source must be a regular file: %s", source)
	} else if info.Size() > limit {
		return nil, fmt.Errorf("source size %d exceeds limit %d", info.Size(), limit)
	}
	return readLimited(file, limit)
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("read limit must be greater than zero")
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("content exceeds limit %d", limit)
	}
	return data, nil
}

func installServices(ctx context.Context, home string, manifest *Manifest, state *launcherState) (map[string]string, error) {
	installed := make(map[string]string, len(manifest.Services))
	for _, service := range manifest.Services {
		artifact := service.Artifacts[platformKey()]
		serviceRoot := filepath.Join(home, "services", service.Name)
		target := filepath.Join(serviceRoot, manifest.Version)
		executable := filepath.Join(target, filepath.FromSlash(artifact.Executable))
		marker := filepath.Join(target, ".artifact-sha256")
		if current, err := os.ReadFile(marker); err == nil && strings.EqualFold(strings.TrimSpace(string(current)), artifact.SHA256) {
			if _, err := os.Stat(executable); err == nil {
				if err := verifyInstalledCodeSignature(executable, artifact.CodeSigning); err != nil {
					return nil, err
				}
				installed[service.Name] = executable
				continue
			}
		}
		if existing := findArtifactRootByMarker(serviceRoot, artifact.SHA256); existing != "" {
			existingExecutable, err := findInstalledExecutable(existing, filepath.Base(filepath.FromSlash(artifact.Executable)))
			if err == nil {
				if err := verifyInstalledCodeSignature(existingExecutable, artifact.CodeSigning); err != nil {
					return nil, err
				}
				installed[service.Name] = existingExecutable
				state.Installed[service.Name] = filepath.Base(existing)
				fmt.Printf("[%s] reusing verified package %s\n", service.Name, shortHash(artifact.SHA256))
				continue
			}
		}
		fmt.Printf("[%s] downloading %s for %s\n", service.Name, manifest.Version, platformKey())
		if err := installArtifact(ctx, home, target, artifact); err != nil {
			return nil, fmt.Errorf("install %s: %w", service.Name, err)
		}
		if err := os.WriteFile(marker, []byte(strings.ToLower(artifact.SHA256)+"\n"), 0o600); err != nil {
			return nil, err
		}
		if err := os.Chmod(executable, 0o755); err != nil {
			return nil, fmt.Errorf("make %s executable: %w", executable, err)
		}
		if err := verifyInstalledCodeSignature(executable, artifact.CodeSigning); err != nil {
			return nil, err
		}
		installed[service.Name] = executable
		state.Installed[service.Name] = manifest.Version
	}
	state.Version = manifest.Version
	return installed, saveState(home, state)
}

func installDatabase(ctx context.Context, home string, manifest *Manifest) (string, error) {
	artifact := manifest.Database.Artifacts[platformKey()]
	databaseRoot := filepath.Join(home, "postgres")
	target := filepath.Join(databaseRoot, manifest.Database.Version)
	marker := filepath.Join(target, ".artifact-sha256")
	expectedMarker := databaseArtifactMarker(artifact.SHA256)
	if current, err := os.ReadFile(marker); err == nil && strings.EqualFold(strings.TrimSpace(string(current)), expectedMarker) {
		if err := verifyInstalledCodeSignature(newManagedDatabase(home, target, manifest.Database.BinDir, "").binary("postgres"), artifact.CodeSigning); err != nil {
			return "", err
		}
		return target, nil
	}
	if existing := findArtifactRootByMarker(databaseRoot, expectedMarker); existing != "" {
		if err := verifyInstalledCodeSignature(newManagedDatabase(home, existing, manifest.Database.BinDir, "").binary("postgres"), artifact.CodeSigning); err != nil {
			return "", err
		}
		fmt.Printf("[postgres] reusing verified package %s\n", shortHash(artifact.SHA256))
		return existing, nil
	}
	fmt.Printf("[postgres] downloading %s for %s\n", manifest.Database.Version, platformKey())
	if err := installArtifact(ctx, home, target, artifact); err != nil {
		return "", fmt.Errorf("install postgres: %w", err)
	}
	if err := os.WriteFile(marker, []byte(expectedMarker+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := verifyInstalledCodeSignature(newManagedDatabase(home, target, manifest.Database.BinDir, "").binary("postgres"), artifact.CodeSigning); err != nil {
		return "", err
	}
	return target, nil
}

func installBrowser(ctx context.Context, home string, manifest *Manifest, state *launcherState) (string, error) {
	if manifest.Browser == nil {
		return "", nil
	}
	if state != nil && state.Installed == nil {
		state.Installed = map[string]string{}
	}
	artifact := manifest.Browser.Artifacts[platformKey()]
	browserRoot := filepath.Join(home, "browser")
	target := filepath.Join(browserRoot, manifest.Browser.Version)
	executable := filepath.Join(target, filepath.FromSlash(artifact.Executable))
	marker := filepath.Join(target, ".artifact-sha256")
	if current, err := os.ReadFile(marker); err == nil && strings.EqualFold(strings.TrimSpace(string(current)), artifact.SHA256) {
		if _, err := os.Stat(executable); err == nil {
			if err := verifyInstalledCodeSignature(executable, artifact.CodeSigning); err != nil {
				return "", err
			}
			return executable, nil
		}
	}
	if existing := findArtifactRootByMarker(browserRoot, artifact.SHA256); existing != "" {
		existingExecutable, err := findInstalledExecutable(existing, filepath.Base(filepath.FromSlash(artifact.Executable)))
		if err == nil {
			if err := verifyInstalledCodeSignature(existingExecutable, artifact.CodeSigning); err != nil {
				return "", err
			}
			if state != nil {
				state.Installed["agent-browser"] = filepath.Base(existing)
			}
			fmt.Printf("[agent-browser] reusing verified package %s\n", shortHash(artifact.SHA256))
			return existingExecutable, nil
		}
	}
	fmt.Printf("[agent-browser] downloading %s for %s\n", manifest.Browser.Version, platformKey())
	if err := installArtifact(ctx, home, target, artifact); err != nil {
		return "", fmt.Errorf("install agent-browser: %w", err)
	}
	if err := os.WriteFile(marker, []byte(strings.ToLower(artifact.SHA256)+"\n"), 0o600); err != nil {
		return "", err
	}
	if err := os.Chmod(executable, 0o755); err != nil {
		return "", fmt.Errorf("make %s executable: %w", executable, err)
	}
	if err := verifyInstalledCodeSignature(executable, artifact.CodeSigning); err != nil {
		return "", err
	}
	if state != nil {
		state.Installed["agent-browser"] = manifest.Browser.Version
	}
	return executable, saveState(home, state)
}

func databaseArtifactMarker(checksum string) string {
	return databaseArtifactMarkerVersion + ":" + strings.ToLower(strings.TrimSpace(checksum))
}

func installFrontend(ctx context.Context, home string, manifest *Manifest, state *launcherState) (string, error) {
	if manifest.Frontend == nil {
		return "", nil
	}
	artifact := manifest.Frontend.Artifacts[platformKey()]
	frontendRootDir := filepath.Join(home, "frontend")
	target := filepath.Join(frontendRootDir, manifest.Version)
	marker := filepath.Join(target, ".artifact-sha256")
	if current, err := os.ReadFile(marker); err == nil && strings.EqualFold(strings.TrimSpace(string(current)), artifact.SHA256) {
		return frontendRoot(target, manifest.Frontend.Root), nil
	}
	if existing := findArtifactRootByMarker(frontendRootDir, artifact.SHA256); existing != "" {
		state.Installed["frontend"] = filepath.Base(existing)
		fmt.Printf("[frontend] reusing verified package %s\n", shortHash(artifact.SHA256))
		return frontendRoot(existing, manifest.Frontend.Root), nil
	}
	fmt.Printf("[frontend] downloading %s for %s\n", manifest.Version, platformKey())
	if err := installArtifact(ctx, home, target, artifact); err != nil {
		return "", fmt.Errorf("install frontend: %w", err)
	}
	if err := os.WriteFile(marker, []byte(strings.ToLower(artifact.SHA256)+"\n"), 0o600); err != nil {
		return "", err
	}
	state.Installed["frontend"] = manifest.Version
	if err := saveState(home, state); err != nil {
		return "", err
	}
	return frontendRoot(target, manifest.Frontend.Root), nil
}

func findArtifactRootByMarker(root, expected string) string {
	expected = strings.ToLower(strings.TrimSpace(expected))
	markers, _ := filepath.Glob(filepath.Join(root, "*", ".artifact-sha256"))
	for _, marker := range markers {
		data, err := os.ReadFile(marker)
		if err == nil && strings.ToLower(strings.TrimSpace(string(data))) == expected {
			return filepath.Dir(marker)
		}
	}
	return ""
}

func findInstalledExecutable(root, name string) (string, error) {
	var executable string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != name {
			return nil
		}
		executable = path
		return filepath.SkipAll
	})
	if err != nil {
		return "", err
	}
	if executable == "" {
		return "", os.ErrNotExist
	}
	return executable, nil
}

func frontendRoot(target, root string) string {
	if strings.TrimSpace(root) == "" {
		return target
	}
	return filepath.Join(target, filepath.FromSlash(root))
}

func installArtifact(ctx context.Context, home, target string, artifact Artifact) error {
	if err := os.MkdirAll(filepath.Join(home, "downloads"), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Join(home, "downloads"), "artifact-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	_ = temporary.Close()
	defer os.Remove(temporaryPath)
	if err := downloadFile(ctx, artifact.URL, temporaryPath, artifact.SizeBytes); err != nil {
		return err
	}
	if err := verifySHA256(temporaryPath, artifact.SHA256); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(target), "."+filepath.Base(target)+".staging-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	if err := extractArtifact(temporaryPath, staging, artifact.Format, artifact.Executable); err != nil {
		return err
	}
	return replaceArtifactDirectory(staging, target)
}

func downloadFile(ctx context.Context, source, target string, expectedSize int64) error {
	if expectedSize <= 0 || expectedSize > maxArtifactDownloadBytes {
		return fmt.Errorf("artifact size %d is outside the supported range", expectedSize)
	}
	parsed, err := url.Parse(source)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		if err := validateDownloadURL(parsed); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return err
		}
		resp, err := downloadClient.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
		}
		if resp.ContentLength >= 0 && resp.ContentLength != expectedSize {
			return fmt.Errorf("download size mismatch: expected %d bytes, server declared %d", expectedSize, resp.ContentLength)
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		copyErr := copyExact(file, resp.Body, expectedSize)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	if parsed != nil && parsed.Scheme == "file" {
		source = parsed.Path
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("artifact source must be a regular file: %s", source)
	}
	if info.Size() != expectedSize {
		return fmt.Errorf("artifact size mismatch: expected %d bytes, got %d", expectedSize, info.Size())
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	copyErr := copyExact(output, input, expectedSize)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyExact(output io.Writer, input io.Reader, expected int64) error {
	written, err := io.Copy(output, io.LimitReader(input, expected+1))
	if err != nil {
		return err
	}
	if written != expected {
		return fmt.Errorf("artifact size mismatch: expected %d bytes, got %d", expected, written)
	}
	return nil
}

func validateDownloadURL(parsed *url.URL) error {
	if parsed == nil || parsed.Host == "" {
		return fmt.Errorf("download URL must be absolute")
	}
	if parsed.User != nil {
		return fmt.Errorf("download URL cannot contain credentials: %s", parsed.Redacted())
	}
	if parsed.Scheme == "https" {
		if ip := net.ParseIP(parsed.Hostname()); ip != nil && (ip.IsUnspecified() || ip.IsMulticast() || ip.IsPrivate()) {
			return fmt.Errorf("HTTPS download URL cannot target a non-public IP: %s", parsed.Redacted())
		}
		return nil
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback() {
		return nil
	}
	return fmt.Errorf("remote downloads require HTTPS: %s", parsed.Redacted())
}

func validateDownloadRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return fmt.Errorf("download stopped after too many redirects")
	}
	if err := validateDownloadURL(request.URL); err != nil {
		return err
	}
	if len(via) > 0 && strings.EqualFold(via[len(via)-1].URL.Scheme, "https") && !strings.EqualFold(request.URL.Scheme, "https") {
		return fmt.Errorf("download redirect cannot downgrade HTTPS to %s", request.URL.Scheme)
	}
	return nil
}

func replaceArtifactDirectory(staging, target string) error {
	rollback := target + ".rollback"
	if err := os.RemoveAll(rollback); err != nil {
		return fmt.Errorf("remove stale artifact rollback: %w", err)
	}
	hadTarget := false
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, rollback); err != nil {
			return fmt.Errorf("preserve current artifact: %w", err)
		}
		hadTarget = true
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staging, target); err != nil {
		if hadTarget {
			_ = os.Rename(rollback, target)
		}
		return fmt.Errorf("activate staged artifact: %w", err)
	}
	if hadTarget {
		if err := os.RemoveAll(rollback); err != nil {
			return fmt.Errorf("remove previous artifact after activation: %w", err)
		}
	}
	return nil
}

func verifySHA256(path, expected string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if !strings.EqualFold(actual, strings.TrimSpace(expected)) {
		return fmt.Errorf("SHA-256 mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

func extractArtifact(archivePath, target, format, executable string) error {
	format = strings.ToLower(strings.TrimSpace(format))
	switch format {
	case "zip":
		return extractZIP(archivePath, target)
	case "tar.gz", "tgz":
		return extractTarGZ(archivePath, target)
	case "raw", "":
		destination, err := safeArchivePath(target, executable)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return copyPath(archivePath, destination, 0o755)
	default:
		return fmt.Errorf("unsupported artifact format %q", format)
	}
}

func extractZIP(path, target string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	budget := archiveBudget{}
	for _, item := range reader.File {
		if item.UncompressedSize64 > uint64(maxArchiveFileBytes) {
			return fmt.Errorf("archive entry %q exceeds the per-file limit", item.Name)
		}
		size := int64(item.UncompressedSize64)
		if item.FileInfo().IsDir() {
			size = 0
		}
		if err := budget.reserve(size); err != nil {
			return fmt.Errorf("archive entry %q: %w", item.Name, err)
		}
	}
	for _, item := range reader.File {
		destination, err := safeArchivePath(target, item.Name)
		if err != nil {
			return err
		}
		if item.FileInfo().IsDir() {
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return err
			}
			continue
		}
		if !item.Mode().IsRegular() {
			return fmt.Errorf("unsupported ZIP entry type for %q", item.Name)
		}
		input, err := item.Open()
		if err != nil {
			return err
		}
		if err := writeArchiveFileExact(destination, input, item.Mode(), int64(item.UncompressedSize64)); err != nil {
			_ = input.Close()
			return err
		}
		_ = input.Close()
	}
	return nil
}

func extractTarGZ(path, target string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	budget := archiveBudget{}
	type archiveSymlink struct {
		path   string
		target string
	}
	var symlinks []archiveSymlink
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		entrySize := int64(0)
		if header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA {
			entrySize = header.Size
		}
		if err := budget.reserve(entrySize); err != nil {
			return fmt.Errorf("archive entry %q: %w", header.Name, err)
		}
		destination, err := safeArchivePath(target, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(destination, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := writeArchiveFileExact(destination, reader, os.FileMode(header.Mode), header.Size); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := validateArchiveSymlink(target, destination, header.Linkname); err != nil {
				return err
			}
			symlinks = append(symlinks, archiveSymlink{path: destination, target: filepath.FromSlash(header.Linkname)})
		default:
			return fmt.Errorf("unsupported tar entry type %d for %q", header.Typeflag, header.Name)
		}
	}
	// Create links only after regular files are written so later entries cannot
	// traverse a link outside the extraction root.
	for _, link := range symlinks {
		if err := os.MkdirAll(filepath.Dir(link.path), 0o755); err != nil {
			return err
		}
		if err := os.Symlink(link.target, link.path); err != nil {
			return err
		}
	}
	return nil
}

func validateArchiveSymlink(root, destination, linkname string) error {
	linkTarget := filepath.Clean(filepath.FromSlash(linkname))
	if filepath.IsAbs(linkTarget) {
		return fmt.Errorf("unsafe archive symlink %q -> %q", destination, linkname)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(destination), linkTarget))
	cleanRoot := filepath.Clean(root)
	if resolved != cleanRoot && !strings.HasPrefix(resolved, cleanRoot+string(filepath.Separator)) {
		return fmt.Errorf("archive symlink escapes target: %q -> %q", destination, linkname)
	}
	return nil
}

func safeArchivePath(root, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	if clean == "." {
		return filepath.Clean(root), nil
	}
	destination := filepath.Join(root, clean)
	if !strings.HasPrefix(destination, filepath.Clean(root)+string(filepath.Separator)) {
		return "", fmt.Errorf("archive path escapes target: %q", name)
	}
	return destination, nil
}

type archiveBudget struct {
	entries int
	bytes   int64
}

func (b *archiveBudget) reserve(size int64) error {
	if size < 0 || size > maxArchiveFileBytes {
		return fmt.Errorf("entry size %d is outside the supported range", size)
	}
	b.entries++
	if b.entries > maxArchiveEntries {
		return fmt.Errorf("archive contains more than %d entries", maxArchiveEntries)
	}
	if size > maxArchiveExpandedBytes-b.bytes {
		return fmt.Errorf("archive expands beyond %d bytes", maxArchiveExpandedBytes)
	}
	b.bytes += size
	return nil
}

func writeArchiveFileExact(path string, input io.Reader, mode os.FileMode, expected int64) error {
	if expected < 0 || expected > maxArchiveFileBytes {
		return fmt.Errorf("archive file size %d is outside the supported range", expected)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if mode.Perm() == 0 {
		mode = 0o644
	}
	output, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	copyErr := copyExact(output, input, expected)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func copyPath(source, target string, mode os.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	return writeArchiveFileExact(target, input, mode, info.Size())
}
