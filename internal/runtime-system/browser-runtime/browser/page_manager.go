package browser

import (
	"strings"
	"time"
)

func (m *browserWindowManager) updateLocked(session *browserSessionInfo, observation map[string]any) {
	if session.WindowID == "" {
		session.WindowID = "window-" + session.ID
	}
	observation["window_id"] = session.WindowID
}

func (m *browserWindowManager) probe(observation map[string]any, run CommandRunner, sessionArgs []string) {
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

func (m *browserTabManager) probe(request Request, observation map[string]any, run CommandRunner, sessionArgs []string) {
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
			reconciled := make(map[string]*browserTabInfo, len(items))
			for _, item := range items {
				id, _ := item["id"].(string)
				if id == "" {
					continue
				}
				current := session.Tabs[id]
				if current == nil {
					current = &browserTabInfo{ID: id}
				}
				if label, _ := item["label"].(string); strings.TrimSpace(label) != "" {
					current.Label = label
				}
				current.URL, _ = item["url"].(string)
				current.Title, _ = item["title"].(string)
				current.Active, _ = item["active"].(bool)
				current.UpdatedAt = time.Now().UTC()
				reconciled[id] = current
				if current.Active {
					session.ActiveTabID = id
				}
			}
			if len(reconciled) > 0 {
				session.Tabs = reconciled
			}
		}
	}
	observation["tab_id"] = session.ActiveTabID
}

func (m *browserTabManager) observationLocked(session *browserSessionInfo) map[string]any {
	tab := session.Tabs[session.ActiveTabID]
	if tab == nil {
		return map[string]any{"id": session.ActiveTabID, "active": true}
	}
	return map[string]any{"id": tab.ID, "url": tab.URL, "title": tab.Title, "active": tab.Active}
}

func (m *browserNavigationManager) updateLocked(session *browserSessionInfo, request Request, observation map[string]any) {
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

func (m *browserDOMObserver) probe(observation map[string]any, run CommandRunner, sessionArgs []string) {
	if observation["screenshot"] != nil {
		return
	}
}
