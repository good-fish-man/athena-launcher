package browser_runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	browserAutomationSchema       = "athena.browser.automation.v3"
	browserAutomationPollInterval = time.Second
	browserAutomationEventLimit   = 200
)

type BrowserAutomationSelector struct {
	Role        string `json:"role,omitempty"`
	Name        string `json:"name,omitempty"`
	Kind        string `json:"kind,omitempty"`
	URLContains string `json:"url_contains,omitempty"`
}

type BrowserAutomationTrigger struct {
	Type   string                    `json:"type"`
	Target BrowserAutomationSelector `json:"target,omitempty"`
}

type BrowserAutomationAction struct {
	Type   string                    `json:"type"`
	Target BrowserAutomationSelector `json:"target,omitempty"`
	Value  string                    `json:"value,omitempty"`
	URL    string                    `json:"url,omitempty"`
}

type BrowserAutomationVerification struct {
	Type   string                    `json:"type,omitempty"`
	Target BrowserAutomationSelector `json:"target,omitempty"`
}

type BrowserAutomationRule struct {
	Schema       string                        `json:"schema"`
	ID           string                        `json:"automation_id"`
	SessionID    string                        `json:"session_id"`
	TabID        string                        `json:"tab_id,omitempty"`
	Trigger      BrowserAutomationTrigger      `json:"trigger"`
	Action       BrowserAutomationAction       `json:"action"`
	Verification BrowserAutomationVerification `json:"verification,omitempty"`
	Enabled      bool                          `json:"enabled"`
	CooldownMS   int                           `json:"cooldown_ms"`
	Status       string                        `json:"status"`
	LastEvent    string                        `json:"last_event,omitempty"`
	LastError    string                        `json:"last_error,omitempty"`
	MonitorError string                        `json:"monitor_error,omitempty"`
	MonitorMode  string                        `json:"monitor_mode,omitempty"`
	LastObserved *time.Time                    `json:"last_observed_at,omitempty"`
	LastFiredAt  *time.Time                    `json:"last_fired_at,omitempty"`
	CreatedAt    time.Time                     `json:"created_at"`
	UpdatedAt    time.Time                     `json:"updated_at"`
}

type AutomationRequest struct {
	Operation    string                        `json:"operation"`
	SessionID    string                        `json:"session_id,omitempty"`
	AutomationID string                        `json:"automation_id,omitempty"`
	TabID        string                        `json:"tab_id,omitempty"`
	Trigger      BrowserAutomationTrigger      `json:"trigger,omitempty"`
	Action       BrowserAutomationAction       `json:"action,omitempty"`
	Verification BrowserAutomationVerification `json:"verification,omitempty"`
	CooldownMS   int                           `json:"cooldown_ms,omitempty"`
}

type browserAutomationStore struct {
	Schema string                   `json:"schema"`
	Rules  []BrowserAutomationRule  `json:"rules"`
	Events []browserAutomationEvent `json:"events,omitempty"`
}

type browserAutomationEvent struct {
	ID           string                               `json:"event_id"`
	Type         string                               `json:"type"`
	SessionID    string                               `json:"session_id"`
	AutomationID string                               `json:"automation_id,omitempty"`
	Status       string                               `json:"status"`
	URL          string                               `json:"url,omitempty"`
	Title        string                               `json:"title,omitempty"`
	Target       BrowserAutomationSelector            `json:"target,omitempty"`
	Resolution   *browserTargetResolution             `json:"target_resolution,omitempty"`
	Lifecycle    []browserAutomationTransition        `json:"lifecycle,omitempty"`
	Verification *browserAutomationVerificationResult `json:"verification,omitempty"`
	Error        string                               `json:"error,omitempty"`
	At           time.Time                            `json:"at"`
}

type browserAutomationTransition struct {
	State string    `json:"state"`
	At    time.Time `json:"at"`
}

type browserAutomationVerificationResult struct {
	Type     string `json:"type,omitempty"`
	Verified bool   `json:"verified"`
	Reason   string `json:"reason"`
}

type browserAutomationSnapshot struct {
	URL      string
	Title    string
	Elements map[string]BrowserAutomationSelector
	Playing  bool
	Paused   bool
	Ended    bool
	Blocked  bool
}

type browserAutomationEngine struct {
	home       string
	path       string
	controller *browserController

	mu        sync.Mutex
	rules     map[string]BrowserAutomationRule
	events    []browserAutomationEvent
	snapshots map[string]browserAutomationSnapshot
	watchers  map[string]context.CancelFunc
}

func newBrowserAutomationEngine(home string) *browserAutomationEngine {
	engine := &browserAutomationEngine{
		home: home, path: filepath.Join(home, "data", "browser-automation-v3.json"),
		rules: make(map[string]BrowserAutomationRule), snapshots: make(map[string]browserAutomationSnapshot),
		events: make([]browserAutomationEvent, 0, browserAutomationEventLimit), watchers: make(map[string]context.CancelFunc),
	}
	_ = engine.load()
	return engine
}

func (e *browserAutomationEngine) bind(controller *browserController) {
	e.mu.Lock()
	e.controller = controller
	sessions := make(map[string]bool)
	for _, rule := range e.rules {
		if rule.Enabled {
			sessions[rule.SessionID] = true
		}
	}
	e.mu.Unlock()
	for sessionID := range sessions {
		e.ensureWatcher(sessionID)
	}
}

func (e *browserAutomationEngine) Manage(ctx context.Context, request AutomationRequest) (map[string]any, error) {
	operation := strings.ToLower(strings.TrimSpace(request.Operation))
	switch operation {
	case "create":
		rule, err := e.create(request)
		if err != nil {
			return nil, err
		}
		return map[string]any{"schema": browserAutomationSchema, "operation": operation, "rule": rule}, nil
	case "list":
		return map[string]any{
			"schema": browserAutomationSchema, "operation": operation, "rules": e.list(request.SessionID),
			"events": e.recentEvents(request.SessionID, "", 50),
		}, nil
	case "get":
		rule, ok := e.get(request.AutomationID)
		if !ok {
			return nil, fmt.Errorf("browser automation %q was not found", request.AutomationID)
		}
		return map[string]any{
			"schema": browserAutomationSchema, "operation": operation, "rule": rule,
			"events": e.recentEvents(rule.SessionID, rule.ID, 50),
		}, nil
	case "pause", "resume":
		rule, err := e.setEnabled(request.AutomationID, operation == "resume")
		if err != nil {
			return nil, err
		}
		return map[string]any{"schema": browserAutomationSchema, "operation": operation, "rule": rule}, nil
	case "delete":
		if err := e.delete(request.AutomationID); err != nil {
			return nil, err
		}
		return map[string]any{"schema": browserAutomationSchema, "operation": operation, "automation_id": request.AutomationID, "deleted": true}, nil
	default:
		return nil, fmt.Errorf("browser automation operation must be create, list, get, pause, resume, or delete")
	}
}

func (e *browserAutomationEngine) create(request AutomationRequest) (BrowserAutomationRule, error) {
	if !browserSessionPattern.MatchString(strings.TrimSpace(request.SessionID)) {
		return BrowserAutomationRule{}, fmt.Errorf("browser automation requires a valid session_id")
	}
	trigger, action := normalizeBrowserAutomationTrigger(request.Trigger), normalizeBrowserAutomationAction(request.Action)
	verification := normalizeBrowserAutomationVerification(request.Verification)
	if err := validateBrowserAutomationRule(trigger, action, verification); err != nil {
		return BrowserAutomationRule{}, err
	}
	id, err := newBrowserAutomationID()
	if err != nil {
		return BrowserAutomationRule{}, err
	}
	now := time.Now().UTC()
	cooldown := request.CooldownMS
	if cooldown <= 0 {
		cooldown = 5000
	}
	if cooldown < 500 || cooldown > 300000 {
		return BrowserAutomationRule{}, fmt.Errorf("browser automation cooldown_ms must be between 500 and 300000")
	}
	rule := BrowserAutomationRule{
		Schema: browserAutomationSchema, ID: id, SessionID: request.SessionID, TabID: strings.TrimSpace(request.TabID),
		Trigger: trigger, Action: action, Verification: verification,
		Enabled: true, CooldownMS: cooldown, Status: "watching", CreatedAt: now, UpdatedAt: now,
	}
	e.mu.Lock()
	e.rules[id] = rule
	err = e.persistLocked()
	e.mu.Unlock()
	if err != nil {
		return BrowserAutomationRule{}, err
	}
	e.ensureWatcher(rule.SessionID)
	return rule, nil
}

func (e *browserAutomationEngine) list(sessionID string) []BrowserAutomationRule {
	e.mu.Lock()
	defer e.mu.Unlock()
	rules := make([]BrowserAutomationRule, 0, len(e.rules))
	for _, rule := range e.rules {
		if strings.TrimSpace(sessionID) == "" || rule.SessionID == sessionID {
			rules = append(rules, rule)
		}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].CreatedAt.Before(rules[j].CreatedAt) })
	return rules
}

func (e *browserAutomationEngine) get(id string) (BrowserAutomationRule, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rule, ok := e.rules[strings.TrimSpace(id)]
	return rule, ok
}

func (e *browserAutomationEngine) sessionState(sessionID string) map[string]any {
	sessionID = strings.TrimSpace(sessionID)
	e.mu.Lock()
	defer e.mu.Unlock()
	rules := make([]BrowserAutomationRule, 0)
	active := 0
	monitorModes := make(map[string]bool)
	for _, rule := range e.rules {
		if rule.SessionID != sessionID {
			continue
		}
		rules = append(rules, rule)
		if rule.Enabled {
			active++
		}
		if rule.MonitorMode != "" {
			monitorModes[rule.MonitorMode] = true
		}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].CreatedAt.Before(rules[j].CreatedAt) })
	events := make([]browserAutomationEvent, 0, 8)
	for index := len(e.events) - 1; index >= 0 && len(events) < 8; index-- {
		if e.events[index].SessionID == sessionID {
			events = append(events, e.events[index])
		}
	}
	modes := make([]string, 0, len(monitorModes))
	for mode := range monitorModes {
		modes = append(modes, mode)
	}
	sort.Strings(modes)
	mode := "idle"
	if active > 0 {
		mode = "watch"
	}
	return map[string]any{
		"schema": browserAutomationSchema, "mode": mode, "active_count": active,
		"rule_count": len(rules), "monitor_modes": modes, "rules": rules, "recent_events": events,
	}
}

func (e *browserAutomationEngine) setEnabled(id string, enabled bool) (BrowserAutomationRule, error) {
	id = strings.TrimSpace(id)
	e.mu.Lock()
	rule, ok := e.rules[id]
	if !ok {
		e.mu.Unlock()
		return BrowserAutomationRule{}, fmt.Errorf("browser automation %q was not found", id)
	}
	rule.Enabled, rule.UpdatedAt = enabled, time.Now().UTC()
	if enabled {
		rule.Status = "watching"
	} else {
		rule.Status = "paused"
	}
	e.rules[id] = rule
	err := e.persistLocked()
	e.mu.Unlock()
	if err != nil {
		return BrowserAutomationRule{}, err
	}
	if enabled {
		e.ensureWatcher(rule.SessionID)
	} else {
		e.stopWatcherIfIdle(rule.SessionID)
	}
	return rule, nil
}

func (e *browserAutomationEngine) delete(id string) error {
	id = strings.TrimSpace(id)
	e.mu.Lock()
	rule, ok := e.rules[id]
	if !ok {
		e.mu.Unlock()
		return fmt.Errorf("browser automation %q was not found", id)
	}
	delete(e.rules, id)
	err := e.persistLocked()
	e.mu.Unlock()
	if err == nil {
		e.stopWatcherIfIdle(rule.SessionID)
	}
	return err
}

func (e *browserAutomationEngine) ensureWatcher(sessionID string) {
	e.mu.Lock()
	if e.controller == nil || e.watchers[sessionID] != nil || !e.hasEnabledRulesLocked(sessionID) {
		e.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.watchers[sessionID] = cancel
	controller := e.controller
	e.mu.Unlock()
	go e.watch(ctx, controller, sessionID)
}

func (e *browserAutomationEngine) watch(ctx context.Context, controller *browserController, sessionID string) {
	defer func() {
		e.mu.Lock()
		delete(e.watchers, sessionID)
		e.mu.Unlock()
	}()
	for ctx.Err() == nil && e.hasEnabledRules(sessionID) {
		e.setMonitorMode(sessionID, "cdp_events", nil)
		if err := e.watchCDPEvents(ctx, controller, sessionID); err == nil || ctx.Err() != nil {
			return
		} else {
			e.setMonitorMode(sessionID, "lightweight_polling", err)
		}
		if !e.watchPollingWindow(ctx, controller, sessionID, 30*time.Second) {
			return
		}
	}
}

func (e *browserAutomationEngine) watchPollingWindow(
	ctx context.Context,
	controller *browserController,
	sessionID string,
	duration time.Duration,
) bool {
	ticker := time.NewTicker(browserAutomationPollInterval)
	defer ticker.Stop()
	deadline := time.NewTimer(duration)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return e.hasEnabledRules(sessionID)
		case <-ticker.C:
			if !e.hasEnabledRules(sessionID) {
				return false
			}
			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			probe, err := controller.probeAutomationState(probeCtx, sessionID)
			cancel()
			if err != nil {
				e.recordSessionError(sessionID, err)
				continue
			}
			e.evaluateProbe(ctx, controller, sessionID, probe)
		}
	}
}

func (e *browserAutomationEngine) evaluateProbe(ctx context.Context, controller *browserController, sessionID string, current browserAutomationSnapshot) {
	e.markObserved(sessionID)
	e.mu.Lock()
	previous, hadPrevious := e.snapshots[sessionID]
	e.snapshots[sessionID] = current
	rules := e.enabledRulesLocked(sessionID)
	e.mu.Unlock()
	events := browserAutomationEvents(sessionID, previous, current, hadPrevious)
	e.recordEvents(events)
	matched := make(map[string]browserAutomationEvent)
	for _, event := range events {
		for _, rule := range rules {
			if !browserAutomationRuleMatches(rule, event, current) || !browserAutomationRuleReady(rule, time.Now().UTC()) {
				continue
			}
			if _, exists := matched[rule.ID]; !exists {
				matched[rule.ID] = event
			}
		}
	}
	if len(matched) == 0 {
		return
	}
	observeCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	state, err := controller.runAction(observeCtx, browserExecuteRequest{
		RequestID: "automation-observe-" + sessionID, SessionID: sessionID, Action: "extract",
		Arguments: map[string]any{"snapshot": true, "automation_event": true},
		RiskLevel: "LOW", Decision: "ALLOW", Approved: true,
	})
	cancel()
	if err != nil {
		e.recordSessionError(sessionID, err)
		return
	}
	for _, rule := range rules {
		event, ok := matched[rule.ID]
		if ok {
			e.executeRule(ctx, controller, rule, event, state)
		}
	}
}

func (e *browserAutomationEngine) setMonitorMode(sessionID, mode string, monitorErr error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, rule := range e.rules {
		if rule.SessionID != sessionID || !rule.Enabled {
			continue
		}
		rule.MonitorMode, rule.UpdatedAt = mode, time.Now().UTC()
		if monitorErr != nil {
			rule.MonitorError = monitorErr.Error()
		} else {
			rule.MonitorError = ""
		}
		e.rules[id] = rule
	}
	_ = e.persistLocked()
}

func (e *browserAutomationEngine) markObserved(sessionID string) {
	now := time.Now().UTC()
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, rule := range e.rules {
		if rule.SessionID == sessionID && rule.Enabled {
			rule.LastObserved, rule.UpdatedAt = &now, now
			e.rules[id] = rule
		}
	}
}

func (e *browserAutomationEngine) executeRule(ctx context.Context, controller *browserController, rule BrowserAutomationRule, event browserAutomationEvent, state map[string]any) {
	event.Lifecycle = append(event.Lifecycle, browserAutomationTransition{State: "triggered", At: time.Now().UTC()})
	e.setRuleRuntimeStatus(rule.ID, "triggered")
	action, arguments, resolution, err := browserAutomationExecutableAction(rule, state, controller.targetResolver)
	event.Resolution = resolution
	if err == nil {
		policy := browserTaskActionPolicy(action, arguments)
		if policy.Decision != "ALLOW" || policy.Risk != "LOW" {
			err = fmt.Errorf("automation action requires user confirmation: %s", policy.Reason)
		}
	}
	var actionState map[string]any
	verification := browserAutomationVerificationResult{Type: rule.Verification.Type, Reason: "action_not_executed"}
	if err == nil {
		e.setRuleRuntimeStatus(rule.ID, "executing")
		event.Lifecycle = append(event.Lifecycle, browserAutomationTransition{State: "executing", At: time.Now().UTC()})
		arguments["automation_verification_type"] = rule.Verification.Type
		arguments["automation_verification_name"] = rule.Verification.Target.Name
		arguments["automation_verification_role"] = rule.Verification.Target.Role
		arguments["automation_verification_kind"] = rule.Verification.Target.Kind
		arguments["automation_verification_url_contains"] = rule.Verification.Target.URLContains
		actionCtx, cancel := context.WithTimeout(ctx, 35*time.Second)
		actionState, err = controller.runTaskAction(actionCtx, browserTaskRequest{
			RequestID: rule.ID + "-" + fmt.Sprint(time.Now().UnixMilli()), SessionID: rule.SessionID,
		}, action, arguments, "Running browser automation", 90)
		cancel()
	}
	if err == nil {
		e.setRuleRuntimeStatus(rule.ID, "verifying")
		event.Lifecycle = append(event.Lifecycle, browserAutomationTransition{State: "verifying", At: time.Now().UTC()})
		verification = browserAutomationVerify(rule.Verification, state, actionState)
		if !verification.Verified {
			err = fmt.Errorf("browser automation verification failed: %s", verification.Reason)
		}
	} else {
		verification.Reason = err.Error()
	}
	now := time.Now().UTC()
	e.mu.Lock()
	current, ok := e.rules[rule.ID]
	if ok {
		current.LastEvent, current.LastFiredAt, current.UpdatedAt = event.Type, &now, now
		if err != nil {
			current.Status, current.LastError = "error", err.Error()
			event.Lifecycle = append(event.Lifecycle, browserAutomationTransition{State: "failed", At: now})
		} else {
			current.Status, current.LastError = "watching", ""
			event.Lifecycle = append(event.Lifecycle, browserAutomationTransition{State: "active", At: now})
		}
		e.rules[rule.ID] = current
		_ = e.persistLocked()
	}
	e.mu.Unlock()
	e.recordAutomationOutcome(event, rule.ID, verification, err)
}

func (e *browserAutomationEngine) setRuleRuntimeStatus(id, status string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rule, ok := e.rules[id]
	if !ok {
		return
	}
	rule.Status, rule.UpdatedAt = status, time.Now().UTC()
	e.rules[id] = rule
}

func browserAutomationExecutableAction(
	rule BrowserAutomationRule,
	state map[string]any,
	resolver *browserTargetResolver,
) (string, map[string]any, *browserTargetResolution, error) {
	action := strings.ToLower(strings.TrimSpace(rule.Action.Type))
	arguments := map[string]any{"snapshot": true, "automation_id": rule.ID}
	switch action {
	case "click", "play":
		if resolver == nil {
			resolver = newBrowserTargetResolver()
		}
		task := inferredBrowserTask{Query: rule.Action.Target.Name}
		if rule.Action.Target.Role != "" {
			task.PreferredRoles = []string{rule.Action.Target.Role}
		}
		if rule.Action.Target.Kind != "" {
			task.PreferredKinds = []string{rule.Action.Target.Kind}
		}
		resolution := resolver.Resolve(state, task, 1, "LOW")
		if resolution.Decision != browserResolutionExecute || resolution.Selected == nil {
			return "", nil, &resolution, fmt.Errorf("automation target resolution failed: %s", resolution.Reason)
		}
		selected := resolution.Selected
		actual := BrowserAutomationSelector{Name: selected.Label, URLContains: selected.URL, Role: selected.Role, Kind: selected.Kind}
		if !browserAutomationSelectorMatches(rule.Action.Target, actual, browserStringValue(state["url"])) {
			return "", nil, &resolution, fmt.Errorf("automation target resolution did not satisfy the rule selector")
		}
		arguments["ref"], arguments["target_label"] = selected.Ref, selected.Label
		arguments["target_resolution"] = resolution
		arguments["expected_page_url"] = browserStringValue(state["url"])
		if targetURL := resolveBrowserElementURL(browserStringValue(state["url"]), selected.URL); targetURL != "" {
			arguments["target_url"] = targetURL
		}
		return action, arguments, &resolution, nil
	case "press", "scroll":
		arguments["value"] = rule.Action.Value
	case "navigate":
		arguments["url"] = rule.Action.URL
	default:
		return "", nil, nil, fmt.Errorf("automation action %q is not supported", action)
	}
	return action, arguments, nil, nil
}

func browserAutomationEvents(sessionID string, previous, current browserAutomationSnapshot, hadPrevious bool) []browserAutomationEvent {
	now := time.Now().UTC()
	events := make([]browserAutomationEvent, 0, 8)
	appendEvent := func(kind string, target BrowserAutomationSelector) {
		events = append(events, browserAutomationEvent{Type: kind, SessionID: sessionID, URL: current.URL, Title: current.Title, Target: target, At: now})
	}
	if !hadPrevious || (current.URL != "" && current.URL != previous.URL) {
		appendEvent("page_loaded", BrowserAutomationSelector{URLContains: current.URL})
	}
	if hadPrevious && (current.URL != previous.URL || current.Title != previous.Title) {
		appendEvent("page_changed", BrowserAutomationSelector{URLContains: current.URL})
	}
	for key, target := range current.Elements {
		if _, exists := previous.Elements[key]; !exists {
			appendEvent("element_appeared", target)
		}
	}
	if hadPrevious {
		for key, target := range previous.Elements {
			if _, exists := current.Elements[key]; !exists {
				appendEvent("element_disappeared", target)
			}
		}
	}
	if current.Playing && (!hadPrevious || !previous.Playing) {
		appendEvent("video_started", BrowserAutomationSelector{Kind: "media"})
	}
	if hadPrevious && current.Paused && !previous.Paused {
		appendEvent("video_paused", BrowserAutomationSelector{Kind: "media"})
	}
	if hadPrevious && current.Ended && !previous.Ended {
		appendEvent("video_ended", BrowserAutomationSelector{Kind: "media"})
	}
	if current.Blocked && (!hadPrevious || !previous.Blocked) {
		appendEvent("login_required", BrowserAutomationSelector{})
	}
	return events
}

func browserAutomationSnapshotFromState(state map[string]any) browserAutomationSnapshot {
	snapshot := browserAutomationSnapshot{
		URL: browserStringValue(state["url"]), Title: browserStringValue(state["title"]),
		Elements: make(map[string]BrowserAutomationSelector), Blocked: browserInterventionRequired(state),
	}
	for _, element := range browserSemanticElements(state) {
		label := normalizeCurrentPageSelection(browserElementTitle(element))
		if label == "" {
			continue
		}
		key := element.Ref + "|" + label
		snapshot.Elements[key] = BrowserAutomationSelector{
			Name: label, URLContains: element.URL, Role: element.Role, Kind: element.Kind,
		}
	}
	playback, _ := state["playback"].(map[string]any)
	snapshot.Playing, _ = playback["playing"].(bool)
	snapshot.Paused, _ = playback["paused"].(bool)
	snapshot.Ended, _ = playback["ended"].(bool)
	return snapshot
}

func browserAutomationRuleMatches(rule BrowserAutomationRule, event browserAutomationEvent, snapshot browserAutomationSnapshot) bool {
	if strings.ToLower(rule.Trigger.Type) != strings.ToLower(event.Type) {
		return false
	}
	if browserAutomationSelectorEmpty(rule.Trigger.Target) {
		return true
	}
	if browserAutomationSelectorMatches(rule.Trigger.Target, event.Target, event.URL) {
		return true
	}
	for _, candidate := range snapshot.Elements {
		if browserAutomationSelectorMatches(rule.Trigger.Target, candidate, snapshot.URL) {
			return true
		}
	}
	return false
}

func browserAutomationElement(state map[string]any, selector BrowserAutomationSelector) (browserSemanticElement, bool) {
	for _, element := range browserSemanticElements(state) {
		candidate := BrowserAutomationSelector{
			Name: browserElementTitle(element), URLContains: element.URL, Role: element.Role, Kind: element.Kind,
		}
		if browserAutomationSelectorMatches(selector, candidate, browserStringValue(state["url"])) {
			return element, true
		}
	}
	return browserSemanticElement{}, false
}

func browserAutomationSelectorMatches(expected, actual BrowserAutomationSelector, pageURL string) bool {
	if expected.Name != "" {
		wanted, got := normalizeCurrentPageSelection(expected.Name), normalizeCurrentPageSelection(actual.Name)
		if wanted == "" || got == "" || currentPageSelectionScore(wanted, got) < 70 {
			return false
		}
	}
	if expected.Role != "" && !strings.Contains(strings.ToLower(actual.Role+" "+actual.Name), strings.ToLower(expected.Role)) {
		return false
	}
	if expected.Kind != "" && !strings.EqualFold(expected.Kind, actual.Kind) && !strings.Contains(strings.ToLower(actual.Name), strings.ToLower(expected.Kind)) {
		return false
	}
	if expected.URLContains != "" {
		value := actual.URLContains
		if value == "" {
			value = pageURL
		}
		if !strings.Contains(strings.ToLower(value), strings.ToLower(expected.URLContains)) {
			return false
		}
	}
	return true
}

func normalizeBrowserAutomationTrigger(value BrowserAutomationTrigger) BrowserAutomationTrigger {
	value.Type = strings.ToLower(strings.TrimSpace(value.Type))
	value.Target = normalizeBrowserAutomationSelector(value.Target)
	return value
}

func normalizeBrowserAutomationAction(value BrowserAutomationAction) BrowserAutomationAction {
	value.Type = strings.ToLower(strings.TrimSpace(value.Type))
	value.Value, value.URL = strings.TrimSpace(value.Value), strings.TrimSpace(value.URL)
	value.Target = normalizeBrowserAutomationSelector(value.Target)
	return value
}

func normalizeBrowserAutomationVerification(value BrowserAutomationVerification) BrowserAutomationVerification {
	value.Type = strings.ToLower(strings.TrimSpace(value.Type))
	value.Target = normalizeBrowserAutomationSelector(value.Target)
	return value
}

func normalizeBrowserAutomationSelector(value BrowserAutomationSelector) BrowserAutomationSelector {
	value.Role = strings.ToLower(strings.TrimSpace(value.Role))
	value.Kind = strings.ToLower(strings.TrimSpace(value.Kind))
	value.Name = strings.TrimSpace(value.Name)
	value.URLContains = strings.TrimSpace(value.URLContains)
	return value
}

func validateBrowserAutomationRule(trigger BrowserAutomationTrigger, action BrowserAutomationAction, verification BrowserAutomationVerification) error {
	allowedTriggers := map[string]bool{
		"page_loaded": true, "page_changed": true, "element_appeared": true, "element_disappeared": true,
		"video_started": true, "video_paused": true, "video_ended": true, "login_required": true,
	}
	if !allowedTriggers[trigger.Type] {
		return fmt.Errorf("unsupported browser automation trigger %q", trigger.Type)
	}
	allowedActions := map[string]bool{"click": true, "play": true, "press": true, "scroll": true, "navigate": true}
	if !allowedActions[action.Type] {
		return fmt.Errorf("unsupported browser automation action %q", action.Type)
	}
	if (action.Type == "click" || action.Type == "play") && browserAutomationSelectorEmpty(action.Target) {
		return fmt.Errorf("browser automation %s requires a semantic target", action.Type)
	}
	if action.Type == "navigate" {
		if _, err := validateBrowserTarget(action.URL); err != nil {
			return fmt.Errorf("browser automation navigate target: %w", err)
		}
	}
	if action.Type == "press" && !allowedAutomationKey(action.Value) {
		return fmt.Errorf("browser automation press value is not allowed")
	}
	if action.Type == "scroll" && action.Value != "up" && action.Value != "down" {
		return fmt.Errorf("browser automation scroll value must be up or down")
	}
	if err := validateBrowserAutomationVerification(action, verification); err != nil {
		return err
	}
	policy := browserTaskActionPolicy(action.Type, map[string]any{
		"target_label": action.Target.Name, "purpose": trigger.Type,
	})
	if policy.Decision != "ALLOW" || policy.Risk != "LOW" {
		return fmt.Errorf("browser automation action is not safe for unattended execution: %s", policy.Reason)
	}
	return nil
}

func validateBrowserAutomationVerification(action BrowserAutomationAction, verification BrowserAutomationVerification) error {
	allowed := map[string]bool{
		"": true, "element_appeared": true, "element_disappeared": true, "page_changed": true,
		"url_contains": true, "video_started": true, "media_playing": true,
	}
	if !allowed[verification.Type] {
		return fmt.Errorf("unsupported browser automation verification %q", verification.Type)
	}
	if (action.Type == "click" || action.Type == "play" || action.Type == "navigate") && verification.Type == "" {
		return fmt.Errorf("browser automation %s requires an explicit verification", action.Type)
	}
	if (verification.Type == "element_appeared" || verification.Type == "element_disappeared") && browserAutomationSelectorEmpty(verification.Target) {
		return fmt.Errorf("browser automation %s verification requires a semantic target", verification.Type)
	}
	if verification.Type == "url_contains" && verification.Target.URLContains == "" {
		return fmt.Errorf("browser automation url_contains verification requires target.url_contains")
	}
	return nil
}

func browserAutomationVerify(
	verification BrowserAutomationVerification,
	before map[string]any,
	after map[string]any,
) browserAutomationVerificationResult {
	result := browserAutomationVerificationResult{Type: verification.Type}
	if after == nil {
		result.Reason = "browser_state_not_available"
		return result
	}
	switch verification.Type {
	case "":
		result.Verified, result.Reason = true, "interaction_engine_verified_action"
	case "element_appeared":
		_, wasVisible := browserAutomationElement(before, verification.Target)
		_, isVisible := browserAutomationElement(after, verification.Target)
		result.Verified = !wasVisible && isVisible
		result.Reason = browserVerificationReason(result.Verified, "target_element_appeared", "target_element_not_observed")
	case "element_disappeared":
		_, wasVisible := browserAutomationElement(before, verification.Target)
		_, isVisible := browserAutomationElement(after, verification.Target)
		result.Verified = wasVisible && !isVisible
		result.Reason = browserVerificationReason(result.Verified, "target_element_disappeared", "target_element_still_visible")
	case "page_changed":
		beforeURL, afterURL := browserStringValue(before["url"]), browserStringValue(after["url"])
		beforeTitle, afterTitle := browserStringValue(before["title"]), browserStringValue(after["title"])
		result.Verified = (afterURL != "" && afterURL != beforeURL) || (afterTitle != "" && afterTitle != beforeTitle)
		result.Reason = browserVerificationReason(result.Verified, "page_changed", "page_did_not_change")
	case "url_contains":
		result.Verified = strings.Contains(
			strings.ToLower(browserStringValue(after["url"])),
			strings.ToLower(verification.Target.URLContains),
		)
		result.Reason = browserVerificationReason(result.Verified, "url_matches_expected_value", "url_does_not_match_expected_value")
	case "video_started", "media_playing":
		playback, _ := after["playback"].(map[string]any)
		playing, _ := playback["playing"].(bool)
		verified, _ := playback["verified"].(bool)
		progressed, _ := playback["progressed"].(bool)
		result.Verified = playing && (verified || progressed)
		result.Reason = browserVerificationReason(result.Verified, "media_playback_verified", "media_playback_not_verified")
	default:
		result.Reason = "unsupported_verification"
	}
	return result
}

func browserVerificationReason(verified bool, success, failure string) string {
	if verified {
		return success
	}
	return failure
}

func browserAutomationVerificationFromArguments(arguments map[string]any) BrowserAutomationVerification {
	if arguments == nil {
		return BrowserAutomationVerification{}
	}
	return BrowserAutomationVerification{
		Type: strings.ToLower(browserStringValue(arguments["automation_verification_type"])),
		Target: BrowserAutomationSelector{
			Name:        browserStringValue(arguments["automation_verification_name"]),
			Role:        strings.ToLower(browserStringValue(arguments["automation_verification_role"])),
			Kind:        strings.ToLower(browserStringValue(arguments["automation_verification_kind"])),
			URLContains: browserStringValue(arguments["automation_verification_url_contains"]),
		},
	}
}

func (e *browserAutomationEngine) recordEvents(events []browserAutomationEvent) {
	if len(events) == 0 {
		return
	}
	e.mu.Lock()
	for index := range events {
		if events[index].ID == "" {
			events[index].ID = browserAutomationEventID(events[index].At, index)
		}
		if events[index].Status == "" {
			events[index].Status = "observed"
		}
		e.appendEventLocked(events[index])
	}
	_ = e.persistLocked()
	e.mu.Unlock()
}

func (e *browserAutomationEngine) recordAutomationOutcome(
	event browserAutomationEvent,
	automationID string,
	verification browserAutomationVerificationResult,
	err error,
) {
	event.ID = browserAutomationEventID(time.Now().UTC(), 0)
	event.AutomationID = automationID
	event.At = time.Now().UTC()
	event.Verification = &verification
	if err != nil {
		event.Status, event.Error = "failed", err.Error()
	} else {
		event.Status = "succeeded"
	}
	e.mu.Lock()
	e.appendEventLocked(event)
	_ = e.persistLocked()
	e.mu.Unlock()
}

func (e *browserAutomationEngine) appendEventLocked(event browserAutomationEvent) {
	e.events = append(e.events, event)
	if overflow := len(e.events) - browserAutomationEventLimit; overflow > 0 {
		copy(e.events, e.events[overflow:])
		e.events = e.events[:browserAutomationEventLimit]
	}
}

func (e *browserAutomationEngine) recentEvents(sessionID, automationID string, limit int) []browserAutomationEvent {
	if limit <= 0 || limit > browserAutomationEventLimit {
		limit = 50
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	result := make([]browserAutomationEvent, 0, min(limit, len(e.events)))
	for index := len(e.events) - 1; index >= 0 && len(result) < limit; index-- {
		event := e.events[index]
		if sessionID != "" && event.SessionID != sessionID {
			continue
		}
		if automationID != "" && event.AutomationID != automationID {
			continue
		}
		result = append(result, event)
	}
	return result
}

func browserAutomationEventID(at time.Time, sequence int) string {
	return fmt.Sprintf("browser-event-%d-%d", at.UnixNano(), sequence)
}

func allowedAutomationKey(value string) bool {
	switch value {
	case "Escape", "ArrowUp", "ArrowDown", "PageUp", "PageDown":
		return true
	}
	return false
}

func browserAutomationSelectorEmpty(value BrowserAutomationSelector) bool {
	return value.Role == "" && value.Name == "" && value.Kind == "" && value.URLContains == ""
}

func browserAutomationRuleReady(rule BrowserAutomationRule, now time.Time) bool {
	if !rule.Enabled || rule.LastFiredAt == nil {
		return rule.Enabled
	}
	return now.Sub(*rule.LastFiredAt) >= time.Duration(rule.CooldownMS)*time.Millisecond
}

func (e *browserAutomationEngine) hasEnabledRules(sessionID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.hasEnabledRulesLocked(sessionID)
}

func (e *browserAutomationEngine) hasEnabledRulesLocked(sessionID string) bool {
	for _, rule := range e.rules {
		if rule.SessionID == sessionID && rule.Enabled {
			return true
		}
	}
	return false
}

func (e *browserAutomationEngine) enabledRulesLocked(sessionID string) []BrowserAutomationRule {
	rules := make([]BrowserAutomationRule, 0)
	for _, rule := range e.rules {
		if rule.SessionID == sessionID && rule.Enabled {
			rules = append(rules, rule)
		}
	}
	return rules
}

func (e *browserAutomationEngine) stopWatcherIfIdle(sessionID string) {
	e.mu.Lock()
	if e.hasEnabledRulesLocked(sessionID) {
		e.mu.Unlock()
		return
	}
	cancel := e.watchers[sessionID]
	delete(e.snapshots, sessionID)
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *browserAutomationEngine) CloseSession(sessionID string) {
	e.mu.Lock()
	cancel := e.watchers[sessionID]
	delete(e.snapshots, sessionID)
	for id, rule := range e.rules {
		if rule.SessionID == sessionID && rule.Enabled {
			rule.Enabled, rule.Status, rule.UpdatedAt = false, "session_closed", time.Now().UTC()
			e.rules[id] = rule
		}
	}
	_ = e.persistLocked()
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (e *browserAutomationEngine) recordSessionError(sessionID string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, rule := range e.rules {
		if rule.SessionID == sessionID && rule.Enabled {
			rule.MonitorError, rule.UpdatedAt = err.Error(), time.Now().UTC()
			e.rules[id] = rule
		}
	}
	_ = e.persistLocked()
}

func (e *browserAutomationEngine) load() error {
	data, err := os.ReadFile(e.path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var store browserAutomationStore
	if err := json.Unmarshal(data, &store); err != nil {
		return err
	}
	for _, rule := range store.Rules {
		if rule.Schema == browserAutomationSchema && rule.ID != "" {
			e.rules[rule.ID] = rule
		}
	}
	if len(store.Events) > browserAutomationEventLimit {
		store.Events = store.Events[len(store.Events)-browserAutomationEventLimit:]
	}
	e.events = append(e.events[:0], store.Events...)
	return nil
}

func (e *browserAutomationEngine) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(e.path), 0o700); err != nil {
		return err
	}
	rules := make([]BrowserAutomationRule, 0, len(e.rules))
	for _, rule := range e.rules {
		rules = append(rules, rule)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].CreatedAt.Before(rules[j].CreatedAt) })
	data, err := json.MarshalIndent(browserAutomationStore{
		Schema: browserAutomationSchema, Rules: rules, Events: append([]browserAutomationEvent(nil), e.events...),
	}, "", "  ")
	if err != nil {
		return err
	}
	temporary := e.path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, e.path)
}

func newBrowserAutomationID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create browser automation id: %w", err)
	}
	return "automation-" + hex.EncodeToString(value), nil
}
