package deployment

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

var downloadClient = &http.Client{Timeout: 30 * time.Minute}

const (
	databaseArtifactMarkerVersion       = "2"
	manifestFileName                    = "release-manifest.json"
	manifestArchiveName                 = "release-manifest.zip"
	manifestSizeLimit             int64 = 4 << 20
)

type httpStatusError struct {
	statusCode int
	url        string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("download %s returned HTTP %d", e.url, e.statusCode)
}

func loadManifest(ctx context.Context, source string) (*Manifest, error) {
	data, err := readSource(ctx, source, manifestSizeLimit)
	if err != nil {
		archiveSource, ok := manifestArchiveFallback(source, err)
		if ok {
			data, err = readSource(ctx, archiveSource, manifestSizeLimit)
			if err != nil {
				return nil, fmt.Errorf("load release manifest %s (archive fallback %s): %w", source, archiveSource, err)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("load release manifest %s: %w", source, err)
	}
	data, err = unpackManifest(data, manifestSizeLimit)
	if err != nil {
		return nil, fmt.Errorf("decode release manifest %s: %w", source, err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse release manifest: %w", err)
	}
	if err := manifest.Validate(platformKey()); err != nil {
		return nil, err
	}
	return &manifest, nil
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
			return nil, &httpStatusError{statusCode: resp.StatusCode, url: source}
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
	return readLimited(file, limit)
}

func readLimited(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("content exceeds %d byte limit", limit)
	}
	return data, nil
}

func manifestArchiveFallback(source string, sourceErr error) (string, bool) {
	statusErr := &httpStatusError{}
	if !errors.As(sourceErr, &statusErr) || statusErr.statusCode != http.StatusNotFound {
		return "", false
	}
	parsed, err := url.Parse(source)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || path.Base(parsed.Path) != manifestFileName {
		return "", false
	}
	parsed.Path = path.Join(path.Dir(parsed.Path), manifestArchiveName)
	return parsed.String(), true
}

func unpackManifest(data []byte, limit int64) ([]byte, error) {
	if !isZipArchive(data) {
		return data, nil
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open ZIP archive: %w", err)
	}
	var manifestFile *zip.File
	for _, candidate := range archive.File {
		clean := path.Clean(candidate.Name)
		if clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("ZIP contains unsafe path %q", candidate.Name)
		}
		if candidate.FileInfo().IsDir() || path.Base(clean) != manifestFileName {
			continue
		}
		if manifestFile != nil {
			return nil, fmt.Errorf("ZIP contains multiple %s files", manifestFileName)
		}
		manifestFile = candidate
	}
	if manifestFile == nil {
		return nil, fmt.Errorf("ZIP does not contain %s", manifestFileName)
	}
	if manifestFile.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("%s exceeds %d byte limit", manifestFileName, limit)
	}
	reader, err := manifestFile.Open()
	if err != nil {
		return nil, fmt.Errorf("open %s from ZIP: %w", manifestFileName, err)
	}
	defer reader.Close()
	return readLimited(reader, limit)
}

func isZipArchive(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && ((data[2] == 3 && data[3] == 4) || (data[2] == 5 && data[3] == 6) || (data[2] == 7 && data[3] == 8))
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
				installed[service.Name] = executable
				continue
			}
		}
		if existing := findArtifactRootByMarker(serviceRoot, artifact.SHA256); existing != "" {
			existingExecutable, err := findInstalledExecutable(existing, filepath.Base(filepath.FromSlash(artifact.Executable)))
			if err == nil {
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
		return target, nil
	}
	if existing := findArtifactRootByMarker(databaseRoot, expectedMarker); existing != "" {
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
			return executable, nil
		}
	}
	if existing := findArtifactRootByMarker(browserRoot, artifact.SHA256); existing != "" {
		existingExecutable, err := findInstalledExecutable(existing, filepath.Base(filepath.FromSlash(artifact.Executable)))
		if err == nil {
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
	if err := downloadFile(ctx, artifact.URL, temporaryPath); err != nil {
		return err
	}
	if err := verifySHA256(temporaryPath, artifact.SHA256); err != nil {
		return err
	}
	staging := target + ".staging"
	_ = os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	if err := extractArtifact(temporaryPath, staging, artifact.Format, artifact.Executable); err != nil {
		_ = os.RemoveAll(staging)
		return err
	}
	_ = os.RemoveAll(target)
	return os.Rename(staging, target)
}

func downloadFile(ctx context.Context, source, target string) error {
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
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(file, resp.Body)
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
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func validateDownloadURL(parsed *url.URL) error {
	if parsed.Scheme == "https" {
		return nil
	}
	host := parsed.Hostname()
	if strings.EqualFold(host, "localhost") || net.ParseIP(host).IsLoopback() {
		return nil
	}
	return fmt.Errorf("remote downloads require HTTPS: %s", parsed.Redacted())
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
		input, err := item.Open()
		if err != nil {
			return err
		}
		if err := writeArchiveFile(destination, input, item.Mode()); err != nil {
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
			if err := writeArchiveFile(destination, reader, os.FileMode(header.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := validateArchiveSymlink(target, destination, header.Linkname); err != nil {
				return err
			}
			symlinks = append(symlinks, archiveSymlink{path: destination, target: filepath.FromSlash(header.Linkname)})
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

func writeArchiveFile(path string, input io.Reader, mode os.FileMode) error {
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
	_, copyErr := io.Copy(output, input)
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
	return writeArchiveFile(target, input, mode)
}
