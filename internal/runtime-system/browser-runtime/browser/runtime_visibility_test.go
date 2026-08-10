package browser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeTracksVisibleBrowserSessions(t *testing.T) {
	runtime := NewRuntime(t.TempDir(), Options{
		AuthMode:    func() string { return "profile" },
		ProfilePath: func() string { return "Default" },
	})
	sessionID, err := runtime.ResolveSession("", true, false, "youtube")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.IsHeaded(sessionID) {
		t.Fatal("new session must not be marked visible before browser startup")
	}
	runtime.SetHeaded(sessionID, true)
	if !runtime.IsHeaded(sessionID) {
		t.Fatal("visible session state was not persisted")
	}
	runtime.CloseSession(sessionID)
	if runtime.IsHeaded(sessionID) {
		t.Fatal("closed session must not remain visible")
	}
}

func TestRuntimeDoesNotTrustPersistedVisibleState(t *testing.T) {
	home := t.TempDir()
	statePath := filepath.Join(home, "data", "browser-runtime-state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		t.Fatal(err)
	}
	state := runtimeState{
		Version: 1,
		Sessions: map[string]*browserSessionInfo{
			"athena-00000000000000000000000000000000": {
				ID: "athena-00000000000000000000000000000000", Headed: true, CreatedAt: time.Now(), UpdatedAt: time.Now(),
			},
		},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := NewRuntime(home, Options{})
	if runtime.IsHeaded("athena-00000000000000000000000000000000") {
		t.Fatal("persisted headed flag must be reset when Launcher starts")
	}
	if !runtime.WasLoadedHeaded("athena-00000000000000000000000000000000") {
		t.Fatal("the previous visible launch must remain available as a relaunch hint")
	}
}

func TestAutoConnectDoesNotRequireHeadedRelaunch(t *testing.T) {
	runtime := NewRuntime(t.TempDir(), Options{AuthMode: func() string { return "auto_connect" }})
	if runtime.RequiresHeadedLaunch() {
		t.Fatal("auto_connect must not close and relaunch the user's attached Chrome")
	}
}
