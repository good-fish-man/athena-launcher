package browser

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (m *browserSessionManager) resolveLocked(requested string, create bool, forceNew bool, targetKey string) (string, error) {
	requested = strings.TrimSpace(requested)
	if forceNew {
		session := m.ensureWithIDLocked(newDeviceBrowserSessionID(), targetKey)
		m.activateLocked(session)
		m.runtime.saveLocked()
		return session.ID, nil
	}
	if create {
		sessionID := stableBrowserSessionID(runtimeDefaultSessionKey)
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
		sessionID = stableBrowserSessionID(runtimeDefaultSessionKey)
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
		Profile: m.runtime.profiles.resolve(), Status: sessionStatusCreated, CreatedAt: now, UpdatedAt: now,
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
	session.Closed = false
	session.Status = sessionStatusRunning
	session.UpdatedAt = time.Now().UTC()
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
		session.Status = sessionStatusStopped
		session.Headed = false
		m.runtime.loadedHeaded[sessionID] = false
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
		"status": session.Status, "closed": session.Closed, "headed": session.Headed, "last_action": session.LastAction, "current_url": session.CurrentURL, "title": session.Title,
	}
}

func (m *browserSessionManager) probe(observation map[string]any, run CommandRunner, sessionArgs []string) {
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
