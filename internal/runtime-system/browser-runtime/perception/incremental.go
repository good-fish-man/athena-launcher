package perception

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"time"
)

const maxPerceptionSessionBaselines = 128

type IncrementalObservation struct {
	Sequence            int64     `json:"sequence"`
	HasPrevious         bool      `json:"has_previous"`
	BaselineCreated     bool      `json:"baseline_created"`
	Changed             bool      `json:"changed"`
	URLChanged          bool      `json:"url_changed"`
	TitleChanged        bool      `json:"title_changed"`
	ContentChanged      bool      `json:"content_changed"`
	ElementsChanged     bool      `json:"elements_changed"`
	AddedRefs           []string  `json:"added_refs,omitempty"`
	RemovedRefs         []string  `json:"removed_refs,omitempty"`
	ChangedRefs         []string  `json:"changed_refs,omitempty"`
	PreviousObservedAt  time.Time `json:"previous_observed_at,omitempty"`
	CurrentObservedAt   time.Time `json:"current_observed_at"`
	Stability           string    `json:"stability"`
	PreviousFingerprint string    `json:"previous_fingerprint,omitempty"`
	CurrentFingerprint  string    `json:"current_fingerprint"`
}

type ActionVerification struct {
	Action          string   `json:"action"`
	Status          string   `json:"status"`
	Confidence      float64  `json:"confidence"`
	Reason          string   `json:"reason"`
	Evidence        []string `json:"evidence,omitempty"`
	RecommendedNext string   `json:"recommended_next"`
	RetryAfterMS    int      `json:"retry_after_ms,omitempty"`
}

type observationFingerprint struct {
	URL         string
	Title       string
	ContentHash string
	ElementHash string
	Elements    map[string]string
}

type sessionBaseline struct {
	Sequence             int64
	ObservedAt           time.Time
	State                observationFingerprint
	ConsecutiveUncertain int
	RepeatedAction       int
	LastActionSignature  string
}

func (o *Orchestrator) observeIncremental(request Request, raw map[string]any, semantic semanticResult, observedAt time.Time) IncrementalObservation {
	current := fingerprintObservation(raw, semantic)
	sessionKey := perceptionSessionKey(request)
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sessions == nil {
		o.sessions = make(map[string]*sessionBaseline)
	}
	previous := o.sessions[sessionKey]
	if previous == nil && len(o.sessions) >= maxPerceptionSessionBaselines {
		o.pruneOldestBaselineLocked()
	}
	delta := IncrementalObservation{
		Sequence: 1, BaselineCreated: previous == nil, CurrentObservedAt: observedAt,
		CurrentFingerprint: current.compactHash(), Stability: "baseline",
	}
	if previous != nil {
		delta.Sequence = previous.Sequence + 1
		delta.HasPrevious = true
		delta.BaselineCreated = false
		delta.PreviousObservedAt = previous.ObservedAt
		delta.PreviousFingerprint = previous.State.compactHash()
		delta.URLChanged = previous.State.URL != current.URL
		delta.TitleChanged = previous.State.Title != current.Title
		delta.ContentChanged = previous.State.ContentHash != current.ContentHash
		delta.AddedRefs, delta.RemovedRefs, delta.ChangedRefs = compareElementMaps(previous.State.Elements, current.Elements)
		delta.ElementsChanged = len(delta.AddedRefs)+len(delta.RemovedRefs)+len(delta.ChangedRefs) > 0
		delta.Changed = delta.URLChanged || delta.TitleChanged || delta.ContentChanged || delta.ElementsChanged
		if delta.Changed {
			delta.Stability = "changed"
		} else {
			delta.Stability = "stable"
		}
	}
	next := &sessionBaseline{Sequence: delta.Sequence, ObservedAt: observedAt, State: current}
	if previous != nil {
		next.ConsecutiveUncertain = previous.ConsecutiveUncertain
		next.RepeatedAction = previous.RepeatedAction
		next.LastActionSignature = previous.LastActionSignature
	}
	o.sessions[sessionKey] = next
	return delta
}

func (o *Orchestrator) recoveryPlan(request Request, verification ActionVerification) RecoveryPlan {
	plan := RecoveryPlan{Strategy: "continue", Reason: "action_observed"}
	key := perceptionSessionKey(request)
	signature := actionSignature(request)
	o.mu.Lock()
	defer o.mu.Unlock()
	baseline := o.sessions[key]
	if baseline == nil {
		return plan
	}
	if signature != "" && signature == baseline.LastActionSignature {
		baseline.RepeatedAction++
	} else {
		baseline.RepeatedAction = 1
		baseline.LastActionSignature = signature
	}
	plan.RepeatedAction = baseline.RepeatedAction
	switch verification.Status {
	case "verified", "observed":
		baseline.ConsecutiveUncertain = 0
		baseline.RepeatedAction = 0
		baseline.LastActionSignature = ""
		plan.RepeatedAction = 0
		plan.Strategy = "continue"
		plan.Reason = verification.Reason
	case "blocked":
		baseline.ConsecutiveUncertain++
		plan.Attempt = baseline.ConsecutiveUncertain
		plan.Strategy = "user_takeover"
		plan.Reason = verification.Reason
		plan.CircuitOpen = true
		plan.RecommendedActions = []string{"request_user_takeover", "observe_same_session_after_user_confirmation"}
	case "failed":
		baseline.ConsecutiveUncertain++
		plan.Attempt = baseline.ConsecutiveUncertain
		plan.Strategy = "revise_action"
		plan.Reason = verification.Reason
		plan.RecommendedActions = []string{"refresh_semantic_snapshot", "choose_alternative_target"}
	default:
		baseline.ConsecutiveUncertain++
		plan.Attempt = baseline.ConsecutiveUncertain
		plan.Reason = verification.Reason
		switch baseline.ConsecutiveUncertain {
		case 1:
			plan.Strategy = "settle_and_observe"
			plan.BackoffMS = max(verification.RetryAfterMS, 500)
			plan.RecommendedActions = []string{"observe_same_session"}
		case 2:
			plan.Strategy = "capture_annotated_viewport"
			plan.BackoffMS = 750
			plan.RecommendedActions = []string{"capture_annotated_viewport", "refresh_semantic_snapshot"}
		case 3:
			plan.Strategy = "alternate_target"
			plan.BackoffMS = 1000
			plan.RecommendedActions = []string{"refresh_semantic_snapshot", "choose_alternative_target"}
		default:
			plan.Strategy = "request_user_takeover"
			plan.CircuitOpen = true
			plan.RecommendedActions = []string{"request_user_takeover"}
		}
	}
	if baseline.RepeatedAction >= 3 && (verification.Status == "uncertain" || verification.Status == "failed") {
		plan.CircuitOpen = true
		plan.Strategy = "stop_repeating_action"
		plan.Reason = "same_action_repeated_without_verified_progress"
		plan.RecommendedActions = []string{"refresh_semantic_snapshot", "choose_alternative_target", "request_user_takeover"}
	}
	return plan
}

func actionSignature(request Request) string {
	action := strings.ToLower(strings.TrimSpace(request.Action))
	if action == "" {
		return ""
	}
	parts := []string{action}
	for _, key := range []string{"ref", "url", "value", "query", "target"} {
		if value := strings.TrimSpace(stringValue(request.Arguments[key])); value != "" {
			parts = append(parts, key+"="+shortHash(value))
		}
	}
	return strings.Join(parts, "|")
}

func (o *Orchestrator) ClearSession(sessionID string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.sessions, strings.TrimSpace(sessionID))
}

func (o *Orchestrator) pruneOldestBaselineLocked() {
	oldestKey := ""
	var oldestTime time.Time
	for key, baseline := range o.sessions {
		if baseline == nil || oldestKey == "" || baseline.ObservedAt.Before(oldestTime) {
			oldestKey = key
			if baseline != nil {
				oldestTime = baseline.ObservedAt
			}
		}
	}
	if oldestKey != "" {
		delete(o.sessions, oldestKey)
	}
}

func perceptionSessionKey(request Request) string {
	if sessionID := strings.TrimSpace(request.SessionID); sessionID != "" {
		return sessionID
	}
	return "default"
}

func fingerprintObservation(raw map[string]any, semantic semanticResult) observationFingerprint {
	observedElements := parseElements(raw["key_elements"], raw["element_boxes"])
	elements := make(map[string]string, len(observedElements))
	for _, element := range observedElements {
		elements[element.Ref] = element.Role + "\x00" + element.Name
	}
	return observationFingerprint{
		URL:         stringValue(raw["url"]),
		Title:       stringValue(raw["title"]),
		ContentHash: shortHash(semantic.Content),
		ElementHash: shortHash(canonicalElements(elements)),
		Elements:    elements,
	}
}

func (state observationFingerprint) compactHash() string {
	return shortHash(strings.Join([]string{state.URL, state.Title, state.ContentHash, state.ElementHash}, "\x00"))
}

func canonicalElements(elements map[string]string) string {
	refs := make([]string, 0, len(elements))
	for ref := range elements {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	var builder strings.Builder
	for _, ref := range refs {
		builder.WriteString(ref)
		builder.WriteByte('=')
		builder.WriteString(elements[ref])
		builder.WriteByte('\n')
	}
	return builder.String()
}

func compareElementMaps(previous, current map[string]string) (added, removed, changed []string) {
	for ref, label := range current {
		previousLabel, exists := previous[ref]
		if !exists {
			added = append(added, ref)
		} else if previousLabel != label {
			changed = append(changed, ref)
		}
	}
	for ref := range previous {
		if _, exists := current[ref]; !exists {
			removed = append(removed, ref)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	return added, removed, changed
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func verifyAction(
	request Request,
	raw map[string]any,
	classification Classification,
	confidence float64,
	delta IncrementalObservation,
	screenshot map[string]any,
) ActionVerification {
	action := strings.ToLower(strings.TrimSpace(request.Action))
	verification := ActionVerification{
		Action: action, Status: "observed", Confidence: confidence,
		Reason: "environment_observed", RecommendedNext: "continue",
	}
	if classification.Type == "challenge" || boolValue(raw["challenge_detected"]) {
		verification.Status = "blocked"
		verification.Confidence = 0.99
		verification.Reason = "browser_challenge_requires_user_takeover"
		verification.Evidence = []string{"challenge_detected"}
		verification.RecommendedNext = "request_user_takeover"
		return verification
	}
	if classification.Type == "error_page" || isErrorPageObservation(raw) {
		verification.Status = "failed"
		verification.Confidence = 0.99
		verification.Reason = "navigation_reached_error_page"
		verification.Evidence = []string{"error_page_observed"}
		verification.RecommendedNext = "revise_action"
		return verification
	}
	if boolValue(raw["action_failed"]) || stringValue(raw["error"]) != "" {
		verification.Status = "failed"
		verification.Reason = "browser_reported_action_error"
		verification.Evidence = []string{"action_error"}
		verification.RecommendedNext = "revise_action"
		return verification
	}
	switch action {
	case "open", "navigate", "extract":
		currentURL := stringValue(raw["url"])
		if currentURL == "" {
			return uncertainVerification(verification, "navigation_has_no_observed_url", screenshot)
		}
		verification.Evidence = append(verification.Evidence, "url_observed")
		if target := stringValue(request.Arguments["url"]); target != "" && !sameNavigationTarget(target, currentURL) {
			return uncertainVerification(verification, "navigation_target_did_not_match_observed_url", screenshot)
		}
		verification.Status = "verified"
		verification.Reason = "navigation_target_observed"
	case "click", "press", "scroll", "drag":
		if !delta.HasPrevious {
			return uncertainVerification(verification, "no_previous_observation_for_comparison", screenshot)
		}
		if !delta.Changed {
			return uncertainVerification(verification, "action_produced_no_observable_change", screenshot)
		}
		verification.Status = "verified"
		verification.Reason = "post_action_state_changed"
		verification.Evidence = deltaEvidence(delta)
	case "type", "select":
		if ref := stringValue(request.Arguments["ref"]); ref == "" {
			return uncertainVerification(verification, "typed_element_ref_missing", screenshot)
		}
		verification.Status = "verified"
		verification.Reason = "browser_accepted_input_and_page_was_observed"
		verification.Evidence = []string{"target_ref_present", "sensitive_value_not_echoed"}
	case "hover":
		if ref := stringValue(request.Arguments["ref"]); ref == "" {
			return uncertainVerification(verification, "hovered_element_ref_missing", screenshot)
		}
		verification.Status = "observed"
		verification.Reason = "hover_target_observed"
		verification.Evidence = []string{"target_ref_present"}
	case "back", "forward":
		if !boolValue(request.Arguments["document_transition_observed"]) {
			return uncertainVerification(verification, "navigation_transition_not_observed", screenshot)
		}
		verification.Status = "verified"
		verification.Reason = "history_navigation_observed"
		verification.Evidence = []string{"document_transition_observed"}
	case "refresh":
		if stringValue(raw["url"]) == "" {
			return uncertainVerification(verification, "refresh_has_no_observed_url", screenshot)
		}
		verification.Status = "verified"
		verification.Reason = "page_reload_observed"
		verification.Evidence = []string{"url_observed"}
	case "download":
		verification.Status = "verified"
		verification.Reason = "download_action_completed_before_observation"
		verification.Evidence = []string{"download_completion_event"}
	case "screenshot":
		if screenshotAvailable(screenshot) {
			verification.Status = "verified"
			verification.Reason = "screenshot_file_observed"
			verification.Evidence = []string{"screenshot_available"}
		} else {
			return uncertainVerification(verification, "screenshot_not_available", screenshot)
		}
	case "observe", "wait", "":
		verification.Reason = "observation_completed"
	}
	return verification
}

func uncertainVerification(verification ActionVerification, reason string, screenshot map[string]any) ActionVerification {
	verification.Status = "uncertain"
	verification.Confidence = clamp(verification.Confidence, 0.2, 0.65)
	verification.Reason = reason
	verification.RecommendedNext = "observe_same_session"
	verification.RetryAfterMS = 500
	if screenshotAvailable(screenshot) {
		verification.Evidence = append(verification.Evidence, "visual_evidence_captured")
	}
	return verification
}

func screenshotAvailable(screenshot map[string]any) bool {
	if screenshot == nil {
		return false
	}
	available, _ := screenshot["available"].(bool)
	return available
}

func deltaEvidence(delta IncrementalObservation) []string {
	evidence := make([]string, 0, 4)
	if delta.URLChanged {
		evidence = append(evidence, "url_changed")
	}
	if delta.TitleChanged {
		evidence = append(evidence, "title_changed")
	}
	if delta.ContentChanged {
		evidence = append(evidence, "content_changed")
	}
	if delta.ElementsChanged {
		evidence = append(evidence, "interactive_elements_changed")
	}
	return evidence
}

func sameNavigationTarget(expected, actual string) bool {
	expectedURL, expectedErr := url.Parse(strings.TrimSpace(expected))
	actualURL, actualErr := url.Parse(strings.TrimSpace(actual))
	if expectedErr == nil && actualErr == nil && expectedURL.Hostname() != "" && actualURL.Hostname() != "" {
		expectedHost := strings.TrimPrefix(strings.ToLower(expectedURL.Hostname()), "www.")
		actualHost := strings.TrimPrefix(strings.ToLower(actualURL.Hostname()), "www.")
		return expectedHost == actualHost || strings.HasSuffix(actualHost, "."+expectedHost)
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(actual)), strings.ToLower(strings.TrimSpace(expected)))
}
