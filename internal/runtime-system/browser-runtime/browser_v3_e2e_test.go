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
		if request.URL.Path == "/second" {
			_, _ = fmt.Fprint(w, `<!doctype html><html><head><title>Second tutorial</title></head><body><main><h1>Second tutorial opened</h1></main></body></html>`)
			return
		}
		if request.URL.Path == "/reference" {
			_, _ = fmt.Fprint(w, `<!doctype html><html><head><title>Athena reference</title></head><body><main><h1>Reference page opened</h1></main></body></html>`)
			return
		}
		_, _ = fmt.Fprintf(w, `<!doctype html><html><head><title>Athena v3 catalog</title></head><body><main aria-label="Tutorial results"><h1>Tutorials</h1><ol><li><a href="%s/first">First tutorial</a></li><li><a href="%s/second">Second tutorial</a></li><li><a href="%s/third">Third tutorial</a></li></ol></main></body></html>`, serverURL(request), serverURL(request), serverURL(request))
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

	selected, err := controller.RunTask(ctx, TaskRequest{
		RequestID: "e2e-select", SessionID: sessionID, Goal: "Open the second result on the current page",
	})
	if err != nil {
		t.Fatalf("select second result: %v", err)
	}
	if got := browserStringValue(selected["session_id"]); got != sessionID {
		t.Fatalf("session changed: got=%q want=%q", got, sessionID)
	}
	if got := browserStringValue(selected["url"]); !strings.HasSuffix(strings.TrimRight(got, "/"), "/second") {
		t.Fatalf("second result was not opened: url=%q state=%#v", got, selected)
	}
	plan, ok := selected["browser_task"].(browserTaskPlan)
	if !ok || !plan.Completed {
		t.Fatalf("browser task did not complete: %#v", selected["browser_task"])
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
