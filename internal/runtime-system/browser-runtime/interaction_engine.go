package browser_runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"
)

const browserInteractionSchema = "athena.browser.interaction-transaction.v3"

type browserActionPolicyDecision struct {
	Risk     string `json:"risk"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type browserInteractionReport struct {
	Schema       string                         `json:"schema"`
	Action       string                         `json:"action"`
	Policy       browserActionPolicyDecision    `json:"policy"`
	Status       string                         `json:"status"`
	Reason       string                         `json:"reason"`
	Attempts     int                            `json:"attempts"`
	Reobserved   bool                           `json:"reobserved,omitempty"`
	Recovered    bool                           `json:"recovered,omitempty"`
	Verification *perception.ActionVerification `json:"verification,omitempty"`
	StartedAt    time.Time                      `json:"started_at"`
	CompletedAt  time.Time                      `json:"completed_at"`
	DurationMS   int64                          `json:"duration_ms"`
}

type browserInteractionTrace struct {
	mu      sync.Mutex
	reports []browserInteractionReport
}

func newBrowserInteractionTrace() *browserInteractionTrace {
	return &browserInteractionTrace{reports: make([]browserInteractionReport, 0, 8)}
}

func (t *browserInteractionTrace) append(report browserInteractionReport) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.reports = append(t.reports, report)
	t.mu.Unlock()
}

func (t *browserInteractionTrace) snapshot() []browserInteractionReport {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	result := make([]browserInteractionReport, len(t.reports))
	copy(result, t.reports)
	return result
}

func (b *browserController) runTaskAction(
	ctx context.Context,
	request browserTaskRequest,
	action string,
	arguments map[string]any,
	message string,
	progressValue int,
) (map[string]any, error) {
	started := time.Now().UTC()
	policy := browserTaskActionPolicy(action, arguments)
	report := browserInteractionReport{
		Schema: browserInteractionSchema, Action: action, Policy: policy, Status: "running", StartedAt: started,
	}
	if err := request.Budget.consume(action); err != nil {
		state := map[string]any{"session_id": request.SessionID}
		report.Status, report.Reason = "budget_exhausted", err.Error()
		finishBrowserInteractionReport(state, &report, request.Trace)
		return state, err
	}
	if policy.Decision != "ALLOW" {
		state := browserInteractionIntervention(request.SessionID, action, policy)
		report.Status, report.Reason = "waiting_user", policy.Reason
		finishBrowserInteractionReport(state, &report, request.Trace)
		return state, nil
	}

	state, err := b.runTaskActionOnce(ctx, request, action, arguments, message, progressValue)
	report.Attempts = 1
	if verification, ok := browserVerificationFromState(state); ok {
		report.Verification = &verification
	}
	if err != nil {
		report.Status, report.Reason = "failed", err.Error()
		finishBrowserInteractionReport(state, &report, request.Trace)
		return state, err
	}
	if browserInteractionVerified(action, arguments, state, report.Verification) {
		report.Status, report.Reason = "verified", browserInteractionSuccessReason(report.Verification)
		finishBrowserInteractionReport(state, &report, request.Trace)
		return state, nil
	}
	if report.Verification != nil && (report.Verification.Status == "blocked" || report.Verification.Status == "failed") {
		err = fmt.Errorf("browser %s action failed verification: %s", action, report.Verification.Reason)
		report.Status, report.Reason = report.Verification.Status, report.Verification.Reason
		finishBrowserInteractionReport(state, &report, request.Trace)
		return state, err
	}

	if request.Progress != nil {
		request.Progress(browserActionProgress{
			Stage: "reobserve", Progress: min(progressValue+2, 99),
			Message: "Re-observing the browser after an unverified action", State: map[string]any{"action": action},
		})
	}
	if err := request.Budget.consume("extract"); err != nil {
		report.Status, report.Reason = "budget_exhausted", err.Error()
		finishBrowserInteractionReport(state, &report, request.Trace)
		return state, err
	}
	reobserved, observeErr := b.runTaskActionOnce(ctx, request, "extract", map[string]any{
		"snapshot": true, "interaction_source": "verification_reobserve",
	}, "Re-observing browser state", min(progressValue+2, 99))
	report.Reobserved = true
	if observeErr == nil && browserInteractionPostcondition(action, arguments, reobserved) {
		state = reobserved
		report.Status, report.Reason, report.Recovered = "verified", "postcondition_observed_after_reobserve", true
		finishBrowserInteractionReport(state, &report, request.Trace)
		return state, nil
	}
	if observeErr != nil {
		report.Status, report.Reason = "failed", "reobserve_failed: "+observeErr.Error()
		finishBrowserInteractionReport(state, &report, request.Trace)
		return state, observeErr
	}
	state = reobserved

	if browserInteractionRetryable(action, arguments, policy) {
		if budgetErr := request.Budget.consume(action); budgetErr != nil {
			report.Status, report.Reason = "budget_exhausted", budgetErr.Error()
			finishBrowserInteractionReport(state, &report, request.Trace)
			return state, budgetErr
		}
		if request.Progress != nil {
			request.Progress(browserActionProgress{
				Stage: "recover", Progress: min(progressValue+3, 99),
				Message: "Retrying a reversible browser action after re-observation", State: map[string]any{"action": action},
			})
		}
		retryArguments := browserInteractionRetryArguments(action, arguments, state)
		state, err = b.runTaskActionOnce(ctx, request, action, retryArguments, message, min(progressValue+3, 99))
		report.Attempts = 2
		if verification, ok := browserVerificationFromState(state); ok {
			report.Verification = &verification
		}
		if err == nil && browserInteractionVerified(action, arguments, state, report.Verification) {
			report.Status, report.Reason, report.Recovered = "verified", "reversible_action_verified_after_retry", true
			finishBrowserInteractionReport(state, &report, request.Trace)
			return state, nil
		}
	}

	if err == nil {
		reason := "action_postcondition_not_observed"
		if report.Verification != nil && report.Verification.Reason != "" {
			reason = report.Verification.Reason
		}
		err = fmt.Errorf("browser %s action could not be verified: %s", action, reason)
	}
	report.Status, report.Reason = "unverified", err.Error()
	finishBrowserInteractionReport(state, &report, request.Trace)
	return state, err
}

func browserTaskActionPolicy(action string, arguments map[string]any) browserActionPolicyDecision {
	action = strings.ToLower(strings.TrimSpace(action))
	label := strings.ToLower(strings.Join([]string{
		browserStringValue(arguments["target_label"]), browserStringValue(arguments["field_label"]),
		browserStringValue(arguments["purpose"]),
	}, " "))
	if browserSensitiveActionLabel(label) {
		return browserActionPolicyDecision{Risk: "HIGH", Decision: "ASK_USER", Reason: "sensitive_browser_action_requires_confirmation"}
	}
	switch action {
	case "download", "upload":
		return browserActionPolicyDecision{Risk: "MEDIUM", Decision: "ASK_USER", Reason: action + "_requires_confirmation"}
	case "type":
		if browserSensitiveInputLabel(label) {
			return browserActionPolicyDecision{Risk: "HIGH", Decision: "ASK_USER", Reason: "sensitive_input_requires_user_takeover"}
		}
		return browserActionPolicyDecision{Risk: "LOW", Decision: "ALLOW", Reason: "reversible_text_input"}
	case "navigate", "click", "play", "hover", "select", "press", "scroll", "back", "forward", "refresh", "wait", "extract", "screenshot":
		return browserActionPolicyDecision{Risk: "LOW", Decision: "ALLOW", Reason: "reversible_browser_interaction"}
	case "drag":
		return browserActionPolicyDecision{Risk: "MEDIUM", Decision: "ASK_USER", Reason: "drag_requires_confirmation"}
	default:
		return browserActionPolicyDecision{Risk: "HIGH", Decision: "BLOCK", Reason: "unsupported_internal_browser_action"}
	}
}

func browserSensitiveActionLabel(label string) bool {
	for _, marker := range []string{
		"buy", "purchase", "checkout", "pay", "place order", "confirm order", "delete", "remove account", "send message",
		"book now", "reserve", "subscribe", "change password", "security settings", "购买", "購買", "付款", "结算", "結算",
		"提交订单", "提交訂單", "删除", "刪除", "发送", "發送", "预订", "預訂", "预约", "預約", "修改密码", "修改密碼",
	} {
		if strings.Contains(label, marker) {
			return true
		}
	}
	return false
}

func browserSensitiveInputLabel(label string) bool {
	for _, marker := range []string{
		"password", "passcode", "verification code", "one-time", "otp", "credit card", "card number", "cvv", "secret",
		"密码", "密碼", "验证码", "驗證碼", "信用卡", "银行卡", "銀行卡", "安全码", "安全碼",
	} {
		if strings.Contains(label, marker) {
			return true
		}
	}
	return false
}

func browserInteractionIntervention(sessionID, action string, policy browserActionPolicyDecision) map[string]any {
	message := "Athena needs your confirmation before continuing this browser action."
	if policy.Decision == "BLOCK" {
		message = "Athena blocked an unsupported or unsafe browser action."
	}
	return map[string]any{
		"session_id":                 sessionID,
		"user_intervention_required": true,
		"intervention": map[string]any{
			"kind": "browser_action_approval", "action": action, "reason": policy.Reason,
			"message": message, "resume_session_id": sessionID,
		},
	}
}

func browserVerificationFromState(state map[string]any) (perception.ActionVerification, bool) {
	if state == nil {
		return perception.ActionVerification{}, false
	}
	switch value := state["verification"].(type) {
	case perception.ActionVerification:
		return value, true
	case *perception.ActionVerification:
		if value != nil {
			return *value, true
		}
	case map[string]any:
		return perception.ActionVerification{
			Action: browserStringValue(value["action"]), Status: browserStringValue(value["status"]),
			Confidence: browserFloatValue(value["confidence"]), Reason: browserStringValue(value["reason"]),
			RecommendedNext: browserStringValue(value["recommended_next"]), RetryAfterMS: int(int64Argument(value["retry_after_ms"])),
		}, true
	}
	return perception.ActionVerification{}, false
}

func browserInteractionVerified(action string, arguments map[string]any, state map[string]any, verification *perception.ActionVerification) bool {
	if verification != nil {
		switch verification.Status {
		case "verified":
			return true
		case "observed":
			if action == "extract" || action == "wait" || action == "scroll" || action == "hover" {
				return true
			}
		}
	}
	return browserInteractionPostcondition(action, arguments, state)
}

func browserInteractionPostcondition(action string, arguments map[string]any, state map[string]any) bool {
	if verification := browserAutomationVerificationFromArguments(arguments); verification.Type != "" {
		before := map[string]any{
			"url":   browserStringValue(arguments["expected_page_url"]),
			"title": browserStringValue(arguments["expected_page_title"]),
		}
		return browserAutomationVerify(verification, before, state).Verified
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "navigate":
		return browserTargetObserved(state, browserStringValue(arguments["url"]))
	case "click":
		if target := browserStringValue(arguments["target_url"]); target != "" {
			return browserTargetObserved(state, target)
		}
	case "play":
		playback, _ := state["playback"].(map[string]any)
		playing, _ := playback["playing"].(bool)
		verified, _ := playback["verified"].(bool)
		progressed, _ := playback["progressed"].(bool)
		return playing && (verified || progressed)
	case "screenshot":
		return browserStringValue(state["screenshot_path"]) != ""
	case "type", "select":
		return browserStringValue(arguments["ref"]) != "" && (browserStringValue(state["url"]) != "" || browserStringValue(state["title"]) != "")
	case "back", "forward":
		transitioned, _ := arguments["document_transition_observed"].(bool)
		return transitioned
	case "refresh", "extract", "wait", "scroll", "hover":
		return browserStringValue(state["url"]) != "" || browserStringValue(state["title"]) != ""
	}
	return false
}

func browserInteractionRetryable(action string, arguments map[string]any, policy browserActionPolicyDecision) bool {
	if policy.Risk != "LOW" || policy.Decision != "ALLOW" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "navigate", "play":
		return true
	case "click":
		return browserStringValue(arguments["target_url"]) != ""
	default:
		return false
	}
}

func browserInteractionRetryArguments(action string, arguments, state map[string]any) map[string]any {
	targetURL := browserStringValue(arguments["target_url"])
	currentURL := browserStringValue(state["url"])
	if targetURL == "" || currentURL == "" || normalizeBrowserPageURL(targetURL) != normalizeBrowserPageURL(currentURL) {
		return arguments
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "click", "play":
		rebound := make(map[string]any, len(arguments))
		for key, value := range arguments {
			rebound[key] = value
		}
		rebound["expected_page_url"] = currentURL
		return rebound
	default:
		return arguments
	}
}

func browserInteractionSuccessReason(verification *perception.ActionVerification) string {
	if verification != nil && verification.Reason != "" {
		return verification.Reason
	}
	return "action_postcondition_observed"
}

func finishBrowserInteractionReport(state map[string]any, report *browserInteractionReport, trace *browserInteractionTrace) {
	if report == nil {
		return
	}
	report.CompletedAt = time.Now().UTC()
	report.DurationMS = report.CompletedAt.Sub(report.StartedAt).Milliseconds()
	trace.append(*report)
	if state != nil {
		state["interaction"] = *report
	}
}
