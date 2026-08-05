package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrowserRuntimeReusesDefaultSessionForDifferentTargets(t *testing.T) {
	runtime := newBrowserRuntime(t.TempDir())
	youtube, err := runtime.resolveSession("", true, false, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	qqMusic, err := runtime.resolveSession("", true, false, "QQ Music")
	if err != nil {
		t.Fatal(err)
	}
	if youtube != qqMusic {
		t.Fatalf("different targets should share one browser session: youtube=%q qq=%q", youtube, qqMusic)
	}
	if len(runtime.state.Workspaces) != 1 {
		t.Fatalf("expected one browser workspace, got %#v", runtime.state.Workspaces)
	}
}

func TestBrowserRuntimeDecoratesObservationWithManagers(t *testing.T) {
	runtime := newBrowserRuntime(t.TempDir())
	sessionID, err := runtime.resolveSession("", true, false, "https://www.youtube.com")
	if err != nil {
		t.Fatal(err)
	}
	observation := runtime.decorateObservation(browserExecuteRequest{
		SessionID: sessionID,
		Action:    "navigate",
		Arguments: map[string]any{"url": "https://www.youtube.com"},
	}, map[string]any{
		"url":          "https://www.youtube.com",
		"title":        "YouTube",
		"key_elements": []map[string]string{{"ref": "@e1", "label": "textbox Search"}},
	}, nil, nil)

	if observation["session_id"] != sessionID || observation["workspace_id"] == "" {
		t.Fatalf("session/workspace metadata missing: %#v", observation)
	}
	metadata, ok := observation["browser_runtime"].(map[string]any)
	if !ok {
		t.Fatalf("browser runtime metadata missing: %#v", observation)
	}
	for _, key := range []string{"profile", "workspace", "window", "tab", "session", "download", "cookies"} {
		if metadata[key] == nil {
			t.Fatalf("browser runtime manager %q missing: %#v", key, metadata)
		}
	}
	cookies := metadata["cookies"].(map[string]any)
	if cookies["raw_cookies_exposed"] != false {
		t.Fatalf("raw cookies must never be exposed: %#v", cookies)
	}
}

func TestPerceptionLayerExposesObservationEngines(t *testing.T) {
	perception := newPerceptionLayer(t.TempDir(), newBrowserRuntime(t.TempDir()))
	if perception.browser == nil || perception.desktop == nil || perception.files == nil ||
		perception.terminal == nil || perception.vision == nil || perception.audio == nil {
		t.Fatalf("perception layer must expose all observation engines: %#v", perception)
	}
}

func TestPerceptionLayerObservesBrowserThroughBrowserEngine(t *testing.T) {
	runtime := newBrowserRuntime(t.TempDir())
	sessionID, err := runtime.resolveSession("", true, false, "https://www.youtube.com")
	if err != nil {
		t.Fatal(err)
	}
	perception := newPerceptionLayer(runtime.home, runtime)
	observation := perception.ObserveBrowser(browserExecuteRequest{
		SessionID: sessionID,
		Action:    "navigate",
		Arguments: map[string]any{"url": "https://www.youtube.com"},
	}, map[string]any{
		"url":   "https://www.youtube.com",
		"title": "YouTube",
	}, nil, nil)

	if observation["session_id"] != sessionID || observation["browser_runtime"] == nil {
		t.Fatalf("browser observation was not enriched through perception layer: %#v", observation)
	}
}

func TestBrowserRuntimeCloseFallsBackToAnotherOpenSession(t *testing.T) {
	runtime := newBrowserRuntime(t.TempDir())
	youtube, err := runtime.resolveSession("", true, false, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	qqMusic, err := runtime.resolveSession("", true, true, "QQ Music")
	if err != nil {
		t.Fatal(err)
	}
	runtime.closeSession(qqMusic)
	active, err := runtime.resolveSession("", false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if active != youtube {
		t.Fatalf("closing active session should fall back to remaining open session: got=%q want=%q", active, youtube)
	}
}

func TestBrowserTabLabelIsStableForTargets(t *testing.T) {
	if got := safeBrowserTabLabel("https://www.youtube.com/watch?v=1"); got != "youtube" {
		t.Fatalf("youtube label = %q", got)
	}
	if got := safeBrowserTabLabel("QQ Music"); got != "qq-music" {
		t.Fatalf("qq music label = %q", got)
	}
}

func TestBrowserRuntimePersistsSessionState(t *testing.T) {
	home := t.TempDir()
	first := newBrowserRuntime(home)
	sessionID, err := first.resolveSession("", true, false, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	first.decorateObservation(browserExecuteRequest{SessionID: sessionID, Action: "navigate"}, map[string]any{
		"url": "https://www.youtube.com", "title": "YouTube",
	}, nil, nil)

	second := newBrowserRuntime(home)
	reused, err := second.resolveSession("", false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if reused != sessionID {
		t.Fatalf("persisted active session not restored: got=%q want=%q", reused, sessionID)
	}
	if second.state.Sessions[sessionID].CurrentURL != "https://www.youtube.com" {
		t.Fatalf("persisted session state missing URL: %#v", second.state.Sessions[sessionID])
	}
}

func TestParseBrowserTabsFromJSON(t *testing.T) {
	tabs, activeID, raw := parseBrowserTabs(`{"tabs":[{"id":"t1","title":"YouTube","url":"https://youtube.com","active":true},{"id":"t2","label":"docs","title":"Docs","url":"https://docs.example"}]}`)
	if raw != "" || len(tabs) != 2 || activeID != "t1" {
		t.Fatalf("tabs=%#v active=%q raw=%q", tabs, activeID, raw)
	}
	if tabs[1]["label"] != "docs" {
		t.Fatalf("tab label missing: %#v", tabs[1])
	}
}

func TestSummarizeBrowserCookiesRedactsValues(t *testing.T) {
	summary := summarizeBrowserCookies(`{"cookies":[{"name":"SID","value":"secret","domain":".youtube.com"},{"name":"pref","value":"hidden","domain":".google.com","expires":-1}]}`)
	if summary["raw_cookies_exposed"] != false || summary["count"] != 2 || summary["has_cookies"] != true {
		t.Fatalf("bad summary: %#v", summary)
	}
	if text := stringifyTestValue(summary); text == "" || strings.Contains(text, "secret") || strings.Contains(text, "hidden") {
		t.Fatalf("cookie values leaked: %#v", summary)
	}
}

func TestBrowserRuntimePrepareDownloadCreatesSafePath(t *testing.T) {
	runtime := newBrowserRuntime(t.TempDir())
	request, path, err := runtime.prepareDownload(browserExecuteRequest{
		SessionID: "athena-00000000000000000000000000000000",
		Action:    "download",
		Arguments: map[string]any{"filename": "../report.pdf"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if path == "" || request.Arguments["path"] != path {
		t.Fatalf("download path not assigned: request=%#v path=%q", request, path)
	}
	if safeBrowserDownloadFilename("../report.pdf", request.SessionID) != "report.pdf" {
		t.Fatalf("unsafe filename was not sanitized")
	}
}

func TestBrowserDownloadSnapshotSeesPartialFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "report.pdf")
	partial := target + ".crdownload"
	if err := os.WriteFile(partial, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot := browserDownloadFileSnapshot(target, time.Now().Add(-time.Second))
	if snapshot["path"] != partial || snapshot["completed"] != false || snapshot["bytes"] != int64(len("partial")) {
		t.Fatalf("unexpected download snapshot: %#v", snapshot)
	}
	if err := os.WriteFile(target, []byte("complete"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot = browserDownloadFileSnapshot(target, time.Now().Add(-time.Second))
	if snapshot["path"] != target || snapshot["completed"] != true {
		t.Fatalf("completed download not detected: %#v", snapshot)
	}
	if got := browserDownloadProgress(50, 100, 15, 85); got != 50 {
		t.Fatalf("progress = %d, want 50", got)
	}
}

func TestBrowserRuntimeProbesRealBrowserStateBestEffort(t *testing.T) {
	home := t.TempDir()
	runtime := newBrowserRuntime(home)
	sessionID, err := runtime.resolveSession("", true, false, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	runner := func(timeout time.Duration, args ...string) (string, error) {
		command := strings.Join(args, " ")
		switch {
		case strings.Contains(command, "tab list"):
			return `{"tabs":[{"id":"t1","title":"YouTube","url":"https://youtube.com","active":true}]}`, nil
		case strings.Contains(command, "cookies get"):
			return `{"cookies":[{"name":"SID","value":"secret","domain":".youtube.com","expires":-1}]}`, nil
		case strings.Contains(command, "session info"):
			return `{"session":"athena","token":"secret-token"}`, nil
		case strings.Contains(command, "get cdp-url"):
			return `ws://127.0.0.1:9222/devtools/browser/abc?token=secret`, nil
		case strings.Contains(command, "screenshot"):
			path := args[len(args)-1]
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return "", err
			}
			return path, os.WriteFile(path, []byte("png"), 0o600)
		default:
			return "", nil
		}
	}
	request := browserExecuteRequest{SessionID: sessionID, Action: "navigate", Arguments: map[string]any{"url": "https://youtube.com"}}
	observation := map[string]any{"url": "https://youtube.com", "title": "YouTube"}
	if screenshot := runtime.captureScreenshot(request, runner, []string{"--session", sessionID}); screenshot != nil {
		observation["screenshot"] = screenshot
	}
	observation = runtime.decorateObservation(request, observation, runner, []string{"--session", sessionID})
	if observation["screenshot"] == nil || observation["tabs"] == nil || observation["cookie_status"] == nil || observation["session_diagnostics"] == nil {
		t.Fatalf("probe metadata missing: %#v", observation)
	}
	metadata := observation["browser_runtime"].(map[string]any)
	cookies := metadata["cookies"].(map[string]any)
	if cookies["count"] != 1 || strings.Contains(stringifyTestValue(cookies), "secret") {
		t.Fatalf("cookie summary incorrect or leaked: %#v", cookies)
	}
	diagnostics := observation["session_diagnostics"].(map[string]any)
	if diagnostics["token"] != "[redacted]" {
		t.Fatalf("session diagnostics were not redacted: %#v", diagnostics)
	}
}

func TestBrowserRuntimeAddsTakeoverRecoveryHint(t *testing.T) {
	runtime := newBrowserRuntime(t.TempDir())
	sessionID, err := runtime.resolveSession("", true, false, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	observation := runtime.decorateObservation(browserExecuteRequest{
		SessionID: sessionID,
		Action:    "navigate",
		Arguments: map[string]any{"url": "https://www.google.com/sorry/index"},
	}, map[string]any{"challenge_detected": true}, nil, nil)
	takeover, ok := observation["takeover"].(map[string]any)
	if !ok || takeover["resume_capability"] != "browser.observe" || takeover["agent_should_not_reopen"] != true {
		t.Fatalf("takeover recovery hint missing: %#v", observation)
	}
}

func stringifyTestValue(value any) string {
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}
