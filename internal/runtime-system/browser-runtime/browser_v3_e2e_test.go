package browser_runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	semantics "github.com/good-fish-man/athena-protocol/draft/v0alpha"
)

func TestE2EBrowserV3KeepsSessionAndSelectsSecondResult(t *testing.T) {
	if os.Getenv("ATHENA_BROWSER_E2E") != "1" {
		t.Skip("set ATHENA_BROWSER_E2E=1 with ATHENA_AGENT_BROWSER_BIN to run the real browser test")
	}
	if _, err := os.Stat(os.Getenv("ATHENA_AGENT_BROWSER_BIN")); err != nil {
		t.Fatalf("ATHENA_AGENT_BROWSER_BIN is unavailable: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if request.URL.Path == "/reference/continued" {
			_, _ = fmt.Fprint(w, `<!doctype html><html><head><title>Athena continued reference</title></head><body><main><h1>Stable tab continuation verified</h1></main></body></html>`)
			return
		}
		if strings.HasPrefix(request.URL.Path, "/video/") {
			title := strings.TrimPrefix(request.URL.Path, "/video/") + " tutorial"
			_, _ = fmt.Fprintf(w, `<!doctype html><html><head><title>%s</title></head><body><main><h1>%s</h1><video id="player" aria-label="%s video" controls muted width="480" height="270"></video><canvas id="frames" width="480" height="270" hidden></canvas><script>const canvas=document.querySelector('#frames');const context=canvas.getContext('2d');let frame=0;setInterval(()=>{context.fillStyle=frame%%2?'#123047':'#0c8f68';context.fillRect(0,0,480,270);context.fillStyle='white';context.font='32px sans-serif';context.fillText('Athena frame '+frame++,80,140)},80);document.querySelector('#player').srcObject=canvas.captureStream(12);</script></main></body></html>`, title, title, title)
			return
		}
		if request.URL.Path == "/reference" {
			_, _ = fmt.Fprint(w, `<!doctype html><html><head><title>Athena reference</title></head><body><main><h1>Reference page opened</h1><a aria-label="Continue reference" href="/reference/continued">Continue reference</a></main></body></html>`)
			return
		}
		_, _ = fmt.Fprintf(w, `<!doctype html><html><head><title>Athena v3 catalog</title></head><body><main aria-label="Video results"><h1>Video tutorials</h1><ol><li><a aria-label="First video" href="%s/video/first">First tutorial video</a></li><li><a aria-label="Second video" href="%s/video/second">Second tutorial video</a></li><li><a aria-label="Third video" href="%s/video/third">Third tutorial video</a></li></ol></main></body></html>`, serverURL(request), serverURL(request), serverURL(request))
	}))
	defer server.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	controller := newBrowserController(home)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sessionID, err := controller.ResolveSession("", true, false, server.URL)
	if err != nil {
		t.Fatalf("resolve browser session: %v", err)
	}

	opened, err := controller.RunTask(ctx, TaskRequest{
		RequestID: "e2e-open", SessionID: sessionID, Goal: "Open the Athena test catalog", Target: server.URL,
	})
	if err != nil {
		t.Fatalf("open catalog: %v", err)
	}
	if observedSession := browserStringValue(opened["session_id"]); observedSession != sessionID {
		t.Fatalf("open observation has no session: %#v", opened)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer closeCancel()
		_, _ = controller.RunAction(closeCtx, Request{SessionID: sessionID, Action: "close", Arguments: map[string]any{}})
		controller.CloseSession(sessionID)
	}()

	effectTrace := testBrowserSemanticTrace(t, "media.playback_state", "playing", sessionID, 2)
	semanticValue, err := semantics.ToMap(effectTrace.value)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := controller.RunTask(ctx, TaskRequest{
		RequestID: "e2e-select", SessionID: sessionID, Goal: "Play the second video on the current page",
		SemanticTrace: semanticValue,
	})
	if err != nil {
		t.Fatalf("play second video: %v; state=%#v", err, selected)
	}
	if got := browserStringValue(selected["session_id"]); got != sessionID {
		t.Fatalf("session changed: got=%q want=%q", got, sessionID)
	}
	if got := browserStringValue(selected["url"]); !strings.HasSuffix(strings.TrimRight(got, "/"), "/video/second") {
		t.Fatalf("second video was not opened: url=%q state=%#v", got, selected)
	}
	playback, _ := selected["playback"].(map[string]any)
	if playing, _ := playback["playing"].(bool); !playing || !browserPlaybackVerified(playback) {
		t.Fatalf("second video playback was not verified: %#v", playback)
	}
	plan, ok := selected["browser_task"].(browserTaskPlan)
	if !ok || !plan.Completed {
		t.Fatalf("browser task did not complete: %#v", selected["browser_task"])
	}
	verifiedTrace, err := semantics.TraceFromMap(selected[semantics.StateKey])
	if err != nil {
		t.Fatal(err)
	}
	if verifiedTrace == nil || verifiedTrace.TargetResolution == nil || verifiedTrace.TargetResolution.SelectedEntityRef == "" {
		t.Fatalf("real browser target was not grounded: %#v", verifiedTrace)
	}
	if verifiedTrace.VerificationSummary == nil || verifiedTrace.VerificationSummary.Status != semantics.OutcomeSucceeded {
		t.Fatalf("real browser outcome was not verified: %#v", verifiedTrace)
	}

	referenceURL := strings.Replace(server.URL, "127.0.0.1", "localhost", 1) + "/reference"
	reference, err := controller.RunTask(ctx, TaskRequest{
		RequestID: "e2e-reference", SessionID: sessionID, Goal: "Open the reference site in the same browser", Target: referenceURL,
	})
	if err != nil {
		t.Fatalf("open reference page: %v", err)
	}
	if got := browserStringValue(reference["session_id"]); got != sessionID {
		t.Fatalf("session changed while opening another tab: got=%q want=%q", got, sessionID)
	}
	if got := browserStringValue(reference["url"]); !strings.HasSuffix(strings.TrimRight(got, "/"), "/reference") {
		t.Fatalf("reference page was not opened: url=%q state=%#v", got, reference)
	}
	if count := browserE2ETabCount(reference); count < 2 {
		t.Fatalf("expected the existing browser session to contain multiple tabs, count=%d state=%#v", count, reference["tabs"])
	}

	executable, err := controller.executable()
	if err != nil {
		t.Fatalf("resolve agent-browser executable: %v", err)
	}
	tabsBefore := browserE2ECommandTabs(t, ctx, controller, executable, sessionID)
	videoTab, videoFound := browserE2EFindTab(tabsBefore, "/video/second")
	referenceTab, referenceFound := browserE2EFindTab(tabsBefore, "/reference")
	if !videoFound || !referenceFound || videoTab.Ref == referenceTab.Ref {
		t.Fatalf("could not ground the two real tabs before external close: %#v", tabsBefore)
	}
	closed, err := controller.RunAction(ctx, Request{
		RequestID: "e2e-close-stable-tab", SessionID: sessionID, Action: "close",
		Arguments: map[string]any{"tab_id": videoTab.Ref},
	})
	if err != nil {
		t.Fatalf("close stable tab through browser capability: %v", err)
	}
	if got := browserStringValue(closed["closed_tab_id"]); got != videoTab.Ref {
		t.Fatalf("browser capability reported the wrong closed tab: got=%q want=%q state=%#v", got, videoTab.Ref, closed)
	}
	tabsAfterClose := browserE2ECommandTabs(t, ctx, controller, executable, sessionID)
	if _, found := browserE2EFindTab(tabsAfterClose, "/video/second"); found {
		t.Fatalf("externally closed tab is still present: %#v", tabsAfterClose)
	}
	remainingReference, found := browserE2EFindTab(tabsAfterClose, "/reference")
	if !found || remainingReference.Ref != referenceTab.Ref {
		t.Fatalf("surviving tab lost its stable id: before=%#v after=%#v", referenceTab, tabsAfterClose)
	}

	reused, err := controller.RunAction(ctx, Request{
		RequestID: "e2e-reuse-stable-tab", SessionID: sessionID, Action: "navigate",
		Arguments: map[string]any{"url": referenceURL, "open_mode": "tab", "headed": true, "snapshot": true},
	})
	if err != nil {
		t.Fatalf("reuse surviving stable tab: %v", err)
	}
	if count := browserE2ETabCount(reused); count != len(tabsAfterClose) {
		t.Fatalf("stable target continuation opened a duplicate tab: before=%d after=%d state=%#v", len(tabsAfterClose), count, reused["tabs"])
	}
	if got := browserStringValue(reused["tab_id"]); got != referenceTab.Ref {
		t.Fatalf("runtime selected the wrong tab after index shift: got=%q want=%q", got, referenceTab.Ref)
	}

	continued, err := controller.RunTask(ctx, TaskRequest{
		RequestID: "e2e-continue-after-close", SessionID: sessionID,
		Goal: "On the current page, click Continue reference",
	})
	if err != nil {
		t.Fatalf("continue task after external tab close: %v; state=%#v", err, continued)
	}
	if got := browserStringValue(continued["url"]); !strings.HasSuffix(strings.TrimRight(got, "/"), "/reference/continued") {
		t.Fatalf("continued task acted on the wrong page: url=%q state=%#v", got, continued)
	}
}

func TestE2EBrowserPointerClicksGroundedCanvas(t *testing.T) {
	if os.Getenv("ATHENA_BROWSER_E2E") != "1" {
		t.Skip("set ATHENA_BROWSER_E2E=1 with ATHENA_AGENT_BROWSER_BIN to run the real browser test")
	}
	if _, err := os.Stat(os.Getenv("ATHENA_AGENT_BROWSER_BIN")); err != nil {
		t.Fatalf("ATHENA_AGENT_BROWSER_BIN is unavailable: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, `<!doctype html><html><head><title>Pointer before</title><style>html,body,canvas{position:fixed;inset:0;width:100%;height:100%;margin:0}</style></head><body><canvas id="surface"></canvas><script>const canvas=document.querySelector('#surface');const draw=color=>{canvas.width=innerWidth*devicePixelRatio;canvas.height=innerHeight*devicePixelRatio;const context=canvas.getContext('2d');context.fillStyle=color;context.fillRect(0,0,canvas.width,canvas.height)};draw('#b91c1c');canvas.addEventListener('click',()=>{draw('#15803d');document.title='Pointer after'})</script></body></html>`)
	}))
	defer server.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	controller := newBrowserController(home)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	sessionID, err := controller.ResolveSession("", true, false, server.URL)
	if err != nil {
		t.Fatalf("resolve browser session: %v", err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer closeCancel()
		_, _ = controller.RunAction(closeCtx, Request{SessionID: sessionID, Action: "close", Arguments: map[string]any{}})
		controller.CloseSession(sessionID)
	}()

	if _, err := controller.RunAction(ctx, Request{
		RequestID: "e2e-pointer-open", SessionID: sessionID, Action: "navigate",
		Arguments: map[string]any{"url": server.URL, "snapshot": true},
	}); err != nil {
		t.Fatalf("open pointer fixture: %v", err)
	}
	screenshot, err := controller.RunAction(ctx, Request{
		RequestID: "e2e-pointer-ground", SessionID: sessionID, Action: "screenshot", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("capture pointer grounding: %v", err)
	}
	grounding, ok := screenshot["pointer_grounding"].(map[string]any)
	if !ok || grounding["available"] != true {
		t.Fatalf("pointer grounding was not produced: grounding=%#v tab_id=%#v tabs=%#v title=%#v url=%#v", screenshot["pointer_grounding"], screenshot["tab_id"], screenshot["tabs"], screenshot["title"], screenshot["url"])
	}

	result, err := controller.RunAction(ctx, Request{
		RequestID: "e2e-pointer-click", SessionID: sessionID, Action: "pointer",
		Arguments: map[string]any{
			"operation": "click", "grounding_id": grounding["grounding_id"],
			"screenshot_id": grounding["screenshot_id"], "page_revision": grounding["page_revision"],
			"coordinate_space": "normalized_1000", "x": 500.0, "y": 500.0,
			"purpose": "Change the visual-only canvas state",
		},
	})
	if err != nil {
		t.Fatalf("execute grounded pointer click: %v; state=%#v", err, result)
	}
	if title := browserStringValue(result["title"]); title != "Pointer after" {
		t.Fatalf("pointer click did not change the canvas page: title=%q state=%#v", title, result)
	}
	verification, ok := browserVerificationFromState(result)
	if !ok || verification.Status != "verified" {
		t.Fatalf("pointer click was not verified: %#v", result["verification"])
	}
}

func serverURL(request *http.Request) string {
	return "http://" + request.Host
}

func browserE2ETabCount(state map[string]any) int {
	tabs, _ := state["tabs"].(map[string]any)
	switch count := tabs["count"].(type) {
	case int:
		return count
	case float64:
		return int(count)
	}
	if items, ok := tabs["items"].([]map[string]any); ok {
		return len(items)
	}
	if items, ok := tabs["items"].([]any); ok {
		return len(items)
	}
	return 0
}

func browserE2ECommandTabs(t *testing.T, ctx context.Context, controller *browserController, executable, sessionID string) []browserCommandTab {
	t.Helper()
	output := browserE2ECommand(t, ctx, controller, executable, sessionID, "tab", "list", "--json")
	tabs := parseBrowserCommandTabs(output)
	if len(tabs) == 0 {
		t.Fatalf("agent-browser returned no tabs: %s", output)
	}
	return tabs
}

func browserE2ECommand(t *testing.T, ctx context.Context, controller *browserController, executable, sessionID string, arguments ...string) string {
	t.Helper()
	args := append(append([]string{}, controller.sessionArgs(sessionID)...), arguments...)
	output, err := controller.browserCommand(ctx, executable, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("agent-browser %v: %v: %s", arguments, err, strings.TrimSpace(string(output)))
	}
	return cleanBrowserCommandOutput(string(output))
}

func browserE2EFindTab(tabs []browserCommandTab, pathSuffix string) (browserCommandTab, bool) {
	for _, tab := range tabs {
		if strings.HasSuffix(strings.TrimRight(tab.URL, "/"), pathSuffix) {
			return tab, true
		}
	}
	return browserCommandTab{}, false
}
