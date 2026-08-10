package deployment

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCollectObservationAttachmentsReadsOnlyScreenshotArtifacts(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "browser", "screenshots", "page.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 64)...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	attachments, err := collectObservationAttachments(home, map[string]any{
		"perception": map[string]any{"visual": map[string]any{"screenshot": map[string]any{
			"path": path, "artifact": map[string]any{"available": true},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].MIMEType != "image/png" || attachments[0].Size != int64(len(data)) || attachments[0].Data == "" {
		t.Fatalf("attachments = %#v", attachments)
	}
}

func TestCollectObservationAttachmentsRejectsOutsidePath(t *testing.T) {
	home := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachments, err := collectObservationAttachments(home, map[string]any{
		"screenshot": map[string]any{"path": outside, "artifact": map[string]any{"available": true}},
	})
	if err != nil || len(attachments) != 0 {
		t.Fatalf("outside artifact leaked: attachments=%#v err=%v", attachments, err)
	}
}

func TestRedactObservationAttachmentPaths(t *testing.T) {
	state := map[string]any{
		"screenshot_path": "/private/device.png",
		"screenshot": map[string]any{
			"path":     "/private/device.png",
			"artifact": map[string]any{"path": "/private/device.png", "sha256": "abc"},
		},
	}
	redactObservationAttachmentPaths(state)
	if state["screenshot_path"] != nil {
		t.Fatalf("top-level screenshot path leaked: %#v", state)
	}
	screenshot := state["screenshot"].(map[string]any)
	artifact := screenshot["artifact"].(map[string]any)
	if screenshot["path"] != nil || artifact["path"] != nil || artifact["sha256"] != "abc" {
		t.Fatalf("artifact paths were not safely redacted: %#v", state)
	}
}
