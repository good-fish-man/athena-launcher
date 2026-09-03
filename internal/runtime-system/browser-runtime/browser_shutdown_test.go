package browser_runtime

import (
	"context"
	"strings"
	"testing"
)

func TestBrowserShutdownDoesNotCloseAutoConnectedChrome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATHENA_BROWSER_AUTH_MODE", browserAuthModeAutoConnect)
	t.Setenv("ATHENA_AGENT_BROWSER_BIN", t.TempDir()+"/missing-agent-browser")
	t.Setenv("PATH", t.TempDir())
	controller := newBrowserController(home)
	sessionID, err := controller.ResolveSession("", true, false, "browser")
	if err != nil {
		t.Fatal(err)
	}
	if err := controller.Shutdown(context.Background()); err != nil {
		t.Fatalf("auto-connect shutdown must not invoke the browser process: %v", err)
	}
	if session := controller.runtime.Snapshot().Sessions[sessionID]; !session.Closed {
		t.Fatalf("auto-connect session state was not released: %#v", session)
	}
}

func TestBrowserShutdownReportsManagedCloseFailureAndReleasesState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ATHENA_BROWSER_AUTH_MODE", browserAuthModeIsolated)
	t.Setenv("ATHENA_AGENT_BROWSER_BIN", t.TempDir()+"/missing-agent-browser")
	t.Setenv("PATH", t.TempDir())
	controller := newBrowserController(home)
	sessionID, err := controller.ResolveSession("", true, false, "browser")
	if err != nil {
		t.Fatal(err)
	}
	err = controller.Shutdown(context.Background())
	if err == nil || !strings.Contains(err.Error(), "close managed browser session") {
		t.Fatalf("managed shutdown error = %v", err)
	}
	if session := controller.runtime.Snapshot().Sessions[sessionID]; !session.Closed {
		t.Fatalf("managed session state was not released after close failure: %#v", session)
	}
}
