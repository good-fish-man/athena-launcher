package browser_runtime

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

const automationTestSession = "athena-0123456789abcdef0123456789abcdef"

func TestBrowserAutomationCreateListAndPersist(t *testing.T) {
	engine := newBrowserAutomationEngine(t.TempDir())
	result, err := engine.Manage(context.Background(), AutomationRequest{
		Operation: "create", SessionID: automationTestSession, CooldownMS: 1000,
		Trigger: BrowserAutomationTrigger{Type: "element_appeared", Target: BrowserAutomationSelector{Name: "Skip Ad"}},
		Action:  BrowserAutomationAction{Type: "click", Target: BrowserAutomationSelector{Name: "Skip Ad"}},
		Verification: BrowserAutomationVerification{
			Type: "element_disappeared", Target: BrowserAutomationSelector{Name: "Skip Ad"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rule, ok := result["rule"].(BrowserAutomationRule)
	if !ok || rule.ID == "" || !rule.Enabled || rule.Status != "watching" {
		t.Fatalf("rule = %#v", result["rule"])
	}
	listed := engine.list(automationTestSession)
	if len(listed) != 1 || listed[0].ID != rule.ID {
		t.Fatalf("listed = %#v", listed)
	}
	info, err := os.Stat(engine.path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("automation store mode = %o", info.Mode().Perm())
	}
	reloaded := newBrowserAutomationEngine(engine.home)
	if rules := reloaded.list(automationTestSession); len(rules) != 1 || rules[0].ID != rule.ID {
		t.Fatalf("reloaded rules = %#v", rules)
	}
}

func TestBrowserAutomationVerificationRequiresObservedTransition(t *testing.T) {
	verification := BrowserAutomationVerification{
		Type: "element_disappeared", Target: BrowserAutomationSelector{Name: "Skip Ad"},
	}
	before := map[string]any{"key_elements": []map[string]string{{"ref": "@e1", "label": "button Skip Ad"}}}
	after := map[string]any{"key_elements": []map[string]string{{"ref": "@e2", "label": "button Play"}}}
	result := browserAutomationVerify(verification, before, after)
	if !result.Verified || result.Reason != "target_element_disappeared" {
		t.Fatalf("verification = %#v", result)
	}
	if result := browserAutomationVerify(verification, after, after); result.Verified {
		t.Fatalf("unobserved transition was accepted: %#v", result)
	}
}

func TestBrowserAutomationListReturnsPersistedEvents(t *testing.T) {
	engine := newBrowserAutomationEngine(t.TempDir())
	engine.recordEvents([]browserAutomationEvent{{
		Type: "page_loaded", SessionID: automationTestSession, URL: "https://example.com", At: time.Now().UTC(),
	}})
	result, err := engine.Manage(context.Background(), AutomationRequest{Operation: "list", SessionID: automationTestSession})
	if err != nil {
		t.Fatal(err)
	}
	events, ok := result["events"].([]browserAutomationEvent)
	if !ok || len(events) != 1 || events[0].Status != "observed" || events[0].ID == "" {
		t.Fatalf("events = %#v", result["events"])
	}
	reloaded := newBrowserAutomationEngine(engine.home)
	if events := reloaded.recentEvents(automationTestSession, "", 10); len(events) != 1 || events[0].URL != "https://example.com" {
		t.Fatalf("reloaded events = %#v", events)
	}
}

func TestBrowserAutomationRejectsSensitiveUnattendedAction(t *testing.T) {
	engine := newBrowserAutomationEngine(t.TempDir())
	_, err := engine.Manage(context.Background(), AutomationRequest{
		Operation: "create", SessionID: automationTestSession,
		Trigger: BrowserAutomationTrigger{Type: "element_appeared", Target: BrowserAutomationSelector{Name: "Place order"}},
		Action:  BrowserAutomationAction{Type: "click", Target: BrowserAutomationSelector{Name: "Place order"}},
	})
	if err == nil {
		t.Fatal("sensitive automation was accepted")
	}
}

func TestBrowserAutomationEventsAreIncremental(t *testing.T) {
	previous := browserAutomationSnapshot{URL: "https://example.com", Elements: map[string]BrowserAutomationSelector{}}
	current := browserAutomationSnapshot{
		URL: "https://example.com/watch", Playing: true,
		Elements: map[string]BrowserAutomationSelector{"@e2|skip ad": {Name: "Skip Ad"}},
	}
	events := browserAutomationEvents(automationTestSession, previous, current, true)
	types := make(map[string]bool)
	for _, event := range events {
		types[event.Type] = true
	}
	for _, expected := range []string{"page_loaded", "page_changed", "element_appeared", "video_started"} {
		if !types[expected] {
			t.Fatalf("missing %s in %#v", expected, events)
		}
	}
}

func TestParseBrowserAutomationProbe(t *testing.T) {
	probe, ok := parseBrowserAutomationProbe(`{
		"success":true,
		"data":{"result":{"athena_probe":true,"url":"https://example.com/watch","title":"Video",
		"elements":[{"role":"button","name":"Skip Ad","url":""}],
		"media":{"playing":true,"paused":false,"ended":false},"blocked":false}}
	}`)
	if !ok || probe.URL != "https://example.com/watch" || !probe.Playing || len(probe.Elements) != 1 {
		t.Fatalf("probe = %#v, ok=%t", probe, ok)
	}
	for _, element := range probe.Elements {
		if element.Role != "button" || element.Name != "Skip Ad" {
			t.Fatalf("element = %#v", element)
		}
	}
}

func TestBrowserAutomationRuleMatchingAndCooldown(t *testing.T) {
	rule := BrowserAutomationRule{
		Enabled: true, CooldownMS: 5000,
		Trigger: BrowserAutomationTrigger{Type: "element_appeared", Target: BrowserAutomationSelector{Name: "Skip Ad"}},
	}
	event := browserAutomationEvent{Type: "element_appeared", Target: BrowserAutomationSelector{Name: "Skip Ad button"}}
	if !browserAutomationRuleMatches(rule, event, browserAutomationSnapshot{}) {
		t.Fatal("semantic automation target did not match")
	}
	now := time.Now().UTC()
	rule.LastFiredAt = &now
	if browserAutomationRuleReady(rule, now.Add(time.Second)) {
		t.Fatal("automation ignored cooldown")
	}
	if !browserAutomationRuleReady(rule, now.Add(6*time.Second)) {
		t.Fatal("automation remained blocked after cooldown")
	}
}

func TestBrowserAutomationPauseResumeDelete(t *testing.T) {
	engine := newBrowserAutomationEngine(t.TempDir())
	created, err := engine.Manage(context.Background(), AutomationRequest{
		Operation: "create", SessionID: automationTestSession,
		Trigger: BrowserAutomationTrigger{Type: "video_ended"},
		Action:  BrowserAutomationAction{Type: "press", Value: "ArrowDown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created["rule"].(BrowserAutomationRule).ID
	if _, err := engine.Manage(context.Background(), AutomationRequest{Operation: "pause", AutomationID: id}); err != nil {
		t.Fatal(err)
	}
	if rule, _ := engine.get(id); rule.Enabled || rule.Status != "paused" {
		t.Fatalf("paused rule = %#v", rule)
	}
	if _, err := engine.Manage(context.Background(), AutomationRequest{Operation: "resume", AutomationID: id}); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.Manage(context.Background(), AutomationRequest{Operation: "delete", AutomationID: id}); err != nil {
		t.Fatal(err)
	}
	if _, ok := engine.get(id); ok {
		t.Fatal("deleted automation still exists")
	}
}

func TestBrowserAutomationSessionStateReportsWatchMode(t *testing.T) {
	engine := newBrowserAutomationEngine(t.TempDir())
	created, err := engine.Manage(context.Background(), AutomationRequest{
		Operation: "create", SessionID: automationTestSession,
		Trigger: BrowserAutomationTrigger{Type: "video_ended"},
		Action:  BrowserAutomationAction{Type: "press", Value: "ArrowDown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created["rule"].(BrowserAutomationRule).ID
	state := engine.sessionState(automationTestSession)
	if state["mode"] != "watch" || state["active_count"] != 1 {
		t.Fatalf("watch state = %#v", state)
	}
	if _, err := engine.Manage(context.Background(), AutomationRequest{Operation: "pause", AutomationID: id}); err != nil {
		t.Fatal(err)
	}
	state = engine.sessionState(automationTestSession)
	if state["mode"] != "idle" || state["active_count"] != 0 {
		t.Fatalf("paused state = %#v", state)
	}
}

func TestBrowserAutomationActionUsesUnifiedTargetResolver(t *testing.T) {
	rule := BrowserAutomationRule{
		ID: "automation-test", Action: BrowserAutomationAction{
			Type: "click", Target: BrowserAutomationSelector{Name: "Skip Ad", Role: "button"},
		},
	}
	state := map[string]any{
		"url": "https://video.example/watch",
		"key_elements": []any{
			map[string]any{"ref": "@e1", "label": "Other action", "role": "button"},
			map[string]any{"ref": "@e2", "label": "Skip Ad", "role": "button"},
		},
	}
	action, arguments, resolution, err := browserAutomationExecutableAction(rule, state, newBrowserTargetResolver())
	if err != nil {
		t.Fatal(err)
	}
	if action != "click" || arguments["ref"] != "@e2" {
		t.Fatalf("action=%q arguments=%#v", action, arguments)
	}
	if resolution == nil || resolution.Decision != browserResolutionExecute || resolution.Selected == nil || resolution.Selected.Ref != "@e2" {
		t.Fatalf("resolution = %#v", resolution)
	}
}

func TestBrowserAutomationMonitorErrorDoesNotOverwriteActionError(t *testing.T) {
	engine := newBrowserAutomationEngine(t.TempDir())
	created, err := engine.Manage(context.Background(), AutomationRequest{
		Operation: "create", SessionID: automationTestSession,
		Trigger: BrowserAutomationTrigger{Type: "video_ended"},
		Action:  BrowserAutomationAction{Type: "press", Value: "ArrowDown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	id := created["rule"].(BrowserAutomationRule).ID
	engine.setMonitorMode(automationTestSession, "lightweight_polling", errors.New("cdp disconnected"))
	rule, _ := engine.get(id)
	if rule.MonitorError != "cdp disconnected" || rule.LastError != "" || rule.Status != "watching" {
		t.Fatalf("rule = %#v", rule)
	}
}
