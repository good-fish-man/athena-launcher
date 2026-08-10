package deployment

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeDeployment(t *testing.T) {
	tests := []struct {
		name      string
		selection deploymentSelection
		wantMode  string
		wantURL   string
		wantError bool
	}{
		{name: "local clears URL", selection: deploymentSelection{Mode: "LOCAL", RemoteURL: "https://unused.example"}},
		{name: "https remote", selection: deploymentSelection{Mode: "remote", RemoteURL: "https://athena.example.com/"}, wantURL: "https://athena.example.com"},
		{name: "full public API URL", selection: deploymentSelection{Mode: "remote", RemoteURL: "https://athena.example.com/api/agent-runtime-client/v1"}, wantURL: "https://athena.example.com"},
		{name: "loopback remote client is preserved", selection: deploymentSelection{Mode: "remote", RemoteURL: "http://127.0.0.1:8090/"}, wantMode: connectionModeRemote, wantURL: "http://127.0.0.1:8090"},
		{name: "public HTTP rejected", selection: deploymentSelection{Mode: "remote", RemoteURL: "http://athena.example.com"}, wantError: true},
		{name: "credentials rejected", selection: deploymentSelection{Mode: "remote", RemoteURL: "https://user:pass@athena.example.com"}, wantError: true},
		{name: "query rejected", selection: deploymentSelection{Mode: "remote", RemoteURL: "https://athena.example.com?token=secret"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeDeployment(test.selection)
			if (err != nil) != test.wantError {
				t.Fatalf("normalizeDeployment() error = %v", err)
			}
			if err == nil && got.RemoteURL != test.wantURL {
				t.Fatalf("RemoteURL = %q, want %q", got.RemoteURL, test.wantURL)
			}
			if err == nil && test.wantMode != "" && got.Mode != test.wantMode {
				t.Fatalf("Mode = %q, want %q", got.Mode, test.wantMode)
			}
		})
	}
}

func TestSavedDeploymentSelectionPreservesLoopbackRemote(t *testing.T) {
	selection, err := savedDeploymentSelection(&launcherState{ConnectionMode: connectionModeRemote, RemoteClientURL: "http://127.0.0.1:8090", RemoteDeviceToken: "remote-token"})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Mode != connectionModeRemote || selection.RemoteURL != "http://127.0.0.1:8090" || selection.Token != "remote-token" {
		t.Fatalf("savedDeploymentSelection() = %+v, want loopback remote mode", selection)
	}
}

func TestDeploymentEndpointPersistsSelection(t *testing.T) {
	home := t.TempDir()
	tracker := newStartupTracker(home)
	control := newStartupController()
	tracker.awaitDeployment(deploymentSelection{Mode: connectionModeLocal})
	handler := startupHandler(tracker, make(chan struct{}, 1), control)
	request := httptest.NewRequest(http.MethodPost, "/api/deployment", strings.NewReader(`{"mode":"remote","remoteUrl":"https://athena.example.com/","deviceToken":"device-secret"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("deployment response = %d %q", response.Code, response.Body.String())
	}
	state, err := loadState(home)
	if err != nil {
		t.Fatal(err)
	}
	if state.ConnectionMode != connectionModeRemote || state.RemoteClientURL != "https://athena.example.com" || state.RemoteDeviceToken != "device-secret" {
		t.Fatalf("persisted deployment = %q %q token=%q", state.ConnectionMode, state.RemoteClientURL, state.RemoteDeviceToken)
	}
	select {
	case selected := <-control.deployment:
		if selected.RemoteURL != state.RemoteClientURL {
			t.Fatalf("signalled URL = %q", selected.RemoteURL)
		}
	default:
		t.Fatal("deployment selection was not signalled")
	}
}

func TestCheckRemoteClient(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://athena.example.com/healthz" {
			t.Fatalf("health URL = %q", request.URL.String())
		}
		return &http.Response{StatusCode: http.StatusNoContent, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
	})}
	if err := checkRemoteClientWithHTTPClient(context.Background(), "https://athena.example.com", client); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRemoteInstallsOnlyFrontend(t *testing.T) {
	home := t.TempDir()
	checksum := strings.Repeat("a", 64)
	manifest := &Manifest{
		Version: "1.0.0",
		Database: DatabaseSpec{Version: "16.0", Artifacts: map[string]Artifact{
			platformKey(): {URL: "https://downloads.example/postgres.tar.gz", SHA256: strings.Repeat("b", 64), Format: "tar.gz"},
		}},
		Services: []ServiceSpec{{Name: "agent-runtime", Order: 1, Artifacts: map[string]Artifact{
			platformKey(): {URL: "https://downloads.example/runtime.tar.gz", SHA256: strings.Repeat("c", 64), Format: "tar.gz", Executable: "agent-runtime"},
		}}},
		Frontend: &FrontendSpec{Root: "dist", Artifacts: map[string]Artifact{
			platformKey(): {URL: "https://downloads.example/ui.tar.gz", SHA256: checksum, Format: "tar.gz"},
		}},
	}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(home, "release-manifest.json")
	if err := os.WriteFile(manifestPath, manifestData, 0o600); err != nil {
		t.Fatal(err)
	}
	frontendTarget := filepath.Join(home, "frontend", manifest.Version)
	if err := os.MkdirAll(filepath.Join(frontendTarget, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontendTarget, ".artifact-sha256"), []byte(checksum+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(frontendTarget, "dist", "index.html"), []byte("Athena"), 0o600); err != nil {
		t.Fatal(err)
	}
	state := &launcherState{ConnectionMode: connectionModeRemote, RemoteClientURL: "https://athena.example.com", Installed: map[string]string{"frontend": manifest.Version}}
	loaded, frontendPath, err := prepareRemote(context.Background(), options{home: home, manifestSource: manifestPath}, nil, nil, true, state)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Version != manifest.Version || frontendPath != filepath.Join(frontendTarget, "dist") {
		t.Fatalf("remote preparation returned version=%q frontend=%q", loaded.Version, frontendPath)
	}
	for _, forbidden := range []string{"postgres", "services", "browser"} {
		if _, err := os.Stat(filepath.Join(home, forbidden)); !os.IsNotExist(err) {
			t.Fatalf("remote preparation created local component directory %q", forbidden)
		}
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
