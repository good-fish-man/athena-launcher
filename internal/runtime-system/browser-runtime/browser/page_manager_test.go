package browser

import "testing"

func TestTabManagerReconcilesCompleteProbe(t *testing.T) {
	session := &browserSessionInfo{
		ID: "athena-test", ActiveTabID: "stale",
		Tabs: map[string]*browserTabInfo{
			"stale": {ID: "stale", URL: "https://old.example", Active: true},
			"t1":    {ID: "t1", Label: "kept-label", URL: "https://before.example"},
		},
	}
	observation := map[string]any{
		"url": "https://current.example", "title": "Current", "tab_id": "t2",
		"tabs": map[string]any{"available": true, "items": []map[string]any{
			{"id": "t1", "url": "https://one.example", "title": "One", "active": false},
			{"id": "t2", "url": "https://current.example", "title": "Current", "active": true},
		}},
	}
	(&browserTabManager{}).updateLocked(session, observation)
	if len(session.Tabs) != 2 || session.Tabs["stale"] != nil {
		t.Fatalf("stale tabs were not reconciled: %#v", session.Tabs)
	}
	if session.ActiveTabID != "t2" || observation["tab_id"] != "t2" {
		t.Fatalf("active tab mismatch: session=%q observation=%#v", session.ActiveTabID, observation["tab_id"])
	}
	if session.Tabs["t1"].Label != "kept-label" {
		t.Fatalf("stable tab label was discarded: %#v", session.Tabs["t1"])
	}
}
