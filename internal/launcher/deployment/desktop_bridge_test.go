package deployment

import (
	browser_runtime "athena-launcher/internal/runtime-system/browser-runtime"
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopBridgeSearchesOnlyAuthorizedRoots(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "report.txt"), []byte("quarterly result"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("quarterly result"), 0o600); err != nil {
		t.Fatal(err)
	}
	matches, _, err := searchDesktopFiles(context.Background(), []string{root}, desktopSearchRequest{Roots: []string{root}, Query: "quarterly", Mode: "content"}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Name != "report.txt" {
		t.Fatalf("unexpected search result: %+v", matches)
	}
}

func TestDesktopBridgeRejectsApplicationCommands(t *testing.T) {
	for _, value := range []string{"/Applications/Music.app", "Music; rm -rf x", "https://example.com"} {
		if validateDesktopApplication(value) == nil {
			t.Fatalf("unsafe application value accepted: %q", value)
		}
	}
}

func TestDesktopAssetSwitchServesBridgeBeforeFrontendIsReady(t *testing.T) {
	tracker := newStartupTracker(t.TempDir())
	handler := newDesktopAssetSwitch(tracker, make(chan struct{}, 1), newStartupController())
	handler.SetDesktopBridge(newDesktopBridge(t.TempDir(), nil))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("GET", "http://wails.localhost/__athena/desktop/status", nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"available":true`) {
		t.Fatalf("unexpected bridge status: %d %s", response.Code, response.Body.String())
	}
}

func TestBrowserKeyElementsExtractsSemanticRefs(t *testing.T) {
	snapshot := `- textbox "Search" @e1
- link "AI Agent tutorial" [ref=e2] [url=https://www.youtube.com/watch?v=abc]
- button "Search" [ref=E3]`
	elements := browser_runtime.KeyElements(snapshot, 10)
	if len(elements) != 3 {
		t.Fatalf("unexpected elements: %+v", elements)
	}
	if elements[0]["ref"] != "@e1" || elements[1]["ref"] != "@e2" || elements[2]["ref"] != "@e3" {
		t.Fatalf("refs were not normalized: %+v", elements)
	}
	if elements[1]["label"] == "" {
		t.Fatalf("element label was not extracted: %+v", elements[1])
	}
}

func TestDetectBrowserChallengeRecognizesGoogleUnusualTraffic(t *testing.T) {
	challenge := browser_runtime.DetectChallenge(map[string]any{
		"url":     "https://www.google.com/sorry/index?continue=https://youtube.com",
		"title":   "Google",
		"content": "Our systems have detected unusual traffic from your computer network.",
	})
	if challenge == nil || challenge["kind"] != "google_unusual_traffic" {
		t.Fatalf("Google unusual traffic challenge was not detected: %+v", challenge)
	}
	if challenge["requires_user_takeover"] != true {
		t.Fatalf("challenge should require user takeover: %+v", challenge)
	}
}

func TestDetectBrowserChallengeIgnoresNormalPage(t *testing.T) {
	if challenge := browser_runtime.DetectChallenge(map[string]any{
		"url":     "https://www.youtube.com/",
		"title":   "YouTube",
		"content": "Search videos and channels",
	}); challenge != nil {
		t.Fatalf("normal page was detected as challenge: %+v", challenge)
	}
}

func TestBrowserSessionArgsUseExecutableOverride(t *testing.T) {
	browser := filepath.Join(t.TempDir(), "chrome")
	if err := os.WriteFile(browser, []byte("fake"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ATHENA_BROWSER_EXECUTABLE_PATH", browser)
	args := browser_runtime.SessionArgs("athena-00000000000000000000000000000000")
	if !containsString(args, "--executable-path") || !containsString(args, browser) {
		t.Fatalf("browser executable override missing from args: %v", args)
	}
}

func TestBrowserSessionArgsSupportProfileAuthMode(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATHENA_BROWSER_AUTH_MODE", "profile")
	args := browser_runtime.NewController(home).SessionArgs("athena-00000000000000000000000000000000")
	wantProfile := filepath.Join(home, "browser", "profiles", "default")
	if !containsString(args, "--profile") || !containsString(args, wantProfile) {
		t.Fatalf("profile auth mode missing profile args: %v", args)
	}
}

func TestBrowserSessionArgsSupportProfileAuthModeFromState(t *testing.T) {
	home := t.TempDir()
	state, err := loadState(home)
	if err != nil {
		t.Fatal(err)
	}
	state.BrowserAuthMode = "profile"
	state.BrowserProfile = "Default"
	if err := saveState(home, state); err != nil {
		t.Fatal(err)
	}
	args := browser_runtime.NewController(home).SessionArgs("athena-00000000000000000000000000000000")
	if !containsString(args, "--profile") || !containsString(args, "Default") || containsString(args, "--restore") {
		t.Fatalf("state profile auth mode not applied correctly: %v", args)
	}
}

func TestBrowserSessionArgsSupportAutoConnectAuthMode(t *testing.T) {
	t.Setenv("ATHENA_BROWSER_AUTH_MODE", "auto_connect")
	args := browser_runtime.NewController(t.TempDir()).SessionArgs("athena-00000000000000000000000000000000")
	if !containsString(args, "--auto-connect") {
		t.Fatalf("auto-connect auth mode missing args: %v", args)
	}
}

func TestExecutableFileExistsRejectsMissingPath(t *testing.T) {
	if browser_runtime.ExecutableFileExists(filepath.Join(t.TempDir(), "missing-chrome")) {
		t.Fatal("missing browser executable was accepted")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
