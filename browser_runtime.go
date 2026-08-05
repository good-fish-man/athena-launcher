package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const browserRuntimeStateVersion = 1
const browserRuntimeDefaultSessionKey = "athena-browser"

type browserCommandRunner func(timeout time.Duration, args ...string) (string, error)

type browserRuntime struct {
	home      string
	statePath string

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
	state browserRuntimeState
}

type browserRuntimeState struct {
	Version         int                              `json:"version"`
	ActiveWorkspace string                           `json:"active_workspace,omitempty"`
	ActiveSession   string                           `json:"active_session,omitempty"`
	Workspaces      map[string]*browserWorkspaceInfo `json:"workspaces,omitempty"`
	Sessions        map[string]*browserSessionInfo   `json:"sessions,omitempty"`
	UpdatedAt       time.Time                        `json:"updated_at"`
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

type browserProfileInfo struct {
	Mode string `json:"mode"`
	Path string `json:"path,omitempty"`
}

type browserProfileManager struct{ runtime *browserRuntime }
type browserWorkspaceManager struct{ runtime *browserRuntime }
type browserWindowManager struct{ runtime *browserRuntime }
type browserTabManager struct{ runtime *browserRuntime }
type browserNavigationManager struct{ runtime *browserRuntime }
type browserDOMObserver struct{ runtime *browserRuntime }
type browserDownloadManager struct{ runtime *browserRuntime }
type browserCookieManager struct{ runtime *browserRuntime }
type browserSessionManager struct{ runtime *browserRuntime }

func newBrowserRuntime(home string) *browserRuntime {
	runtime := &browserRuntime{
		home:      home,
		statePath: filepath.Join(home, "data", "browser-runtime-state.json"),
		state: browserRuntimeState{
			Version:    browserRuntimeStateVersion,
			Workspaces: make(map[string]*browserWorkspaceInfo),
			Sessions:   make(map[string]*browserSessionInfo),
		},
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

func (r *browserRuntime) load() {
	if strings.TrimSpace(r.home) == "" {
		return
	}
	data, err := os.ReadFile(r.statePath)
	if err != nil {
		return
	}
	var state browserRuntimeState
	if json.Unmarshal(data, &state) != nil {
		return
	}
	if state.Workspaces == nil {
		state.Workspaces = make(map[string]*browserWorkspaceInfo)
	}
	if state.Sessions == nil {
		state.Sessions = make(map[string]*browserSessionInfo)
	}
	state.Version = browserRuntimeStateVersion
	r.state = state
}

func (r *browserRuntime) saveLocked() {
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

func (r *browserRuntime) sessionArgs(sessionID string) []string {
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
	if executable := preferredBrowserExecutable(); executable != "" {
		args = append(args, "--executable-path", executable)
	}
	return args
}

func (r *browserRuntime) resolveSession(requested string, create bool, forceNew bool, targetKey string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions.resolveLocked(requested, create, forceNew, targetKey)
}

func (r *browserRuntime) closeSession(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions.closeLocked(sessionID)
	r.saveLocked()
}

func (r *browserRuntime) hasSessionContent(sessionID string) bool {
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

func (r *browserRuntime) prepareDownload(request browserExecuteRequest) (browserExecuteRequest, string, error) {
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

func (r *browserRuntime) decorateObservation(request browserExecuteRequest, observation map[string]any, run browserCommandRunner, sessionArgs []string) map[string]any {
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

func (r *browserRuntime) enrichTakeoverRecovery(request browserExecuteRequest, observation map[string]any) {
	challengeDetected, _ := observation["challenge_detected"].(bool)
	if !challengeDetected && !request.UserTakeover {
		return
	}
	reason := "user_takeover"
	if challengeDetected {
		reason = "browser_challenge"
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

func (r *browserRuntime) captureScreenshot(request browserExecuteRequest, run browserCommandRunner, sessionArgs []string) map[string]any {
	if run == nil || !shouldCaptureBrowserScreenshot(request) {
		return nil
	}
	directory := filepath.Join(r.home, "browser", "screenshots")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return map[string]any{"available": false, "error": err.Error()}
	}
	path := filepath.Join(directory, request.SessionID+"-"+time.Now().UTC().Format("20060102-150405.000")+".png")
	output, err := run(30*time.Second, append(sessionArgs, "screenshot", path)...)
	if err != nil {
		return map[string]any{"available": false, "error": err.Error()}
	}
	info := map[string]any{"available": true, "path": path}
	if strings.TrimSpace(output) != "" {
		info["output"] = truncateBrowserOutput(strings.TrimSpace(output), 2000)
	}
	if stat, statErr := os.Stat(path); statErr == nil {
		info["bytes"] = stat.Size()
		info["modified_at"] = stat.ModTime().UTC().Format(time.RFC3339Nano)
	}
	return info
}

func (m *browserProfileManager) resolve() browserProfileInfo {
	home := ""
	if m != nil && m.runtime != nil {
		home = m.runtime.home
	}
	mode := browserAuthMode(home)
	info := browserProfileInfo{Mode: mode}
	if mode == "profile" {
		info.Path = browserProfile(home)
	}
	return info
}

func (m *browserWorkspaceManager) ensureLocked(targetKey string) *browserWorkspaceInfo {
	normalized := normalizeBrowserWorkspaceKey(targetKey)
	if normalized == "" {
		normalized = "default"
	}
	id := stableBrowserSessionID("workspace:" + normalized)
	now := time.Now().UTC()
	if existing := m.runtime.state.Workspaces[id]; existing != nil {
		existing.UpdatedAt = now
		m.runtime.state.ActiveWorkspace = id
		return existing
	}
	workspace := &browserWorkspaceInfo{ID: id, Key: normalized, Label: browserWorkspaceLabel(normalized), CreatedAt: now, UpdatedAt: now}
	m.runtime.state.Workspaces[id] = workspace
	m.runtime.state.ActiveWorkspace = id
	return workspace
}

func (m *browserWorkspaceManager) observationLocked(workspaceID string) map[string]any {
	workspace := m.runtime.state.Workspaces[workspaceID]
	if workspace == nil {
		return nil
	}
	return map[string]any{
		"id": workspace.ID, "key": workspace.Key, "label": workspace.Label,
		"active_session_id": workspace.ActiveSessionID, "session_count": len(workspace.SessionIDs),
	}
}

func browserWorkspaceLabel(key string) string {
	if key == "" || key == "default" {
		return "Default Browser Workspace"
	}
	return key
}

func (m *browserSessionManager) resolveLocked(requested string, create bool, forceNew bool, targetKey string) (string, error) {
	requested = strings.TrimSpace(requested)
	if forceNew {
		session := m.ensureWithIDLocked(newDeviceBrowserSessionID(), targetKey)
		m.activateLocked(session)
		m.runtime.saveLocked()
		return session.ID, nil
	}
	if create {
		sessionID := stableBrowserSessionID(browserRuntimeDefaultSessionKey)
		session := m.ensureWithIDLocked(sessionID, targetKey)
		m.activateLocked(session)
		m.runtime.saveLocked()
		return session.ID, nil
	}
	if requested != "" {
		if !browserSessionPattern.MatchString(requested) {
			return "", fmt.Errorf("invalid browser session id")
		}
		session := m.ensureWithIDLocked(requested, targetKey)
		m.activateLocked(session)
		m.runtime.saveLocked()
		return session.ID, nil
	}
	if m.runtime.state.ActiveSession != "" {
		if session := m.runtime.state.Sessions[m.runtime.state.ActiveSession]; session != nil && !session.Closed {
			m.activateLocked(session)
			m.runtime.saveLocked()
			return session.ID, nil
		}
	}
	return "", fmt.Errorf("browser session is unavailable; open or navigate first")
}

func (m *browserSessionManager) ensureLocked(sessionID string, targetKey string) *browserSessionInfo {
	if strings.TrimSpace(sessionID) == "" {
		sessionID = stableBrowserSessionID(browserRuntimeDefaultSessionKey)
	}
	return m.ensureWithIDLocked(sessionID, targetKey)
}

func (m *browserSessionManager) ensureWithIDLocked(sessionID string, targetKey string) *browserSessionInfo {
	workspace := m.runtime.workspaces.ensureLocked(targetKey)
	if existing := m.runtime.state.Sessions[sessionID]; existing != nil {
		existing.Closed = false
		if existing.WorkspaceID == "" {
			existing.WorkspaceID = workspace.ID
		}
		if existing.TargetKey == "" {
			existing.TargetKey = normalizeBrowserSessionKey(targetKey)
		}
		if existing.Profile.Mode == "" {
			existing.Profile = m.runtime.profiles.resolve()
		}
		if existing.Tabs == nil {
			existing.Tabs = make(map[string]*browserTabInfo)
		}
		m.linkSessionLocked(workspace, sessionID)
		return existing
	}
	now := time.Now().UTC()
	session := &browserSessionInfo{
		ID: sessionID, WorkspaceID: workspace.ID, TargetKey: normalizeBrowserSessionKey(targetKey),
		WindowID: "window-" + sessionID, ActiveTabID: "tab-main", Tabs: make(map[string]*browserTabInfo),
		Profile: m.runtime.profiles.resolve(), CreatedAt: now, UpdatedAt: now,
	}
	session.Tabs[session.ActiveTabID] = &browserTabInfo{ID: session.ActiveTabID, Active: true, UpdatedAt: now}
	m.runtime.state.Sessions[session.ID] = session
	m.linkSessionLocked(workspace, session.ID)
	return session
}

func (m *browserSessionManager) linkSessionLocked(workspace *browserWorkspaceInfo, sessionID string) {
	for _, current := range workspace.SessionIDs {
		if current == sessionID {
			workspace.ActiveSessionID = sessionID
			return
		}
	}
	workspace.SessionIDs = append(workspace.SessionIDs, sessionID)
	workspace.ActiveSessionID = sessionID
}

func (m *browserSessionManager) activateLocked(session *browserSessionInfo) {
	m.runtime.state.ActiveSession = session.ID
	m.runtime.state.ActiveWorkspace = session.WorkspaceID
	if workspace := m.runtime.state.Workspaces[session.WorkspaceID]; workspace != nil {
		workspace.ActiveSessionID = session.ID
		workspace.UpdatedAt = time.Now().UTC()
	}
}

func (m *browserSessionManager) closeLocked(sessionID string) {
	if sessionID == "" {
		sessionID = m.runtime.state.ActiveSession
	}
	if session := m.runtime.state.Sessions[sessionID]; session != nil {
		session.Closed = true
		session.UpdatedAt = time.Now().UTC()
	}
	if m.runtime.state.ActiveSession == sessionID {
		m.runtime.state.ActiveSession = m.nextOpenSessionLocked(sessionID)
	}
}

func (m *browserSessionManager) nextOpenSessionLocked(excluding string) string {
	for id, session := range m.runtime.state.Sessions {
		if id != excluding && !session.Closed {
			return id
		}
	}
	return ""
}

func (m *browserSessionManager) observationLocked(session *browserSessionInfo) map[string]any {
	return map[string]any{
		"id": session.ID, "target_key": session.TargetKey, "active": m.runtime.state.ActiveSession == session.ID,
		"closed": session.Closed, "last_action": session.LastAction, "current_url": session.CurrentURL, "title": session.Title,
	}
}

func (m *browserWindowManager) updateLocked(session *browserSessionInfo, observation map[string]any) {
	if session.WindowID == "" {
		session.WindowID = "window-" + session.ID
	}
	observation["window_id"] = session.WindowID
}

func (m *browserWindowManager) probe(observation map[string]any, run browserCommandRunner, sessionArgs []string) {
	output, err := run(10*time.Second, append(sessionArgs, "get", "cdp-url")...)
	if err != nil {
		observation["window_diagnostics"] = map[string]any{"cdp_available": false, "error": err.Error()}
		return
	}
	cdpURL := strings.TrimSpace(output)
	observation["window_diagnostics"] = map[string]any{"cdp_available": cdpURL != "", "cdp_url_redacted": redactBrowserCDPURL(cdpURL)}
}

func (m *browserWindowManager) observationLocked(session *browserSessionInfo) map[string]any {
	return map[string]any{"id": session.WindowID, "active": true, "session_id": session.ID}
}

func (m *browserTabManager) probe(request browserExecuteRequest, observation map[string]any, run browserCommandRunner, sessionArgs []string) {
	output, err := run(10*time.Second, append(sessionArgs, "tab", "list", "--json")...)
	if err != nil {
		observation["tabs"] = map[string]any{"available": false, "error": err.Error()}
		return
	}
	tabs, activeID, raw := parseBrowserTabs(output)
	if len(tabs) == 0 {
		observation["tabs"] = map[string]any{"available": true, "raw": truncateBrowserOutput(raw, 4000)}
		return
	}
	observation["tabs"] = map[string]any{"available": true, "items": tabs, "active_tab_id": activeID, "count": len(tabs)}
	if activeID != "" {
		observation["tab_id"] = activeID
	}
}

func (m *browserTabManager) updateLocked(session *browserSessionInfo, observation map[string]any) {
	if session.Tabs == nil {
		session.Tabs = make(map[string]*browserTabInfo)
	}
	tabID := session.ActiveTabID
	if tabID == "" {
		tabID = "tab-main"
		session.ActiveTabID = tabID
	}
	tab := session.Tabs[tabID]
	if tab == nil {
		tab = &browserTabInfo{ID: tabID}
		session.Tabs[tabID] = tab
	}
	if probedID, _ := observation["tab_id"].(string); strings.TrimSpace(probedID) != "" {
		tabID = strings.TrimSpace(probedID)
		session.ActiveTabID = tabID
		tab = session.Tabs[tabID]
		if tab == nil {
			tab = &browserTabInfo{ID: tabID}
			session.Tabs[tabID] = tab
		}
	}
	tab.URL, _ = observation["url"].(string)
	tab.Title, _ = observation["title"].(string)
	tab.Active = true
	tab.UpdatedAt = time.Now().UTC()
	if tabs, ok := observation["tabs"].(map[string]any); ok {
		if items, ok := tabs["items"].([]map[string]any); ok {
			for _, item := range items {
				id, _ := item["id"].(string)
				if id == "" {
					continue
				}
				current := session.Tabs[id]
				if current == nil {
					current = &browserTabInfo{ID: id}
					session.Tabs[id] = current
				}
				current.Label, _ = item["label"].(string)
				current.URL, _ = item["url"].(string)
				current.Title, _ = item["title"].(string)
				current.Active, _ = item["active"].(bool)
				current.UpdatedAt = time.Now().UTC()
				if current.Active {
					session.ActiveTabID = id
				}
			}
		}
	}
	observation["tab_id"] = tab.ID
}

func (m *browserTabManager) observationLocked(session *browserSessionInfo) map[string]any {
	tab := session.Tabs[session.ActiveTabID]
	if tab == nil {
		return map[string]any{"id": session.ActiveTabID, "active": true}
	}
	return map[string]any{"id": tab.ID, "url": tab.URL, "title": tab.Title, "active": tab.Active}
}

func (m *browserNavigationManager) updateLocked(session *browserSessionInfo, request browserExecuteRequest, observation map[string]any) {
	if currentURL, _ := observation["url"].(string); currentURL != "" {
		session.CurrentURL = currentURL
	}
	if title, _ := observation["title"].(string); title != "" {
		session.Title = title
	}
	observation["navigation"] = map[string]any{
		"action": request.Action, "target": browserSessionTargetKey(request.Arguments),
		"postcondition_checked": true,
	}
}

func (m *browserDOMObserver) enrichLocked(session *browserSessionInfo, observation map[string]any) {
	elements, _ := observation["key_elements"].([]map[string]string)
	observation["dom"] = map[string]any{
		"observer": "accessibility_snapshot", "key_element_count": len(elements),
		"redacted": true, "session_id": session.ID,
	}
}

func (m *browserDOMObserver) probe(observation map[string]any, run browserCommandRunner, sessionArgs []string) {
	if observation["screenshot"] != nil {
		return
	}
}

func (m *browserDownloadManager) directory() string {
	if m.runtime.home == "" {
		return ""
	}
	return filepath.Join(m.runtime.home, "browser", "downloads")
}

func (m *browserDownloadManager) enrichLocked(request browserExecuteRequest, observation map[string]any) {
	if request.Action != "download" {
		return
	}
	observation["download"] = m.observationLocked(request, observation)
}

func (m *browserDownloadManager) probe(request browserExecuteRequest, observation map[string]any) {
	if request.Action != "download" {
		return
	}
	path, _ := request.Arguments["path"].(string)
	info := map[string]any{"requested": true, "path": path, "directory": m.directory(), "completed": false}
	if stat, err := os.Stat(path); err == nil {
		info["completed"] = true
		info["bytes"] = stat.Size()
		info["modified_at"] = stat.ModTime().UTC().Format(time.RFC3339Nano)
		info["filename"] = filepath.Base(path)
	} else if path != "" {
		info["error"] = err.Error()
	}
	observation["download"] = info
}

func (m *browserDownloadManager) observationLocked(request browserExecuteRequest, observation map[string]any) map[string]any {
	if existing, ok := observation["download"].(map[string]any); ok {
		if existing["directory"] == nil {
			existing["directory"] = m.directory()
		}
		existing["requires_user_approval"] = request.Action == "download"
		return existing
	}
	return map[string]any{
		"requested":              request.Action == "download",
		"directory":              m.directory(),
		"requires_user_approval": request.Action == "download",
	}
}

func (m *browserCookieManager) probe(observation map[string]any, run browserCommandRunner, sessionArgs []string) {
	output, err := run(10*time.Second, append(sessionArgs, "cookies", "get", "--json")...)
	if err != nil {
		observation["cookie_status"] = map[string]any{"available": false, "raw_cookies_exposed": false, "error": err.Error()}
		return
	}
	summary := summarizeBrowserCookies(output)
	observation["cookie_status"] = summary
}

func (m *browserCookieManager) observationLocked(session *browserSessionInfo) map[string]any {
	if session == nil {
		return map[string]any{"raw_cookies_exposed": false}
	}
	return map[string]any{
		"mode":                session.Profile.Mode,
		"raw_cookies_exposed": false,
		"summary":             "cookies stay inside the selected browser profile",
	}
}

func (m *browserSessionManager) probe(observation map[string]any, run browserCommandRunner, sessionArgs []string) {
	output, err := run(10*time.Second, append(sessionArgs, "session", "info", "--json")...)
	if err != nil {
		observation["session_diagnostics"] = map[string]any{"available": false, "error": err.Error()}
		return
	}
	var parsed any
	if json.Unmarshal([]byte(output), &parsed) != nil {
		observation["session_diagnostics"] = map[string]any{"available": true, "raw": truncateBrowserOutput(strings.TrimSpace(output), 4000)}
		return
	}
	observation["session_diagnostics"] = redactBrowserJSON(parsed)
}

func shouldCaptureBrowserScreenshot(request browserExecuteRequest) bool {
	if request.Arguments != nil {
		if disabled, ok := request.Arguments["screenshot"].(bool); ok && !disabled {
			return false
		}
	}
	switch request.Action {
	case "navigate", "extract", "click", "type", "press", "scroll", "wait", "download":
		return true
	default:
		return false
	}
}

func safeBrowserDownloadFilename(value string, sessionID string) string {
	value = strings.TrimSpace(filepath.Base(value))
	if value == "" || value == "." || value == string(filepath.Separator) {
		value = "download-" + sessionID + "-" + time.Now().UTC().Format("20060102-150405")
	}
	value = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == 0 {
			return '-'
		}
		return r
	}, value)
	return value
}

func redactBrowserCDPURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		if value == "" {
			return ""
		}
		return "[redacted]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func parseBrowserTabs(output string) ([]map[string]any, string, string) {
	var parsed any
	if json.Unmarshal([]byte(output), &parsed) != nil {
		return nil, "", strings.TrimSpace(output)
	}
	candidates := findBrowserTabObjects(parsed)
	tabs := make([]map[string]any, 0, len(candidates))
	activeID := ""
	for _, candidate := range candidates {
		item := map[string]any{}
		for _, key := range []string{"id", "label", "url", "title"} {
			if value, ok := candidate[key].(string); ok && strings.TrimSpace(value) != "" {
				item[key] = value
			}
		}
		if active, ok := candidate["active"].(bool); ok {
			item["active"] = active
			if active {
				if id, _ := item["id"].(string); id != "" {
					activeID = id
				}
			}
		}
		if len(item) > 0 {
			tabs = append(tabs, item)
		}
	}
	return tabs, activeID, ""
}

func findBrowserTabObjects(value any) []map[string]any {
	switch typed := value.(type) {
	case []any:
		var result []map[string]any
		for _, item := range typed {
			result = append(result, findBrowserTabObjects(item)...)
		}
		return result
	case map[string]any:
		if looksLikeBrowserTab(typed) {
			return []map[string]any{typed}
		}
		for _, key := range []string{"tabs", "items", "pages"} {
			if nested, ok := typed[key]; ok {
				if result := findBrowserTabObjects(nested); len(result) > 0 {
					return result
				}
			}
		}
	}
	return nil
}

func looksLikeBrowserTab(value map[string]any) bool {
	if _, ok := value["id"].(string); ok {
		if _, hasURL := value["url"].(string); hasURL {
			return true
		}
		if _, hasTitle := value["title"].(string); hasTitle {
			return true
		}
	}
	return false
}

func summarizeBrowserCookies(output string) map[string]any {
	summary := map[string]any{"available": true, "raw_cookies_exposed": false}
	var parsed any
	if json.Unmarshal([]byte(output), &parsed) != nil {
		summary["parse_error"] = true
		return summary
	}
	cookies := findBrowserCookieObjects(parsed)
	domains := make(map[string]bool)
	sessionCookies := 0
	for _, cookie := range cookies {
		if domain, _ := cookie["domain"].(string); strings.TrimSpace(domain) != "" {
			domains[domain] = true
		}
		if expires, ok := cookie["expires"]; !ok || expires == nil || expires == float64(-1) {
			sessionCookies++
		}
	}
	domainList := make([]string, 0, len(domains))
	for domain := range domains {
		domainList = append(domainList, domain)
	}
	summary["count"] = len(cookies)
	summary["has_cookies"] = len(cookies) > 0
	summary["session_cookie_count"] = sessionCookies
	summary["domains"] = domainList
	return summary
}

func findBrowserCookieObjects(value any) []map[string]any {
	switch typed := value.(type) {
	case []any:
		var result []map[string]any
		for _, item := range typed {
			result = append(result, findBrowserCookieObjects(item)...)
		}
		return result
	case map[string]any:
		if _, hasName := typed["name"].(string); hasName {
			if _, hasValue := typed["value"].(string); hasValue {
				return []map[string]any{typed}
			}
		}
		for _, key := range []string{"cookies", "items"} {
			if nested, ok := typed[key]; ok {
				if result := findBrowserCookieObjects(nested); len(result) > 0 {
					return result
				}
			}
		}
	}
	return nil
}

func redactBrowserJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed)+1)
		result["available"] = true
		for key, item := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "cookie") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "secret") {
				result[key] = "[redacted]"
				continue
			}
			result[key] = redactBrowserJSON(item)
		}
		return result
	case []any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, redactBrowserJSON(item))
		}
		return result
	default:
		return typed
	}
}
