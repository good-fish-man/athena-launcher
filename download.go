package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
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
)

var downloadClient = &http.Client{Timeout: 30 * time.Minute}

func loadManifest(ctx context.Context, source string) (*Manifest, error) {
	data, err := readSource(ctx, source, 4<<20)
	if err != nil {
		return nil, fmt.Errorf("load release manifest %s: %w", source, err)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse release manifest: %w", err)
	}
	if err := manifest.validate(); err != nil {
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
			return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
		}
		return io.ReadAll(io.LimitReader(resp.Body, limit))
	}
	if parsed != nil && parsed.Scheme == "file" {
		source = parsed.Path
	}
	return os.ReadFile(source)
}

func installServices(ctx context.Context, home string, manifest *Manifest, state *launcherState) (map[string]string, error) {
	installed := make(map[string]string, len(manifest.Services))
	for _, service := range manifest.Services {
		artifact := service.Artifacts[platformKey()]
		target := filepath.Join(home, "services", service.Name, manifest.Version)
		executable := filepath.Join(target, filepath.FromSlash(artifact.Executable))
		marker := filepath.Join(target, ".artifact-sha256")
		if current, err := os.ReadFile(marker); err == nil && strings.EqualFold(strings.TrimSpace(string(current)), artifact.SHA256) {
			if _, err := os.Stat(executable); err == nil {
				installed[service.Name] = executable
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
	target := filepath.Join(home, "postgres", manifest.Database.Version)
	marker := filepath.Join(target, ".artifact-sha256")
	if current, err := os.ReadFile(marker); err == nil && strings.EqualFold(strings.TrimSpace(string(current)), artifact.SHA256) {
		return target, nil
	}
	fmt.Printf("[postgres] downloading %s for %s\n", manifest.Database.Version, platformKey())
	if err := installArtifact(ctx, home, target, artifact); err != nil {
		return "", fmt.Errorf("install postgres: %w", err)
	}
	if err := os.WriteFile(marker, []byte(strings.ToLower(artifact.SHA256)+"\n"), 0o600); err != nil {
		return "", err
	}
	return target, nil
}

func installFrontend(ctx context.Context, home string, manifest *Manifest, state *launcherState) (string, error) {
	if manifest.Frontend == nil {
		return "", nil
	}
	artifact := manifest.Frontend.Artifacts[platformKey()]
	target := filepath.Join(home, "frontend", manifest.Version)
	marker := filepath.Join(target, ".artifact-sha256")
	if current, err := os.ReadFile(marker); err == nil && strings.EqualFold(strings.TrimSpace(string(current)), artifact.SHA256) {
		return frontendRoot(target, manifest.Frontend.Root), nil
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
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
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
		}
	}
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
