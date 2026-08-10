package browser

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestCaptureScreenshotUsesElementProvider(t *testing.T) {
	runtime := NewRuntime(t.TempDir(), Options{})
	var command []string
	runner := func(_ time.Duration, args ...string) (string, error) {
		command = append([]string{}, args...)
		return "captured", os.WriteFile(args[len(args)-1], []byte("png"), 0o600)
	}
	result := runtime.CaptureScreenshot(Request{
		SessionID: "athena-test",
		Action:    "screenshot",
		Arguments: map[string]any{"screenshot": true, "screenshot_scope": "element", "ref": "@e7"},
	}, runner, []string{"--session", "athena-test"})

	joined := strings.Join(command, " ")
	if !strings.Contains(joined, "screenshot @e7") || result["scope"] != "element" || result["ref"] != "@e7" {
		t.Fatalf("element screenshot command/result mismatch: command=%q result=%#v", joined, result)
	}
}

func TestCaptureScreenshotUsesFullPageAnnotatedProvider(t *testing.T) {
	runtime := NewRuntime(t.TempDir(), Options{})
	var command []string
	runner := func(_ time.Duration, args ...string) (string, error) {
		command = append([]string{}, args...)
		return `{"success":true,"data":{"annotations":[{"ref":"@e1","label":"Play"}]}}`, os.WriteFile(args[len(args)-1], []byte("png"), 0o600)
	}
	result := runtime.CaptureScreenshot(Request{
		SessionID: "athena-test",
		Action:    "navigate",
		Arguments: map[string]any{
			"perception_capture": true, "screenshot_scope": "full_page", "screenshot_annotate": true,
		},
	}, runner, []string{"--session", "athena-test"})

	joined := strings.Join(command, " ")
	if !strings.Contains(joined, "screenshot --full --annotate --json") {
		t.Fatalf("full-page annotated flags missing: %q", joined)
	}
	if result["scope"] != "full_page" || result["annotated"] != true || result["provider_output"] == nil {
		t.Fatalf("full-page result mismatch: %#v", result)
	}
}

func TestCaptureScreenshotRejectsElementWithoutRef(t *testing.T) {
	runtime := NewRuntime(t.TempDir(), Options{})
	called := false
	result := runtime.CaptureScreenshot(Request{
		SessionID: "athena-test", Action: "screenshot",
		Arguments: map[string]any{"screenshot": true, "screenshot_scope": "element"},
	}, func(time.Duration, ...string) (string, error) {
		called = true
		return "", nil
	}, nil)
	if called || result["available"] != false {
		t.Fatalf("invalid element capture reached provider: called=%v result=%#v", called, result)
	}
}
