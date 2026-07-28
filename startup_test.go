package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStartupTrackerPersistsStepFailure(t *testing.T) {
	home := t.TempDir()
	tracker := newStartupTracker(home)
	tracker.begin("manifest", "Reading manifest")
	tracker.complete("manifest", "Manifest verified")
	tracker.begin("database-package", "Downloading PostgreSQL")
	tracker.fail("database-package", os.ErrPermission)

	snapshot := tracker.current()
	if snapshot.State != "error" || !strings.Contains(snapshot.Error, "permission denied") {
		t.Fatalf("unexpected error snapshot: %+v", snapshot)
	}
	if snapshot.Steps[0].Status != "complete" || snapshot.Steps[1].Status != "error" {
		t.Fatalf("unexpected step states: %+v", snapshot.Steps[:2])
	}

	data, err := os.ReadFile(filepath.Join(home, startupStatusFile))
	if err != nil {
		t.Fatal(err)
	}
	var persisted startupSnapshot
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Error != snapshot.Error {
		t.Fatalf("persisted error = %q, want %q", persisted.Error, snapshot.Error)
	}
}

func TestStartupHandlerExposesStatusAndKnownLogs(t *testing.T) {
	home := t.TempDir()
	logDir := filepath.Join(home, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "launcher.log"), []byte("startup detail\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	retry := make(chan struct{}, 1)
	control := newStartupController()
	tracker := newStartupTracker(home)
	handler := startupHandler(tracker, retry, control)

	statusResponse := httptest.NewRecorder()
	handler.ServeHTTP(statusResponse, httptest.NewRequest(http.MethodGet, "/api/status", nil))
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"state":"starting"`) {
		t.Fatalf("status response = %d %s", statusResponse.Code, statusResponse.Body.String())
	}

	logResponse := httptest.NewRecorder()
	handler.ServeHTTP(logResponse, httptest.NewRequest(http.MethodGet, "/api/log?source=launcher", nil))
	if logResponse.Code != http.StatusOK || logResponse.Body.String() != "startup detail\n" {
		t.Fatalf("log response = %d %q", logResponse.Code, logResponse.Body.String())
	}

	invalidResponse := httptest.NewRecorder()
	handler.ServeHTTP(invalidResponse, httptest.NewRequest(http.MethodGet, "/api/log?source=../../secret", nil))
	if invalidResponse.Code != http.StatusBadRequest {
		t.Fatalf("invalid log source response = %d", invalidResponse.Code)
	}

	retryResponse := httptest.NewRecorder()
	handler.ServeHTTP(retryResponse, httptest.NewRequest(http.MethodPost, "/api/retry", nil))
	if retryResponse.Code != http.StatusAccepted {
		t.Fatalf("retry response = %d", retryResponse.Code)
	}
	select {
	case <-retry:
	default:
		t.Fatal("retry signal was not delivered")
	}

	updateResponse := httptest.NewRecorder()
	handler.ServeHTTP(updateResponse, httptest.NewRequest(http.MethodPost, "/api/update/check", nil))
	if updateResponse.Code != http.StatusAccepted {
		t.Fatalf("update check response = %d", updateResponse.Code)
	}
	select {
	case <-control.checkUpdate:
	default:
		t.Fatal("update check signal was not delivered")
	}

	applyResponse := httptest.NewRecorder()
	handler.ServeHTTP(applyResponse, httptest.NewRequest(http.MethodPost, "/api/update/apply", nil))
	if applyResponse.Code != http.StatusConflict {
		t.Fatalf("update apply without offer response = %d", applyResponse.Code)
	}

	dismissResponse := httptest.NewRecorder()
	handler.ServeHTTP(dismissResponse, httptest.NewRequest(http.MethodPost, "/api/update/dismiss", nil))
	if dismissResponse.Code != http.StatusConflict {
		t.Fatalf("update dismiss without deferrable offer response = %d", dismissResponse.Code)
	}
	tracker.offerUpdate([]packageUpdate{{Component: "agent-runtime", DisplayName: "Agent Runtime"}}, true)
	dismissResponse = httptest.NewRecorder()
	handler.ServeHTTP(dismissResponse, httptest.NewRequest(http.MethodPost, "/api/update/dismiss", nil))
	if dismissResponse.Code != http.StatusAccepted {
		t.Fatalf("deferrable update dismiss response = %d", dismissResponse.Code)
	}
	select {
	case <-control.dismissUpdate:
	default:
		t.Fatal("update dismiss signal was not delivered")
	}
}

func TestStartupPageContainsLiveStatusUI(t *testing.T) {
	for _, expected := range []string{"Startup Center", "/api/status", "/api/log?source=", "/api/update/apply", "Update and restart", "Open Athena"} {
		if !strings.Contains(startupPageHTML, expected) {
			t.Fatalf("startup page is missing %q", expected)
		}
	}
}

func TestTailFileReturnsOnlyCompleteTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "launcher.log")
	if err := os.WriteFile(path, []byte("first line\nsecond line\nthird line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := tailFile(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "third line\n" {
		t.Fatalf("tailFile() = %q", got)
	}
}
