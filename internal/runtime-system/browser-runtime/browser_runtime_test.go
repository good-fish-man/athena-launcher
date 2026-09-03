package browser_runtime

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
	youtube, err := runtime.ResolveSession("", true, false, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	qqMusic, err := runtime.ResolveSession("", true, false, "QQ Music")
	if err != nil {
		t.Fatal(err)
	}
	if youtube != qqMusic {
		t.Fatalf("different targets should share one browser session: youtube=%q qq=%q", youtube, qqMusic)
	}
	if snapshot := runtime.Snapshot(); snapshot.WorkspaceCount != 1 {
		t.Fatalf("expected one browser workspace, got %#v", snapshot)
	}
}

func TestBrowserOpenArgsPutHeadedBeforeCommand(t *testing.T) {
	args := browserOpenArgs([]string{"--session", "athena-test", "--profile", "Default"}, "https://www.youtube.com", true)
	want := []string{"--session", "athena-test", "--profile", "Default", "--headed", "open", "https://www.youtube.com"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("browser open args = %#v, want %#v", args, want)
	}
}

func TestBrowserAgentSessionActiveUsesSessionInfo(t *testing.T) {
	called := false
	active := browserAgentSessionActive(func(_ time.Duration, args ...string) (string, error) {
		called = true
		if !strings.HasSuffix(strings.Join(args, " "), "session info --json") {
			t.Fatalf("unexpected session probe: %v", args)
		}
		return `{"success":true,"data":{"active":true,"pid":1234}}`, nil
	}, []string{"--namespace", "athena", "--session", "athena-test"})
	if !called || !active {
		t.Fatalf("session probe called=%v active=%v", called, active)
	}
}

func TestPrepareBrowserRefreshTargetRestoresPersistedPage(t *testing.T) {
	const target = "https://example.com/"
	opened := false
	run := func(_ time.Duration, args ...string) (string, error) {
		command := strings.Join(args, " ")
		switch {
		case strings.HasSuffix(command, "get url"):
			if opened {
				return target, nil
			}
			return "about:blank", nil
		case strings.HasSuffix(command, "open "+target):
			opened = true
			return "", nil
		default:
			return "", fmt.Errorf("unexpected browser command: %s", command)
		}
	}

	got, restored, err := prepareBrowserRefreshTarget(run, []string{"--session", "athena-test"}, "about:blank", target)
	if err != nil {
		t.Fatal(err)
	}
	if !restored || !opened || got != target {
		t.Fatalf("refresh target = %q, restored=%v, opened=%v", got, restored, opened)
	}
}

func TestPrepareBrowserRefreshTargetRejectsBlankSession(t *testing.T) {
	_, _, err := prepareBrowserRefreshTarget(nil, nil, "about:blank", "")
	if err == nil || !strings.Contains(err.Error(), "no live browser page") {
		t.Fatalf("expected unavailable current-page error, got %v", err)
	}
}

func TestHeadedLaunchOnlyRestartsConfirmedLiveHeadlessSession(t *testing.T) {
	tests := []struct {
		name                                   string
		hasContent, live, headed, loadedHeaded bool
		want                                   bool
	}{
		{name: "stale persisted session", hasContent: true, live: false, want: false},
		{name: "visible session restored", hasContent: true, live: true, loadedHeaded: true, want: false},
		{name: "visible in current process", hasContent: true, live: true, headed: true, want: false},
		{name: "confirmed live headless session", hasContent: true, live: true, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := shouldRestartBrowserAsHeaded(true, test.hasContent, test.live, test.headed, test.loadedHeaded)
			if got != test.want {
				t.Fatalf("restart = %v, want %v", got, test.want)
			}
		})
	}
}

func TestBrowserObservationExpandsSparseProfileChooserSnapshot(t *testing.T) {
	interactiveSnapshots := 0
	expandedSnapshots := 0
	run := func(_ time.Duration, args ...string) (string, error) {
		command := strings.Join(args, " ")
		switch {
		case strings.Contains(command, " eval ") || strings.HasPrefix(command, "eval "):
			return `{"url":"https://stream.example/browse","title":"Stream","content":"Stream\\nWho's watching?\\nyufu\\nKids\\nAdd Profile\\nManage Profiles"}`, nil
		case strings.Contains(command, "snapshot -i"):
			interactiveSnapshots++
			return `@e6 link "Add Profile"`, nil
		case strings.Contains(command, "snapshot --urls"):
			expandedSnapshots++
			return `- link "Stream" [ref=e1, url=https://stream.example/]
- heading "Who's watching?" [ref=e2]
- link "yufu" [ref=e4, url=https://stream.example/SwitchProfile?tkn=secret-a]
- link "Kids" [ref=e5, url=https://stream.example/SwitchProfile?tkn=secret-b]
- link "Add Profile" [ref=e6]
- link "Manage Profiles" [ref=e3, url=https://stream.example/ManageProfiles]`, nil
		default:
			return "", nil
		}
	}

	state, err := browserObservation(run, []string{"--session", "athena-test"})
	if err != nil {
		t.Fatal(err)
	}
	if interactiveSnapshots != 1 || expandedSnapshots != 1 {
		t.Fatalf("snapshot calls interactive=%d expanded=%d", interactiveSnapshots, expandedSnapshots)
	}
	if state["observation_strategy"] != "interactive_plus_accessibility" {
		t.Fatalf("observation strategy = %#v", state["observation_strategy"])
	}
	choice, ok := currentPageSelectionCandidate(state, "yufu Netflix profile")
	if !ok || choice.Ref != "@e4" || browserElementTitle(choice) != "yufu" {
		t.Fatalf("profile choice = %#v, ok=%v", choice, ok)
	}
}

func TestReusableBrowserTabPrefersExactTarget(t *testing.T) {
	output := `{"success":true,"data":{"tabs":[{"active":false,"label":null,"tabId":"t1","title":"YouTube","url":"https://www.youtube.com/"},{"active":true,"label":"youtube","tabId":"t2","title":"Liked videos - YouTube","url":"https://www.youtube.com/playlist?list=LL"}]}}`
	run := func(time.Duration, ...string) (string, error) { return output, nil }
	ref := reusableBrowserTabRef(run, []string{"--session", "athena-test"}, "https://www.youtube.com", "youtube")
	if ref != "t1" {
		t.Fatalf("reusable tab=%q, want exact YouTube home tab t1", ref)
	}
}

func TestReusableBrowserTabDoesNotCrossOrigins(t *testing.T) {
	output := `{"tabs":[{"active":true,"tabId":"t1","title":"YouTube","url":"https://www.youtube.com/"}]}`
	run := func(time.Duration, ...string) (string, error) { return output, nil }
	if ref := reusableBrowserTabRef(run, nil, "https://y.qq.com/", "qq-music"); ref != "" {
		t.Fatalf("cross-origin tab was reused: %q", ref)
	}
}

func TestBrowserRuntimeDecoratesObservationWithManagers(t *testing.T) {
	runtime := newBrowserRuntime(t.TempDir())
	sessionID, err := runtime.ResolveSession("", true, false, "https://www.youtube.com")
	if err != nil {
		t.Fatal(err)
	}
	observation := runtime.DecorateObservation(browserExecuteRequest{
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
	home := t.TempDir()
	runtime := newBrowserRuntime(home)
	sessionID, err := runtime.ResolveSession("", true, false, "https://www.youtube.com")
	if err != nil {
		t.Fatal(err)
	}
	perception := newPerceptionLayer(home, runtime)
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
	youtube, err := runtime.ResolveSession("", true, false, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	qqMusic, err := runtime.ResolveSession("", true, true, "QQ Music")
	if err != nil {
		t.Fatal(err)
	}
	runtime.CloseSession(qqMusic)
	active, err := runtime.ResolveSession("", false, false, "")
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

func TestBrowserMediaIdentityRequiresItemURLSemantics(t *testing.T) {
	for _, address := range []string{
		"https://music.example/",
		"https://music.example/profile",
		"https://music.example/playlist/42",
		"https://music.example/album/42",
	} {
		if kind, id := browserMediaIdentity(address); kind != "" || id != "" {
			t.Fatalf("non-item URL %q classified as kind=%q id=%q", address, kind, id)
		}
	}
	if kind, id := browserMediaIdentity("https://music.example/songDetail/track-a"); kind != "audio" || id != "track-a" {
		t.Fatalf("song detail identity = kind=%q id=%q", kind, id)
	}
}

func TestMediaCandidatesAcceptSemanticKindForOpaqueItemURL(t *testing.T) {
	output := `{"candidates":[
		{"title":"First discovered track","url":"https://music.example/artist/opaque-title","kind":"audio","position":100}
	]}`
	candidates := parseBrowserMediaCandidates(output, "https://music.example/")
	if len(candidates) != 1 || candidates[0].Kind != "audio" || candidates[0].ID == "" {
		t.Fatalf("semantic media candidate = %#v", candidates)
	}
}

func TestBrowserDocumentObservationParsesSingleEvaluation(t *testing.T) {
	output := `{"success":true,"data":{"result":{"url":"https://example.com/feed","title":"Example","content":"First item"}}}`
	document, ok := parseBrowserDocumentObservation(output)
	if !ok || document["url"] != "https://example.com/feed" || document["content"] != "First item" {
		t.Fatalf("document observation = %#v, ok=%v", document, ok)
	}
}

func TestEnrichBrowserCandidatesUsesSinglePerceptionScan(t *testing.T) {
	commands := 0
	run := func(_ time.Duration, args ...string) (string, error) {
		commands++
		if !strings.Contains(strings.Join(args, " "), "eval") {
			return "", fmt.Errorf("unexpected browser command: %v", args)
		}
		return `{"data":{"result":{"media_candidates":[{"title":"Opaque song","url":"https://music.example/artist/opaque","kind":"audio","position":100}],"content_candidates":[{"title":"A substantial article title","url":"https://music.example/articles/one","context":"heading","score":7,"structured":true,"position":200}]}}}`, nil
	}
	state := enrichBrowserCandidates(run, nil, map[string]any{"url": "https://music.example/"})
	if commands != 1 {
		t.Fatalf("candidate extraction used %d browser commands, want 1", commands)
	}
	if len(browserMediaCandidates(state)) != 1 {
		t.Fatalf("media candidates = %#v", state["media_candidates"])
	}
	content, _ := state["content_candidates"].([]map[string]any)
	if len(content) != 1 || content[0]["context"] != "heading" {
		t.Fatalf("content candidates = %#v", state["content_candidates"])
	}
}

func TestContentCandidatesExcludeMetadataAndPreserveStories(t *testing.T) {
	output := `{"candidates":[
		{"title":"Inline article reference","url":"https://source.example/inline","position":20,"order":1,"context":"article_body","score":0},
		{"title":"First substantial story title","url":"https://source.example/first","position":100,"order":2,"context":"heading","score":7,"structured":true},
		{"title":"source.example","url":"https://forum.example/from?site=source.example","position":100,"order":2},
		{"title":"BlueBerry2001","url":"https://forum.example/user?id=BlueBerry2001","position":110,"order":3},
		{"title":"https://golang.org/conduct","url":"https://golang.org/conduct","position":120,"order":4},
		{"title":"Prefer the Forum on Google","url":"https://forum.example/preferences","position":130,"order":5},
		{"title":"Second substantial story title","url":"https://other.example/second","position":200,"order":6,"context":"heading","score":7,"structured":true}
	]}`
	candidates := parseBrowserContentCandidates(output, "https://forum.example/")
	if len(candidates) != 3 || candidates[0].Title != "First substantial story title" || candidates[1].Title != "Second substantial story title" || candidates[2].Title != "Inline article reference" {
		t.Fatalf("content candidates = %#v", candidates)
	}
	if candidates[0].Context != "heading" || candidates[2].Score != 0 {
		t.Fatalf("content structure was not preserved: %#v", candidates)
	}
}

func TestContentCandidatesPreserveHigherSemanticContextForDuplicateURL(t *testing.T) {
	output := `{"candidates":[
		{"title":"Article title","url":"https://community.example/team/article","position":100,"order":1,"context":"heading","score":7,"structured":true},
		{"title":"Add a comment to post - Article title","url":"https://community.example/team/article","position":160,"order":2,"context":"collection","score":5,"structured":true}
	]}`
	candidates := parseBrowserContentCandidates(output, "https://community.example/")
	if len(candidates) != 1 || candidates[0].Title != "Article title" || candidates[0].Context != "heading" {
		t.Fatalf("higher semantic duplicate was not preserved: %#v", candidates)
	}
}

func TestContentCandidatesExcludeSearchFacets(t *testing.T) {
	output := `{"candidates":[
		{"title":"Repositories (3k) results","url":"https://github.com/search?q=golang+agent&type=repositories","position":10,"order":1,"context":"collection","score":5},
		{"title":"Issues (27k) results","url":"https://github.com/search?q=golang+agent&type=issues","position":20,"order":2,"context":"collection","score":5},
		{"title":"microsoft/retina","url":"https://github.com/microsoft/retina","position":100,"order":3,"context":"collection","score":5},
		{"title":"google/adk-go","url":"https://github.com/google/adk-go","position":200,"order":4,"context":"collection","score":5}
	]}`
	candidates := parseBrowserContentCandidates(output, "https://github.com/search?q=golang+agent")
	if len(candidates) != 2 || candidates[0].Title != "microsoft/retina" || candidates[1].Title != "google/adk-go" {
		t.Fatalf("search facets polluted content candidates: %#v", candidates)
	}
}

func TestContentCandidatesExcludeSourcePreferences(t *testing.T) {
	output := `{"success":true,"data":{"result":{"candidates":[
		{"title":"Add as preferred on Google","url":"https://www.google.com/preferences/source?q=bbc.com","context":"collection","score":5},
		{"title":"Fact-checking a public claim","url":"https://news.example/articles/fact-check","context":"collection","score":5}
	]}}}`
	candidates := parseBrowserContentCandidates(output, "https://news.example/articles/current")
	if len(candidates) != 1 || candidates[0].Title != "Fact-checking a public claim" {
		t.Fatalf("source preference leaked into candidates: %#v", candidates)
	}
}

func TestContentCandidatesExcludeSocialShareLinks(t *testing.T) {
	output := `{"success":true,"data":{"result":{"candidates":[
		{"title":"Share to Facebook","url":"https://www.facebook.com/sharer/sharer.php?u=https://news.example/current","context":"collection","score":5},
		{"title":"A related analysis","url":"https://news.example/articles/analysis","context":"collection","score":5}
	]}}}`
	candidates := parseBrowserContentCandidates(output, "https://news.example/articles/current")
	if len(candidates) != 1 || candidates[0].Title != "A related analysis" {
		t.Fatalf("social share link leaked into candidates: %#v", candidates)
	}
}

func TestContentCandidatesExcludeProfileLinksFromDiscussionRows(t *testing.T) {
	output := `{"success":true,"data":{"result":{"candidates":[
		{"title":"First discussion title","url":"https://forum.example/t/first-discussion/10","context":"heading","score":7},
		{"title":"alice's profile","url":"https://forum.example/u/alice","context":"collection","score":5},
		{"title":"Second discussion title","url":"https://forum.example/t/second-discussion/20","context":"heading","score":7}
	]}}}`
	candidates := parseBrowserContentCandidates(output, "https://forum.example/")
	if len(candidates) != 2 || candidates[0].Title != "First discussion title" || candidates[1].Title != "Second discussion title" {
		t.Fatalf("profile link polluted discussion candidates: %#v", candidates)
	}
	if !isLowValueBrowserTarget("alice's profile", "https://forum.example/u/alice") {
		t.Fatal("profile target was not classified as low value")
	}
}

func TestBrowserRuntimePersistsSessionState(t *testing.T) {
	home := t.TempDir()
	first := newBrowserRuntime(home)
	sessionID, err := first.ResolveSession("", true, false, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	first.DecorateObservation(browserExecuteRequest{SessionID: sessionID, Action: "navigate"}, map[string]any{
		"url": "https://www.youtube.com", "title": "YouTube",
	}, nil, nil)

	second := newBrowserRuntime(home)
	if snapshot := second.Snapshot(); snapshot.Sessions[sessionID].Status != "PAUSED" {
		t.Fatalf("restored session should be paused before reconnect: %#v", snapshot.Sessions[sessionID])
	}
	reused, err := second.ResolveSession("", false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if reused != sessionID {
		t.Fatalf("persisted active session not restored: got=%q want=%q", reused, sessionID)
	}
	if snapshot := second.Snapshot(); snapshot.Sessions[sessionID].CurrentURL != "https://www.youtube.com" || snapshot.Sessions[sessionID].Status != "RUNNING" {
		t.Fatalf("persisted session state missing URL: %#v", snapshot)
	}
}

func TestBrowserRuntimePrepareDownloadCreatesSafePath(t *testing.T) {
	runtime := newBrowserRuntime(t.TempDir())
	request, path, err := runtime.PrepareDownload(browserExecuteRequest{
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
	sessionID, err := runtime.ResolveSession("", true, false, "YouTube")
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
	request := browserExecuteRequest{SessionID: sessionID, Action: "navigate", Arguments: map[string]any{
		"url": "https://youtube.com", "screenshot": true,
	}}
	observation := map[string]any{"url": "https://youtube.com", "title": "YouTube"}
	if screenshot := runtime.CaptureScreenshot(request, runner, []string{"--session", sessionID}); screenshot != nil {
		observation["screenshot"] = screenshot
	}
	observation = runtime.DecorateObservation(request, observation, runner, []string{"--session", sessionID})
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

func TestPerceptionLayerCompressesRawPageBeforeReturningObservation(t *testing.T) {
	home := t.TempDir()
	runtime := newBrowserRuntime(home)
	sessionID, err := runtime.ResolveSession("", true, false, "https://example.com")
	if err != nil {
		t.Fatal(err)
	}
	perception := newPerceptionLayer(home, runtime)
	observation := perception.ObserveBrowser(browserExecuteRequest{
		RequestID: "request-budget",
		SessionID: sessionID,
		Action:    "observe",
	}, map[string]any{
		"url":      "https://example.com",
		"title":    "Example",
		"content":  strings.Repeat("large page content ", 1000),
		"snapshot": strings.Repeat("snapshot row\n", 2000),
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": "link First"},
			{"ref": "@e2", "label": "button Second"},
		},
	}, nil, nil)

	if len(observation["content"].(string)) > 6000 || len(observation["snapshot"].(string)) > 12000 {
		t.Fatalf("perception budget was not applied: content=%d snapshot=%d", len(observation["content"].(string)), len(observation["snapshot"].(string)))
	}
	metadata, ok := observation["perception"].(map[string]any)
	if !ok || metadata["semantic"] == nil || metadata["visual"] == nil || metadata["spatial"] == nil {
		t.Fatalf("layered perception result missing: %#v", observation)
	}
}

func TestBrowserRuntimeAddsTakeoverRecoveryHint(t *testing.T) {
	runtime := newBrowserRuntime(t.TempDir())
	sessionID, err := runtime.ResolveSession("", true, false, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	observation := runtime.DecorateObservation(browserExecuteRequest{
		SessionID: sessionID,
		Action:    "navigate",
		Arguments: map[string]any{"url": "https://www.google.com/sorry/index"},
	}, map[string]any{"challenge_detected": true}, nil, nil)
	takeover, ok := observation["takeover"].(map[string]any)
	if !ok || takeover["resume_capability"] != "browser.observe" || takeover["agent_should_not_reopen"] != true {
		t.Fatalf("takeover recovery hint missing: %#v", observation)
	}
}

func TestClearStaleBrowserSessionConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ATHENA_AGENT_BROWSER_HOME", root)
	sessionID := "athena-11111111111111111111111111111111"
	path := filepath.Join(root, "namespaces", "athena", "run", sessionID+".config")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := clearStaleBrowserSessionConfig(sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale browser config still exists: %v", err)
	}
}

func TestRecoverableBrowserConnectionError(t *testing.T) {
	recoverable := []string{
		"Failed to connect: No such file or directory",
		"failed to connect to browser socket",
		"connection refused",
	}
	for _, detail := range recoverable {
		if !isRecoverableBrowserConnectionError(detail) {
			t.Fatalf("expected recoverable browser error: %q", detail)
		}
	}
	if isRecoverableBrowserConnectionError("navigation failed: invalid URL") {
		t.Fatal("unrelated browser error must not trigger session recovery")
	}
}

func stringifyTestValue(value any) string {
	return strings.TrimSpace(fmt.Sprintf("%v", value))
}
