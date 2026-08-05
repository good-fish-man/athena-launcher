package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const startupStatusFile = "startup-status.json"

type startupStep struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	StartedAt  string `json:"startedAt,omitempty"`
	FinishedAt string `json:"finishedAt,omitempty"`
}

type startupSnapshot struct {
	State       string            `json:"state"`
	Message     string            `json:"message"`
	Error       string            `json:"error,omitempty"`
	Version     string            `json:"version"`
	FrontendURL string            `json:"frontendUrl,omitempty"`
	LogPath     string            `json:"logPath"`
	UpdatedAt   string            `json:"updatedAt"`
	Steps       []startupStep     `json:"steps"`
	Update      startupUpdate     `json:"update"`
	Deployment  startupDeployment `json:"deployment"`
}

type startupDeployment struct {
	Required    bool   `json:"required"`
	Mode        string `json:"mode"`
	RemoteURL   string `json:"remoteUrl,omitempty"`
	DeviceToken string `json:"deviceToken,omitempty"`
	Error       string `json:"error,omitempty"`
}

type startupUpdate struct {
	State      string          `json:"state"`
	Message    string          `json:"message,omitempty"`
	CanDefer   bool            `json:"canDefer"`
	Components []packageUpdate `json:"components,omitempty"`
}

type startupController struct {
	checkUpdate   chan struct{}
	applyUpdate   chan struct{}
	dismissUpdate chan struct{}
	deployment    chan deploymentSelection
}

func newStartupController() *startupController {
	return &startupController{
		checkUpdate:   make(chan struct{}, 1),
		applyUpdate:   make(chan struct{}, 1),
		dismissUpdate: make(chan struct{}, 1),
		deployment:    make(chan deploymentSelection, 1),
	}
}

type startupTracker struct {
	mu       sync.RWMutex
	home     string
	snapshot startupSnapshot
	listener func(startupSnapshot)
}

func newStartupTracker(home string) *startupTracker {
	tracker := &startupTracker{home: home}
	tracker.reset()
	return tracker
}

func (t *startupTracker) reset() {
	t.mu.Lock()
	deployment := t.snapshot.Deployment
	t.snapshot = startupSnapshot{
		State:      "starting",
		Message:    "Preparing Athena",
		Version:    launcherVersion,
		LogPath:    filepath.Join(t.home, "logs", "launcher.log"),
		Update:     startupUpdate{State: "idle"},
		Deployment: deployment,
		Steps: []startupStep{
			{ID: "manifest", Label: "Release manifest", Status: "pending"},
			{ID: "database-package", Label: "PostgreSQL package", Status: "pending"},
			{ID: "browser-package", Label: "Authenticated browser", Status: "pending"},
			{ID: "services-package", Label: "Runtime packages", Status: "pending"},
			{ID: "frontend-package", Label: "Athena interface", Status: "pending"},
			{ID: "configuration", Label: "Service configuration", Status: "pending"},
			{ID: "database", Label: "PostgreSQL", Status: "pending"},
			{ID: "agent-runtime", Label: "Agent Runtime", Status: "pending"},
			{ID: "agent-runtime-client", Label: "Runtime Client", Status: "pending"},
			{ID: "frontend", Label: "Athena UI", Status: "pending"},
		},
	}
	t.touchLocked()
	t.mu.Unlock()
	t.persist()
}

func (t *startupTracker) awaitDeployment(selection deploymentSelection) {
	t.mu.Lock()
	t.snapshot.State = "configuration"
	t.snapshot.Message = "Choose where Athena should connect"
	t.snapshot.Deployment = startupDeployment{Required: true, Mode: selection.Mode, RemoteURL: selection.RemoteURL, DeviceToken: selection.Token}
	t.touchLocked()
	t.mu.Unlock()
	t.persist()
}

func (t *startupTracker) deploymentError(err error) {
	t.mu.Lock()
	if err != nil {
		t.snapshot.Deployment.Error = err.Error()
	}
	t.touchLocked()
	t.mu.Unlock()
	t.persist()
}

func (t *startupTracker) deploymentConfigured(selection deploymentSelection) {
	t.mu.Lock()
	t.snapshot.State = "starting"
	t.snapshot.Message = "Preparing Athena"
	t.snapshot.Deployment = startupDeployment{Mode: selection.Mode, RemoteURL: selection.RemoteURL, DeviceToken: selection.Token}
	t.touchLocked()
	t.mu.Unlock()
	t.persist()
}

func (t *startupTracker) checkingForUpdates() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.snapshot.Update = startupUpdate{State: "checking", Message: "Checking remote package hashes"}
	t.touchLocked()
	t.mu.Unlock()
	t.persist()
}

func (t *startupTracker) offerUpdate(updates []packageUpdate, canDefer bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.snapshot.Update = startupUpdate{State: "available", Message: fmt.Sprintf("%d package updates are available", len(updates)), CanDefer: canDefer, Components: append([]packageUpdate(nil), updates...)}
	t.touchLocked()
	t.mu.Unlock()
	t.persist()
}

func (t *startupTracker) applyingUpdate() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.snapshot.Update.State = "applying"
	t.snapshot.Update.Message = "Stopping current services and applying updates"
	t.snapshot.Update.CanDefer = false
	t.touchLocked()
	t.mu.Unlock()
	t.persist()
}

func (t *startupTracker) clearUpdate(message string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.snapshot.Update = startupUpdate{State: "none", Message: message}
	t.touchLocked()
	t.mu.Unlock()
	t.persist()
}

func (t *startupTracker) updateError(err error) {
	if t == nil || err == nil {
		return
	}
	t.mu.Lock()
	t.snapshot.Update = startupUpdate{State: "error", Message: err.Error(), CanDefer: true}
	t.touchLocked()
	t.mu.Unlock()
	t.persist()
}

func (t *startupTracker) begin(id, detail string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.snapshot.State = "starting"
	t.snapshot.Message = detail
	t.snapshot.Error = ""
	if step := t.stepLocked(id); step != nil {
		step.Status = "running"
		step.Detail = detail
		step.StartedAt = time.Now().Format(time.RFC3339)
		step.FinishedAt = ""
	}
	t.touchLocked()
	t.mu.Unlock()
	t.persist()
}

func (t *startupTracker) complete(id, detail string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	if step := t.stepLocked(id); step != nil {
		step.Status = "complete"
		step.Detail = detail
		step.FinishedAt = time.Now().Format(time.RFC3339)
	}
	t.snapshot.Message = detail
	t.touchLocked()
	t.mu.Unlock()
	t.persist()
}

func (t *startupTracker) fail(id string, err error) {
	if t == nil || err == nil {
		return
	}
	message := err.Error()
	t.mu.Lock()
	t.snapshot.State = "error"
	t.snapshot.Message = "Athena could not start"
	t.snapshot.Error = message
	if step := t.stepLocked(id); step != nil {
		step.Status = "error"
		step.Detail = message
		step.FinishedAt = time.Now().Format(time.RFC3339)
	}
	t.touchLocked()
	t.mu.Unlock()
	t.persist()
}

func (t *startupTracker) ready(frontendURL string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.snapshot.State = "ready"
	t.snapshot.Message = "Athena is ready"
	t.snapshot.Error = ""
	t.snapshot.FrontendURL = frontendURL
	t.touchLocked()
	t.mu.Unlock()
	t.persist()
}

func (t *startupTracker) current() startupSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	copy := t.snapshot
	copy.Steps = append([]startupStep(nil), t.snapshot.Steps...)
	return copy
}

func (t *startupTracker) stepLocked(id string) *startupStep {
	for index := range t.snapshot.Steps {
		if t.snapshot.Steps[index].ID == id {
			return &t.snapshot.Steps[index]
		}
	}
	return nil
}

func (t *startupTracker) touchLocked() {
	t.snapshot.UpdatedAt = time.Now().Format(time.RFC3339)
}

func (t *startupTracker) persist() {
	snapshot := t.current()
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(t.home, startupStatusFile)
	temporary := path + ".tmp"
	if err := os.MkdirAll(t.home, 0o700); err != nil {
		return
	}
	if err := os.WriteFile(temporary, data, 0o600); err == nil {
		_ = os.Rename(temporary, path)
	}
	t.mu.RLock()
	listener := t.listener
	t.mu.RUnlock()
	if listener != nil {
		listener(snapshot)
	}
}

func (t *startupTracker) listen(listener func(startupSnapshot)) {
	t.mu.Lock()
	t.listener = listener
	t.mu.Unlock()
}

type startupServer struct {
	server  *http.Server
	address string
	retry   chan struct{}
	control *startupController
}

func startStartupServer(tracker *startupTracker, control *startupController) (*startupServer, error) {
	address := fmt.Sprintf("127.0.0.1:%d", defaultStartupPort)
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("startup center listen %s: %w", address, err)
	}
	retry := make(chan struct{}, 1)
	server := &http.Server{Addr: address, Handler: requestErrorLogger("startup-center", startupHandler(tracker, retry, control)), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			fmt.Fprintln(os.Stderr, "[startup-center]", err)
		}
	}()
	return &startupServer{server: server, address: address, retry: retry, control: control}, nil
}

func startupHandler(tracker *startupTracker, retry chan<- struct{}, control *startupController) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/api/status", func(response http.ResponseWriter, _ *http.Request) {
		writeStartupJSON(response, tracker.current())
	})
	mux.HandleFunc("/api/log", func(response http.ResponseWriter, request *http.Request) {
		name := request.URL.Query().Get("source")
		path, ok := startupLogPath(tracker.home, name)
		if !ok {
			http.Error(response, "unknown log source", http.StatusBadRequest)
			return
		}
		response.Header().Set("Content-Type", "text/plain; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		content, err := tailFile(path, 128<<10)
		if err != nil && !os.IsNotExist(err) {
			http.Error(response, err.Error(), http.StatusInternalServerError)
			return
		}
		_, _ = response.Write(content)
	})
	mux.HandleFunc("/api/retry", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodPost)
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if retry == nil {
			http.Error(response, "retry is unavailable", http.StatusServiceUnavailable)
			return
		}
		select {
		case retry <- struct{}{}:
		default:
		}
		response.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/api/deployment", func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			response.Header().Set("Allow", http.MethodPost)
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if control == nil || !tracker.current().Deployment.Required {
			http.Error(response, "deployment selection is not currently available", http.StatusConflict)
			return
		}
		var requested deploymentSelection
		decoder := json.NewDecoder(io.LimitReader(request.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&requested); err != nil {
			http.Error(response, "invalid deployment selection: "+err.Error(), http.StatusBadRequest)
			return
		}
		selection, err := normalizeDeployment(requested)
		if err != nil {
			tracker.deploymentError(err)
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		state, err := loadState(tracker.home)
		if err != nil {
			tracker.deploymentError(err)
			http.Error(response, err.Error(), http.StatusInternalServerError)
			return
		}
		state.ConnectionMode = selection.Mode
		state.RemoteClientURL = selection.RemoteURL
		state.RemoteDeviceToken = selection.Token
		if err := saveState(tracker.home, state); err != nil {
			tracker.deploymentError(err)
			http.Error(response, err.Error(), http.StatusInternalServerError)
			return
		}
		tracker.deploymentConfigured(selection)
		select {
		case control.deployment <- selection:
		default:
		}
		writeStartupJSON(response, selection)
	})
	mux.HandleFunc("/api/browser-settings", func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			state, err := loadState(tracker.home)
			if err != nil {
				http.Error(response, err.Error(), http.StatusInternalServerError)
				return
			}
			writeStartupJSON(response, browserSettingsFromState(tracker.home, state))
		case http.MethodPost:
			var requested browserSettingsRequest
			decoder := json.NewDecoder(io.LimitReader(request.Body, 16<<10))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&requested); err != nil {
				http.Error(response, "invalid browser settings: "+err.Error(), http.StatusBadRequest)
				return
			}
			state, err := loadState(tracker.home)
			if err != nil {
				http.Error(response, err.Error(), http.StatusInternalServerError)
				return
			}
			applyBrowserSettings(state, requested)
			if err := saveState(tracker.home, state); err != nil {
				http.Error(response, err.Error(), http.StatusInternalServerError)
				return
			}
			writeStartupJSON(response, browserSettingsFromState(tracker.home, state))
		default:
			response.Header().Set("Allow", strings.Join([]string{http.MethodGet, http.MethodPost}, ", "))
			http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/update/check", func(response http.ResponseWriter, request *http.Request) {
		if !acceptStartupAction(response, request, control != nil) {
			return
		}
		tracker.checkingForUpdates()
		signalStartupAction(control.checkUpdate)
		response.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/api/update/apply", func(response http.ResponseWriter, request *http.Request) {
		if !acceptStartupAction(response, request, control != nil) {
			return
		}
		if tracker.current().Update.State != "available" {
			http.Error(response, "no package update is awaiting approval", http.StatusConflict)
			return
		}
		tracker.applyingUpdate()
		signalStartupAction(control.applyUpdate)
		response.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/api/update/dismiss", func(response http.ResponseWriter, request *http.Request) {
		if !acceptStartupAction(response, request, control != nil) {
			return
		}
		update := tracker.current().Update
		if update.State != "available" || !update.CanDefer {
			http.Error(response, "no deferrable package update is awaiting approval", http.StatusConflict)
			return
		}
		tracker.clearUpdate("Update postponed")
		signalStartupAction(control.dismissUpdate)
		response.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("/", func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("Content-Type", "text/html; charset=utf-8")
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'")
		_, _ = response.Write([]byte(startupPageHTML))
	})
	return mux
}

func acceptStartupAction(response http.ResponseWriter, request *http.Request, available bool) bool {
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	if !available {
		http.Error(response, "action is unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func signalStartupAction(channel chan<- struct{}) {
	select {
	case channel <- struct{}{}:
	default:
	}
}

func (s *startupServer) Stop(ctx context.Context) error {
	if s == nil || s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func startupLogPath(home, name string) (string, bool) {
	files := map[string]string{
		"launcher": "launcher.log",
		"postgres": "postgres.log",
		"runtime":  "agent-runtime.log",
		"client":   "agent-runtime-client.log",
	}
	file, ok := files[name]
	if !ok {
		return "", false
	}
	return filepath.Join(home, "logs", file), true
}

func tailFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	start := info.Size() - limit
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, 0); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil, err
	}
	if start > 0 {
		if newline := strings.IndexByte(string(data), '\n'); newline >= 0 {
			data = data[newline+1:]
		}
	}
	return data, nil
}

func writeStartupJSON(response http.ResponseWriter, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(response).Encode(value)
}
