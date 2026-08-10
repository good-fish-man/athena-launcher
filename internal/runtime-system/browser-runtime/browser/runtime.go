package browser

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const runtimeStateVersion = 2
const runtimeDefaultSessionKey = "athena-browser"

const (
	sessionStatusCreated          = "CREATED"
	sessionStatusRunning          = "RUNNING"
	sessionStatusPaused           = "PAUSED"
	sessionStatusWaiting          = "WAITING"
	sessionStatusAutomationActive = "AUTOMATION_ACTIVE"
	sessionStatusError            = "ERROR"
	sessionStatusStopped          = "STOPPED"
)

type CommandRunner func(timeout time.Duration, args ...string) (string, error)

type Runtime struct {
	home       string
	statePath  string
	authMode   func() string
	profile    func() string
	executable func() string

	profiles   *browserProfileManager
	workspaces *browserWorkspaceManager
	windows    *browserWindowManager
	tabs       *browserTabManager
	navigation *browserNavigationManager
	dom        *browserDOMObserver
	downloads  *browserDownloadManager
	cookies    *browserCookieManager
	sessions   *browserSessionManager

	mu    sync.Mutex
	state runtimeState
	// loadedHeaded remembers whether a persisted session was last launched as
	// visible. The live agent-browser probe remains the source of truth.
	loadedHeaded map[string]bool
}

type runtimeState struct {
	Version         int                              `json:"version"`
	ActiveWorkspace string                           `json:"active_workspace,omitempty"`
	ActiveSession   string                           `json:"active_session,omitempty"`
	Workspaces      map[string]*browserWorkspaceInfo `json:"workspaces,omitempty"`
	Sessions        map[string]*browserSessionInfo   `json:"sessions,omitempty"`
	UpdatedAt       time.Time                        `json:"updated_at"`
}

type Snapshot struct {
	ActiveWorkspace string
	ActiveSession   string
	WorkspaceCount  int
	Sessions        map[string]SessionSnapshot
}

type SessionSnapshot struct {
	ID         string
	CurrentURL string
	Title      string
	Status     string
	Closed     bool
}

type browserWorkspaceInfo struct {
	ID              string    `json:"id"`
	Key             string    `json:"key"`
	Label           string    `json:"label"`
	ActiveSessionID string    `json:"active_session_id,omitempty"`
	SessionIDs      []string  `json:"session_ids,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type browserSessionInfo struct {
	ID          string                     `json:"id"`
	WorkspaceID string                     `json:"workspace_id"`
	TargetKey   string                     `json:"target_key,omitempty"`
	WindowID    string                     `json:"window_id"`
	ActiveTabID string                     `json:"active_tab_id"`
	Tabs        map[string]*browserTabInfo `json:"tabs,omitempty"`
	Profile     browserProfileInfo         `json:"profile"`
	CurrentURL  string                     `json:"current_url,omitempty"`
	Title       string                     `json:"title,omitempty"`
	LastAction  string                     `json:"last_action,omitempty"`
	Status      string                     `json:"status"`
	Headed      bool                       `json:"headed,omitempty"`
	Closed      bool                       `json:"closed,omitempty"`
	CreatedAt   time.Time                  `json:"created_at"`
	UpdatedAt   time.Time                  `json:"updated_at"`
}

type browserTabInfo struct {
	ID        string    `json:"id"`
	Label     string    `json:"label,omitempty"`
	URL       string    `json:"url,omitempty"`
	Title     string    `json:"title,omitempty"`
	Active    bool      `json:"active"`
	UpdatedAt time.Time `json:"updated_at"`
}

type browserProfileManager struct{ runtime *Runtime }
type browserWorkspaceManager struct{ runtime *Runtime }
type browserWindowManager struct{ runtime *Runtime }
type browserTabManager struct{ runtime *Runtime }
type browserNavigationManager struct{ runtime *Runtime }
type browserDOMObserver struct{ runtime *Runtime }
type browserDownloadManager struct{ runtime *Runtime }
type browserCookieManager struct{ runtime *Runtime }
type browserSessionManager struct{ runtime *Runtime }

type Options struct {
	AuthMode       func() string
	ProfilePath    func() string
	ExecutablePath func() string
}

func NewRuntime(home string, options Options) *Runtime {
	runtime := &Runtime{
		home:       home,
		statePath:  filepath.Join(home, "data", "browser-runtime-state.json"),
		authMode:   options.AuthMode,
		profile:    options.ProfilePath,
		executable: options.ExecutablePath,
		state: runtimeState{
			Version:    runtimeStateVersion,
			Workspaces: make(map[string]*browserWorkspaceInfo),
			Sessions:   make(map[string]*browserSessionInfo),
		},
		loadedHeaded: make(map[string]bool),
	}
	runtime.profiles = &browserProfileManager{runtime: runtime}
	runtime.workspaces = &browserWorkspaceManager{runtime: runtime}
	runtime.windows = &browserWindowManager{runtime: runtime}
	runtime.tabs = &browserTabManager{runtime: runtime}
	runtime.navigation = &browserNavigationManager{runtime: runtime}
	runtime.dom = &browserDOMObserver{runtime: runtime}
	runtime.downloads = &browserDownloadManager{runtime: runtime}
	runtime.cookies = &browserCookieManager{runtime: runtime}
	runtime.sessions = &browserSessionManager{runtime: runtime}
	runtime.load()
	return runtime
}

func (r *Runtime) load() {
	if strings.TrimSpace(r.home) == "" {
		return
	}
	data, err := os.ReadFile(r.statePath)
	if err != nil {
		return
	}
	var state runtimeState
	if json.Unmarshal(data, &state) != nil {
		return
	}
	if state.Workspaces == nil {
		state.Workspaces = make(map[string]*browserWorkspaceInfo)
	}
	if state.Sessions == nil {
		state.Sessions = make(map[string]*browserSessionInfo)
	}
	// Headed is a process-local launch guarantee, not durable browser state.
	// After Launcher restarts, the old daemon may be gone or headless, so the
	// next visible action must establish a fresh headed browser explicitly.
	for sessionID, session := range state.Sessions {
		if session != nil {
			if session.Headed {
				r.loadedHeaded[sessionID] = true
			}
			session.Headed = false
			if session.Closed {
				session.Status = sessionStatusStopped
			} else {
				session.Status = sessionStatusPaused
			}
		}
	}
	state.Version = runtimeStateVersion
	r.state = state
}

func (r *Runtime) saveLocked() {
	if strings.TrimSpace(r.home) == "" {
		return
	}
	r.state.UpdatedAt = time.Now().UTC()
	data, err := json.MarshalIndent(r.state, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(r.statePath), 0o700); err != nil {
		return
	}
	_ = os.WriteFile(r.statePath, data, 0o600)
}

func (r *Runtime) SessionArgs(sessionID string) []string {
	args := []string{"--namespace", "athena", "--session", sessionID}
	profile := r.profiles.resolve()
	switch profile.Mode {
	case "auto_connect":
		args = append(args, "--auto-connect")
	case "profile":
		if profile.Path != "" {
			args = append(args, "--profile", profile.Path)
		}
	default:
		args = append(args, "--restore", sessionID)
	}
	if executable := callString(r.executable); executable != "" {
		args = append(args, "--executable-path", executable)
	}
	return args
}

func (r *Runtime) ResolveSession(requested string, create bool, forceNew bool, targetKey string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions.resolveLocked(requested, create, forceNew, targetKey)
}

func (r *Runtime) CloseSession(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions.closeLocked(sessionID)
	r.saveLocked()
}

func (r *Runtime) HasSessionContent(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	session := r.state.Sessions[strings.TrimSpace(sessionID)]
	if session == nil || session.Closed {
		return false
	}
	if strings.TrimSpace(session.CurrentURL) != "" || strings.TrimSpace(session.Title) != "" {
		return true
	}
	for _, tab := range session.Tabs {
		if tab != nil && (strings.TrimSpace(tab.URL) != "" || strings.TrimSpace(tab.Title) != "") {
			return true
		}
	}
	return false
}

func (r *Runtime) IsHeaded(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	session := r.state.Sessions[strings.TrimSpace(sessionID)]
	return session != nil && !session.Closed && session.Headed
}

func (r *Runtime) WasLoadedHeaded(sessionID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.loadedHeaded[strings.TrimSpace(sessionID)]
}

func (r *Runtime) RequiresHeadedLaunch() bool {
	return r.profiles == nil || r.profiles.resolve().Mode != "auto_connect"
}

func (r *Runtime) SetHeaded(sessionID string, headed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sessionID = strings.TrimSpace(sessionID)
	if session := r.state.Sessions[sessionID]; session != nil {
		session.Headed = headed
		r.loadedHeaded[sessionID] = headed
		if headed && !session.Closed {
			session.Status = sessionStatusRunning
		}
		session.UpdatedAt = time.Now().UTC()
		r.saveLocked()
	}
}

func (r *Runtime) SetSessionStatus(sessionID, status string) {
	status = normalizeSessionStatus(status)
	if status == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if session := r.state.Sessions[strings.TrimSpace(sessionID)]; session != nil {
		session.Status = status
		session.Closed = status == sessionStatusStopped
		session.UpdatedAt = time.Now().UTC()
		r.saveLocked()
	}
}

func normalizeSessionStatus(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case sessionStatusCreated, sessionStatusRunning, sessionStatusPaused, sessionStatusWaiting,
		sessionStatusAutomationActive, sessionStatusError, sessionStatusStopped:
		return strings.ToUpper(strings.TrimSpace(status))
	default:
		return ""
	}
}

func (r *Runtime) Snapshot() Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	snapshot := Snapshot{
		ActiveWorkspace: r.state.ActiveWorkspace,
		ActiveSession:   r.state.ActiveSession,
		WorkspaceCount:  len(r.state.Workspaces),
		Sessions:        make(map[string]SessionSnapshot, len(r.state.Sessions)),
	}
	for id, session := range r.state.Sessions {
		if session == nil {
			continue
		}
		snapshot.Sessions[id] = SessionSnapshot{ID: id, CurrentURL: session.CurrentURL, Title: session.Title, Status: session.Status, Closed: session.Closed}
	}
	return snapshot
}

func (r *Runtime) PrepareDownload(request Request) (Request, string, error) {
	if request.Action != "download" {
		return request, "", nil
	}
	if request.Arguments == nil {
		request.Arguments = make(map[string]any)
	}
	path, _ := request.Arguments["path"].(string)
	if strings.TrimSpace(path) == "" {
		filename, _ := request.Arguments["filename"].(string)
		path = filepath.Join(r.downloads.directory(), safeBrowserDownloadFilename(filename, request.SessionID))
		request.Arguments["path"] = path
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return request, "", err
	}
	return request, path, nil
}

func (r *Runtime) DecorateObservation(request Request, observation map[string]any, run CommandRunner, sessionArgs []string) map[string]any {
	if observation == nil {
		observation = make(map[string]any)
	}
	if run != nil {
		r.tabs.probe(request, observation, run, sessionArgs)
		r.cookies.probe(observation, run, sessionArgs)
		r.sessions.probe(observation, run, sessionArgs)
		r.windows.probe(observation, run, sessionArgs)
		r.dom.probe(observation, run, sessionArgs)
		r.downloads.probe(request, observation)
	}
	r.enrichTakeoverRecovery(request, observation)
	r.mu.Lock()
	defer r.mu.Unlock()
	session := r.sessions.ensureLocked(request.SessionID, browserSessionTargetKey(request.Arguments))
	session.LastAction = request.Action
	if takeover, _ := observation["takeover"].(map[string]any); takeover != nil {
		session.Status = sessionStatusWaiting
	} else {
		session.Status = sessionStatusRunning
	}
	session.UpdatedAt = time.Now().UTC()
	r.navigation.updateLocked(session, request, observation)
	r.tabs.updateLocked(session, observation)
	r.windows.updateLocked(session, observation)
	r.dom.enrichLocked(session, observation)
	r.downloads.enrichLocked(request, observation)
	cookies := r.cookies.observationLocked(session)
	if status, ok := observation["cookie_status"].(map[string]any); ok {
		for key, value := range status {
			cookies[key] = value
		}
	}
	observation["browser_runtime"] = map[string]any{
		"profile":   session.Profile,
		"workspace": r.workspaces.observationLocked(session.WorkspaceID),
		"window":    r.windows.observationLocked(session),
		"tab":       r.tabs.observationLocked(session),
		"session":   r.sessions.observationLocked(session),
		"download":  r.downloads.observationLocked(request, observation),
		"cookies":   cookies,
	}
	observation["session_id"] = session.ID
	observation["workspace_id"] = session.WorkspaceID
	r.saveLocked()
	return observation
}

func (r *Runtime) enrichTakeoverRecovery(request Request, observation map[string]any) {
	challengeDetected, _ := observation["challenge_detected"].(bool)
	interventionRequired, _ := observation["user_intervention_required"].(bool)
	if !challengeDetected && !interventionRequired && !request.UserTakeover {
		return
	}
	reason := "user_takeover"
	if challengeDetected {
		reason = "browser_challenge"
	} else if interventionRequired {
		reason = "browser_authentication"
	}
	observation["takeover"] = map[string]any{
		"required":                   true,
		"reason":                     reason,
		"resume_capability":          "browser.observe",
		"resume_session_id":          request.SessionID,
		"keep_session_open":          true,
		"agent_should_not_reopen":    true,
		"message":                    "After the user completes the visible browser step, continue by observing this same browser session.",
		"raw_credentials_exposed":    false,
		"raw_verification_exposed":   false,
		"requires_visible_browser":   true,
		"safe_next_user_instruction": "Complete the visible verification/login step, then tell Athena to continue.",
	}
}

func (r *Runtime) CaptureScreenshot(request Request, run CommandRunner, sessionArgs []string) map[string]any {
	if run == nil || !shouldCaptureBrowserScreenshot(request) {
		return nil
	}
	scope := browserScreenshotScope(request.Arguments)
	ref := browserScreenshotRef(request.Arguments)
	annotate := browserBoolArgument(request.Arguments, "screenshot_annotate")
	if scope == "element" && ref == "" {
		return map[string]any{"available": false, "scope": scope, "error": "element screenshot requires a valid ref"}
	}
	directory := filepath.Join(r.home, "browser", "screenshots")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return map[string]any{"available": false, "scope": scope, "error": err.Error()}
	}
	path := filepath.Join(directory, request.SessionID+"-"+scope+"-"+time.Now().UTC().Format("20060102-150405.000")+".png")
	args := append(append([]string{}, sessionArgs...), "screenshot")
	if scope == "element" {
		args = append(args, ref)
	}
	if scope == "full_page" {
		args = append(args, "--full")
	}
	if annotate {
		args = append(args, "--annotate", "--json")
	}
	args = append(args, path)
	output, err := run(30*time.Second, args...)
	if err != nil {
		return map[string]any{"available": false, "scope": scope, "error": err.Error()}
	}
	info := map[string]any{
		"available": true, "path": path, "scope": scope, "annotated": annotate,
	}
	if ref != "" {
		info["ref"] = ref
	}
	if reason, _ := request.Arguments["screenshot_reason"].(string); strings.TrimSpace(reason) != "" {
		info["reason"] = strings.TrimSpace(reason)
	}
	if strings.TrimSpace(output) != "" {
		info["provider_output"] = redactBrowserScreenshotOutput(output)
	}
	if stat, statErr := os.Stat(path); statErr == nil {
		info["bytes"] = stat.Size()
		info["modified_at"] = stat.ModTime().UTC().Format(time.RFC3339Nano)
	}
	info["artifact"] = browserScreenshotArtifact(path)
	return info
}

func browserScreenshotArtifact(path string) map[string]any {
	artifact := map[string]any{
		"transport": "control_attachment", "model_image_input": true,
		"path": path,
	}
	file, err := os.Open(path)
	if err != nil {
		artifact["available"] = false
		artifact["error"] = err.Error()
		return artifact
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		artifact["available"] = false
		artifact["error"] = err.Error()
		return artifact
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path)))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	artifact["available"] = true
	artifact["id"] = "image-" + digest[:16]
	artifact["sha256"] = digest
	artifact["mime_type"] = mimeType
	return artifact
}
