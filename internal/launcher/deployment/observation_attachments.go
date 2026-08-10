package deployment

import (
	controlpkg "athena-launcher/internal/control"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxObservationAttachments = 2
	maxObservationImageBytes  = 4 << 20
)

func collectObservationAttachments(home string, state map[string]any) ([]controlpkg.Attachment, error) {
	root, err := filepath.Abs(filepath.Join(home, "browser", "screenshots"))
	if err != nil {
		return nil, fmt.Errorf("resolve screenshot directory: %w", err)
	}
	paths := make([]string, 0, maxObservationAttachments)
	collectArtifactPaths(state, &paths)
	attachments := make([]controlpkg.Attachment, 0, min(len(paths), maxObservationAttachments))
	seen := make(map[string]bool)
	for _, path := range paths {
		if len(attachments) >= maxObservationAttachments {
			break
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		resolvedRoot, rootErr := filepath.EvalSymlinks(root)
		resolvedPath, pathErr := filepath.EvalSymlinks(absolute)
		if rootErr != nil || pathErr != nil || !pathWithinRoot(resolvedRoot, resolvedPath) || seen[resolvedPath] {
			continue
		}
		seen[resolvedPath] = true
		attachment, err := readObservationAttachment(resolvedPath)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, nil
}

func redactObservationAttachmentPaths(value any) {
	switch typed := value.(type) {
	case map[string]any:
		if artifact, ok := typed["artifact"].(map[string]any); ok {
			delete(typed, "path")
			delete(artifact, "path")
		}
		delete(typed, "screenshot_path")
		for _, child := range typed {
			redactObservationAttachmentPaths(child)
		}
	case []any:
		for _, child := range typed {
			redactObservationAttachmentPaths(child)
		}
	}
}

func collectArtifactPaths(value any, paths *[]string) {
	if len(*paths) >= maxObservationAttachments*4 {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		if artifact, ok := typed["artifact"].(map[string]any); ok {
			path, _ := artifact["path"].(string)
			if strings.TrimSpace(path) == "" {
				path, _ = typed["path"].(string)
			}
			if strings.TrimSpace(path) != "" {
				*paths = append(*paths, strings.TrimSpace(path))
			}
		}
		for _, child := range typed {
			collectArtifactPaths(child, paths)
		}
	case []any:
		for _, child := range typed {
			collectArtifactPaths(child, paths)
		}
	}
}

func readObservationAttachment(path string) (controlpkg.Attachment, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil || linkInfo.Mode()&os.ModeSymlink != 0 {
		return controlpkg.Attachment{}, fmt.Errorf("screenshot attachment must not be a symbolic link")
	}
	info, err := os.Stat(path)
	if err != nil {
		return controlpkg.Attachment{}, fmt.Errorf("inspect screenshot attachment: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxObservationImageBytes {
		return controlpkg.Attachment{}, fmt.Errorf("screenshot attachment must be a regular file between 1 and %d bytes", maxObservationImageBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return controlpkg.Attachment{}, fmt.Errorf("read screenshot attachment: %w", err)
	}
	mimeType := screenshotMIMEType(path)
	detected := http.DetectContentType(data)
	if mimeType == "" || detected != mimeType {
		return controlpkg.Attachment{}, fmt.Errorf("screenshot attachment content type %q does not match %q", detected, mimeType)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	return controlpkg.Attachment{
		ID: "artifact-" + digest[:16], Kind: "image", MIMEType: mimeType,
		Size: int64(len(data)), SHA256: digest, Encoding: "base64",
		Data: base64.StdEncoding.EncodeToString(data), Purpose: "browser_observation", Detail: "auto",
	}, nil
}

func pathWithinRoot(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func screenshotMIMEType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	default:
		return ""
	}
}
