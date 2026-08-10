package browser_runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"
)

func TestBrowserOCRProviderExtractsBoundedText(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "browser", "screenshots", "page.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := newBrowserOCRProvider(home)
	provider.lookPath = func(string) (string, error) { return "/test/tesseract", nil }
	provider.run = func(_ context.Context, executable string, args ...string) ([]byte, error) {
		if executable != "/test/tesseract" || args[0] != path || args[1] != "stdout" {
			t.Fatalf("unexpected OCR command: executable=%q args=%#v", executable, args)
		}
		return []byte("Hello from the screenshot"), nil
	}
	result := provider.Extract(perception.OCRRequest{Path: path, Language: "eng", MaxChars: 10})
	if result["available"] != true || result["text"] != "Hello from" || result["truncated"] != true {
		t.Fatalf("unexpected OCR result: %#v", result)
	}
}

func TestBrowserOCRProviderRejectsPathOutsideAthenaScreenshots(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(path, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := newBrowserOCRProvider(home)
	result := provider.Extract(perception.OCRRequest{Path: path})
	if result["available"] != false || result["reason"] != "invalid_screenshot_path" {
		t.Fatalf("outside screenshot path was accepted: %#v", result)
	}
}

func TestBrowserOCRProviderReportsMissingExecutable(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "browser", "screenshots", "page.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("png"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider := newBrowserOCRProvider(home)
	provider.lookPath = func(string) (string, error) { return "", errors.New("missing") }
	result := provider.Extract(perception.OCRRequest{Path: path})
	if result["reason"] != "tesseract_not_installed" {
		t.Fatalf("missing provider diagnostics mismatch: %#v", result)
	}
}
