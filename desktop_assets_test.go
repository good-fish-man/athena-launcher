package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesktopAssetSwitchMovesFromStartupToFrontend(t *testing.T) {
	home := t.TempDir()
	tracker := newStartupTracker(home)
	control := newStartupController()
	retry := make(chan struct{}, 1)
	handler := newDesktopAssetSwitch(tracker, retry, control)

	startupResponse := httptest.NewRecorder()
	handler.ServeHTTP(startupResponse, httptest.NewRequest("GET", "http://wails.localhost/", nil))
	if startupResponse.Code != 200 || !strings.Contains(startupResponse.Body.String(), "Preparing your agent workspace") {
		t.Fatalf("startup response = %d %q", startupResponse.Code, startupResponse.Body.String())
	}

	frontend := filepath.Join(home, "frontend")
	if err := os.MkdirAll(frontend, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontend, "index.html"), []byte("athena desktop interface"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := handler.UseFrontend(frontend); err != nil {
		t.Fatal(err)
	}
	tracker.ready("/")

	frontendResponse := httptest.NewRecorder()
	handler.ServeHTTP(frontendResponse, httptest.NewRequest("GET", "http://wails.localhost/chat", nil))
	if frontendResponse.Code != 200 || frontendResponse.Body.String() != "athena desktop interface" {
		t.Fatalf("frontend response = %d %q", frontendResponse.Code, frontendResponse.Body.String())
	}
}
