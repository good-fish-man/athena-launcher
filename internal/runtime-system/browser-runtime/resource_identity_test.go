package browser_runtime

import (
	"testing"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"
)

func TestAttachBrowserResourceIdentityUsesActiveTabAndPerceptionVersion(t *testing.T) {
	observation := map[string]any{
		"session_id": "athena-0123456789abcdef0123456789abcdef", "tab_id": "tab-2",
		"url": "https://fixture.local/videos", "title": "Videos",
		"perception": map[string]any{"incremental": perception.IncrementalObservation{CurrentFingerprint: "page-v7"}},
	}
	result := attachBrowserResourceIdentity(browserExecuteRequest{SessionID: "ignored"}, observation)
	if got := result["resource_ref"]; got != "browser://session/athena-0123456789abcdef0123456789abcdef/tab/tab-2" {
		t.Fatalf("unexpected resource ref: %v", got)
	}
	if got := result["resource_version"]; got != "page-v7" {
		t.Fatalf("unexpected resource version: %v", got)
	}
}

func TestAttachBrowserResourceIdentityFallbackIsDeterministicAndChangesWithPage(t *testing.T) {
	request := browserExecuteRequest{SessionID: "athena-0123456789abcdef0123456789abcdef"}
	first := attachBrowserResourceIdentity(request, map[string]any{"url": "https://fixture.local/a", "title": "A"})
	second := attachBrowserResourceIdentity(request, map[string]any{"url": "https://fixture.local/a", "title": "A"})
	if first["resource_version"] != second["resource_version"] {
		t.Fatal("same page produced different resource versions")
	}
	third := attachBrowserResourceIdentity(request, map[string]any{"url": "https://fixture.local/b", "title": "B"})
	if first["resource_version"] == third["resource_version"] {
		t.Fatal("different pages produced the same resource version")
	}
}
