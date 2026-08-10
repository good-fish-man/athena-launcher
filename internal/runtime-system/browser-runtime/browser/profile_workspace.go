package browser

import "time"

func (m *browserProfileManager) resolve() browserProfileInfo {
	mode := callString(m.runtime.authMode)
	if mode == "" {
		mode = "isolated"
	}
	info := browserProfileInfo{Mode: mode}
	if mode == "profile" {
		info.Path = callString(m.runtime.profile)
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
