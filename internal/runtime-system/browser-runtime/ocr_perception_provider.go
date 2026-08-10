package browser_runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"
)

const browserOCRTimeout = 45 * time.Second

var browserOCRLanguagePattern = regexp.MustCompile(`^[A-Za-z0-9_+.-]+$`)

type browserOCRProvider struct {
	home     string
	lookPath func(string) (string, error)
	run      func(context.Context, string, ...string) ([]byte, error)
}

func newBrowserOCRProvider(home string) *browserOCRProvider {
	return &browserOCRProvider{
		home:     home,
		lookPath: exec.LookPath,
		run: func(ctx context.Context, executable string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, executable, args...).CombinedOutput()
		},
	}
}

func (p *browserOCRProvider) Extract(request perception.OCRRequest) map[string]any {
	if p == nil || p.lookPath == nil || p.run == nil {
		return map[string]any{"available": false, "reason": "ocr_provider_not_configured"}
	}
	path, err := p.validScreenshotPath(request.Path)
	if err != nil {
		return map[string]any{"available": false, "reason": "invalid_screenshot_path", "error": err.Error()}
	}
	executable, err := p.lookPath("tesseract")
	if err != nil {
		return map[string]any{
			"available": false, "provider": "tesseract", "reason": "tesseract_not_installed",
		}
	}
	language := strings.TrimSpace(request.Language)
	if language == "" {
		language = "eng"
	}
	if !browserOCRLanguagePattern.MatchString(language) {
		return map[string]any{"available": false, "provider": "tesseract", "reason": "invalid_ocr_language"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), browserOCRTimeout)
	defer cancel()
	output, runErr := p.run(ctx, executable, path, "stdout", "-l", language)
	if ctx.Err() != nil {
		return map[string]any{"available": false, "provider": "tesseract", "reason": "ocr_timeout"}
	}
	if runErr != nil {
		return map[string]any{
			"available": false, "provider": "tesseract", "reason": "ocr_failed",
			"error": truncateBrowserOutput(strings.TrimSpace(string(output)), 1000),
		}
	}
	text := strings.TrimSpace(string(output))
	limit := request.MaxChars
	if limit <= 0 {
		limit = perception.DefaultBudget().MaxOCRChars
	}
	text, truncated := truncateBrowserOCRText(text, limit)
	return map[string]any{
		"available": true, "provider": "tesseract", "language": language,
		"text": text, "chars": len(text), "truncated": truncated,
	}
}

func truncateBrowserOCRText(value string, limit int) (string, bool) {
	runes := []rune(strings.TrimSpace(value))
	if limit <= 0 || len(runes) <= limit {
		return string(runes), false
	}
	return string(runes[:limit]), true
}

func (p *browserOCRProvider) validScreenshotPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", os.ErrInvalid
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(filepath.Join(p.home, "browser", "screenshots"))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", os.ErrPermission
	}
	info, err := os.Stat(absolute)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", os.ErrInvalid
	}
	return absolute, nil
}
