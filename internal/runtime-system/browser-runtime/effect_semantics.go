package browser_runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	semantics "github.com/good-fish-man/athena-protocol/draft/v0alpha"
)

type browserEffectTrace struct {
	value *semantics.SemanticTrace
}

const browserEffectFailureSchema = "athena.browser.effect-failure.v3"

const (
	browserFailureSnapshotDrift        = "snapshot_drift"
	browserFailureTargetMissing        = "target_missing"
	browserFailureAuthentication       = "authentication_required"
	browserFailureForbiddenEffect      = "forbidden_effect"
	browserFailureCancelled            = "cancelled"
	browserFailureRetryExhausted       = "retry_exhausted"
	browserFailureExecution            = "execution_failed"
	browserFailureInsufficientEvidence = "insufficient_evidence"
)

type browserEffectFailure struct {
	Schema       string   `json:"schema"`
	Kind         string   `json:"kind"`
	Reason       string   `json:"reason"`
	Recoverable  bool     `json:"recoverable"`
	Retryable    bool     `json:"retryable"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

func newBrowserEffectTrace(value map[string]any, actionID, sessionID string) (*browserEffectTrace, error) {
	trace, err := semantics.TraceFromMap(value)
	if err != nil || trace == nil {
		return nil, err
	}
	now := time.Now().UTC()
	if trace.Run == nil {
		trace.Run = &semantics.PlanRun{
			PlanRunID: semantics.NewID("plan-run"), PlanRef: trace.Plan.PlanCandidateID,
			Status: semantics.RunRunning, StartedAt: now,
			Execution: semantics.ExecutionContext{
				WorldSnapshotRef: trace.Outcome.TargetSpec.SourceSnapshotRef,
				EnvironmentRef:   "browser-session:" + strings.TrimSpace(sessionID),
				ActorBindings:    map[string]string{"browser_session": strings.TrimSpace(sessionID)},
			},
		}
	}
	if len(trace.Attempts) == 0 {
		trace.Attempts = []semantics.ActionAttempt{{
			AttemptID: semantics.NewID("action-attempt"), PlanRunRef: trace.Run.PlanRunID,
			PlanStepRef: browserEffectExecutionStep(trace.Plan), ActionID: strings.TrimSpace(actionID),
			Attempt: 1, Status: semantics.AttemptRunning, StartedAt: now,
		}}
	}
	return &browserEffectTrace{value: trace}, nil
}

func (t *browserEffectTrace) finish(state map[string]any, plan *browserTaskPlan, taskErr error) error {
	if t == nil || t.value == nil || plan == nil {
		return taskErr
	}
	now := time.Now().UTC()
	failure := classifyBrowserEffectFailure(state, plan, taskErr)
	if failure.Kind != "" {
		state["failure"] = failure
	}
	t.resolveTarget(plan, state, now)
	t.invalidateTarget(failure)
	t.discoverAffordance(plan)
	results := make([]semantics.VerificationResult, 0, len(browserEffectClauses(t.value.Outcome)))
	for _, clause := range browserEffectClauses(t.value.Outcome) {
		results = append(results, verifyBrowserEffect(t.value, clause, state, plan, failure, taskErr, now))
	}
	t.value.VerificationResults = results
	summary := semantics.Aggregate(t.value.Outcome.OutcomeID, t.value.Run.PlanRunID, results, now)
	t.value.VerificationSummary = &summary
	if failure.Kind == browserFailureCancelled {
		t.value.Run.Status = semantics.RunCancelled
		t.value.Run.TerminalReason = failure.Reason
	} else {
		t.value.Run.Status = semantics.TerminalRunStatus(&summary)
		t.value.Run.TerminalReason = browserEffectSummaryMessage(summary)
	}
	if t.value.Run.Status != semantics.RunRunning && t.value.Run.Status != semantics.RunPending {
		t.value.Run.FinishedAt = now
	}
	for index := range t.value.Attempts {
		attempt := &t.value.Attempts[index]
		if !attempt.FinishedAt.IsZero() {
			continue
		}
		switch {
		case failure.Kind == browserFailureCancelled:
			attempt.FinishedAt = now
			attempt.Status = semantics.AttemptCancelled
			attempt.Error = failure.Reason
		case failure.Kind == browserFailureSnapshotDrift || failure.Kind == browserFailureTargetMissing:
			attempt.FinishedAt = now
			attempt.Status = semantics.AttemptFailed
			attempt.Error = failure.Reason
		case browserInterventionRequired(state):
			attempt.Status = semantics.AttemptPending
		case taskErr != nil:
			attempt.FinishedAt = now
			attempt.Status = semantics.AttemptFailed
			attempt.Error = taskErr.Error()
		default:
			attempt.FinishedAt = now
			attempt.Status = semantics.AttemptSucceeded
		}
	}
	if err := t.value.Validate(); err != nil {
		return fmt.Errorf("validate browser effect trace: %w", err)
	}
	encoded, err := semantics.ToMap(t.value)
	if err != nil {
		return fmt.Errorf("encode browser effect trace: %w", err)
	}
	state[semantics.StateKey] = encoded

	plan.Completed = summary.Status == semantics.OutcomeSucceeded
	switch summary.Status {
	case semantics.OutcomeSucceeded:
		plan.Message = browserEffectSuccessMessage(t.value.Outcome, plan.Message)
	case semantics.OutcomeUnknown:
		if failure.Kind == browserFailureCancelled {
			plan.Message = failure.Reason
			break
		}
		plan.Message = browserEffectSummaryMessage(summary)
		reason := browserFailureInsufficientEvidence
		recoverable := true
		if failure.Kind != "" {
			reason = failure.Kind
			recoverable = failure.Recoverable
		}
		state["continuation_required"] = map[string]any{
			"kind": "effect_verification", "reason": reason, "session_id": browserStringValue(state["session_id"]),
			"recoverable": recoverable, "retryable": failure.Retryable,
		}
		state["suggested_actions"] = appendBrowserEffectObserveSuggestion(
			state["suggested_actions"], browserStringValue(state["session_id"]), browserStringValue(state["url"]),
		)
	case semantics.OutcomeFailed, semantics.OutcomeConflicting:
		plan.Message = browserEffectSummaryMessage(summary)
		if taskErr == nil {
			taskErr = fmt.Errorf("browser outcome verification %s", summary.Status)
		}
	}
	if failure.Recoverable && summary.Status == semantics.OutcomeUnknown {
		return nil
	}
	return taskErr
}

func (t *browserEffectTrace) invalidateTarget(failure browserEffectFailure) {
	if t == nil || t.value == nil || t.value.TargetResolution == nil {
		return
	}
	if failure.Kind != browserFailureSnapshotDrift && failure.Kind != browserFailureTargetMissing {
		return
	}
	t.value.TargetResolution.Status = semantics.TargetResolutionReobserve
	t.value.TargetResolution.SelectedEntityRef = ""
	t.value.TargetResolution.Confidence = 0
	t.value.TargetResolution.Reason = failure.Kind
	t.value.TargetResolution.EvidenceRefs = append(t.value.TargetResolution.EvidenceRefs, failure.EvidenceRefs...)
}

func (t *browserEffectTrace) resolveTarget(plan *browserTaskPlan, state map[string]any, now time.Time) {
	if t == nil || t.value == nil || plan == nil || plan.Resolution == nil {
		return
	}
	resolution := plan.Resolution
	readSet := map[string]any{
		"session_id": state["session_id"], "url": state["url"], "title": state["title"],
		"target_spec": t.value.Outcome.TargetSpec,
	}
	readSetHash := semantics.Hash(readSet)
	snapshotRef := strings.TrimSpace(t.value.Outcome.TargetSpec.SourceSnapshotRef)
	if readSetHash != "" {
		snapshotRef = "browser-observation:" + readSetHash
	}
	converted := &semantics.TargetResolution{
		ResolutionID: semantics.NewID("target-resolution"), TargetSpecRef: t.value.Outcome.TargetSpec.TargetSpecID,
		SourceSnapshotRef: snapshotRef, Status: string(resolution.Decision), Confidence: resolution.Confidence,
		Reason: resolution.Reason, WorldReadSetHash: readSetHash, ResolvedAt: now, ValidUntil: now.Add(30 * time.Second),
	}
	for _, candidate := range resolution.Candidates {
		entityRef := browserTargetEntityRef(candidate)
		converted.Candidates = append(converted.Candidates, semantics.TargetCandidate{
			EntityRef: entityRef, Label: candidate.Label, URL: candidate.URL, Role: candidate.Role, Kind: candidate.Kind,
			Ordinal: candidate.Order, Confidence: candidate.Confidence,
			EvidenceRefs: []string{"browser-target:" + entityRef},
			Metadata:     map[string]any{"source": candidate.Source, "position": candidate.Position, "playable": candidate.Playable},
		})
	}
	if resolution.Selected != nil {
		converted.SelectedEntityRef = browserTargetEntityRef(*resolution.Selected)
		converted.EvidenceRefs = []string{"browser-target:" + converted.SelectedEntityRef}
	}
	t.value.TargetResolution = converted
}

func (t *browserEffectTrace) discoverAffordance(plan *browserTaskPlan) {
	if t == nil || t.value == nil || plan == nil {
		return
	}
	targetRef := ""
	if t.value.TargetResolution != nil {
		targetRef = t.value.TargetResolution.SelectedEntityRef
	}
	if targetRef == "" {
		targetRef = t.value.Outcome.TargetSpec.TargetSpecID
	}
	operation := "navigate"
	for _, clause := range t.value.Outcome.DesiredEffects {
		switch clause.Predicate {
		case "media.playback_state":
			operation = "play"
		case "target.selection_state":
			operation = "click"
		}
	}
	effects := make([]string, 0, len(t.value.Outcome.DesiredEffects))
	for _, clause := range t.value.Outcome.DesiredEffects {
		effects = append(effects, clause.ClauseID)
	}
	confidence := 0.72
	evidence := []string{"browser-task-plan:" + plan.Intent}
	if t.value.TargetResolution != nil {
		confidence = t.value.TargetResolution.Confidence
		evidence = append(evidence, t.value.TargetResolution.EvidenceRefs...)
	}
	t.value.Affordances = []semantics.AffordanceCandidate{{
		AffordanceID: semantics.NewID("affordance"), TargetRef: targetRef,
		Capability: "browser.task", Operation: operation, Effects: effects,
		Confidence: confidence, EvidenceRefs: evidence,
	}}
}

func verifyBrowserEffect(trace *semantics.SemanticTrace, clause semantics.EffectClause, state map[string]any, plan *browserTaskPlan, failure browserEffectFailure, taskErr error, now time.Time) semantics.VerificationResult {
	result := semantics.VerificationResult{
		VerificationID: semantics.NewID("verification"), OutcomeRef: trace.Outcome.OutcomeID,
		PlanRunRef: trace.Run.PlanRunID, EffectClauseID: clause.ClauseID,
		Status: semantics.VerificationUnknown, ExpectedValue: clause.Expected,
		Confidence: 0.25, Reason: "required browser evidence was not observed", VerifiedAt: now,
	}
	sessionID := browserStringValue(state["session_id"])
	switch clause.Predicate {
	case "media.playback_state":
		playback, _ := state["playback"].(map[string]any)
		playing, hasPlaying := playback["playing"].(bool)
		verified, hasVerified := playback["verified"].(bool)
		result.ObservedValue = map[string]any{"playing": playing, "verified": verified}
		result.EvidenceRefs = []string{"browser-state:playback"}
		switch {
		case failure.Kind == browserFailureCancelled:
			result.Reason = "execution was cancelled before playback could be verified"
		case failure.Kind == browserFailureSnapshotDrift:
			result.Reason = "the target snapshot changed before playback could be verified"
		case failure.Kind == browserFailureTargetMissing:
			result.Reason = "the resolved target is no longer present; a new observation is required"
		case browserAuthenticationRequired(state):
			result.Status, result.Confidence, result.Reason = semantics.VerificationUnsatisfied, 0.97, "media playback is blocked by required user authentication"
		case browserEffectContinuationRequired(state):
			result.Reason = "media playback is waiting for an explicit continuation"
		case hasPlaying && playing && (!hasVerified || verified):
			result.Status, result.Confidence, result.Reason = semantics.VerificationSatisfied, 0.98, "post-action media state is playing"
		case hasPlaying && !playing:
			result.Status, result.Confidence, result.Reason = semantics.VerificationUnsatisfied, 0.94, "post-action media state is not playing"
		case taskErr != nil:
			result.Status, result.Confidence, result.Reason = semantics.VerificationUnsatisfied, 0.9, taskErr.Error()
		}
	case "browser.page_state":
		pageURL := browserStringValue(state["url"])
		result.ObservedValue = pageURL
		result.EvidenceRefs = []string{"browser-state:url"}
		switch {
		case failure.Kind == browserFailureCancelled:
			result.Reason = "execution was cancelled before the page outcome could be verified"
		case failure.Kind == browserFailureSnapshotDrift:
			result.Reason = "the observed page no longer matches the target snapshot"
		case failure.Kind == browserFailureTargetMissing:
			result.Reason = "the target is missing from the current page observation"
		case browserChallengeDetected(state) || browserInterventionRequired(state) || browserEffectContinuationRequired(state):
			result.Reason = "page outcome is waiting for user intervention"
		case taskErr != nil:
			result.Status, result.Confidence, result.Reason = semantics.VerificationUnsatisfied, 0.9, taskErr.Error()
		case pageURL != "":
			result.Status, result.Confidence, result.Reason = semantics.VerificationSatisfied, 0.96, "a reachable post-action page was observed"
		}
	case "target.selection_state":
		verified := browserPlanHasVerifiedSelection(plan)
		result.ObservedValue = verified
		result.EvidenceRefs = []string{"browser-interaction:selection"}
		switch {
		case failure.Kind == browserFailureCancelled:
			result.Reason = "execution was cancelled before the target selection could be verified"
		case failure.Kind == browserFailureSnapshotDrift:
			result.Reason = "the target snapshot changed before selection could be verified"
		case failure.Kind == browserFailureTargetMissing:
			result.Reason = "the target is missing from the current page observation"
		case verified:
			result.Status, result.Confidence, result.Reason = semantics.VerificationSatisfied, 0.93, "selection interaction was verified"
		case taskErr != nil:
			result.Status, result.Confidence, result.Reason = semantics.VerificationUnsatisfied, 0.9, taskErr.Error()
		}
	case "browser.session_identity":
		expected, _ := clause.Expected.(string)
		result.ObservedValue = sessionID
		result.EvidenceRefs = []string{"browser-state:session"}
		switch {
		case sessionID == "":
			result.Reason = "browser session id was not observed"
		case expected == "" || expected == sessionID:
			result.Status, result.Confidence, result.Reason = semantics.VerificationSatisfied, 0.99, "browser session identity was preserved"
		default:
			result.Status, result.Confidence, result.Reason = semantics.VerificationConflicting, 0.99, "observed browser session differs from the requested session"
		}
	case "browser.authentication_state":
		changed, explicit := browserAuthenticationStateChanged(state)
		result.ObservedValue = map[string]any{"changed": changed, "explicit": explicit}
		result.EvidenceRefs = []string{"browser-state:authentication", "browser-plan:interaction-trace"}
		switch {
		case explicit && changed:
			result.Status, result.Confidence, result.Reason = semantics.VerificationUnsatisfied, 0.99, "browser authentication state changed without authorization"
		case explicit && !changed:
			result.Status, result.Confidence, result.Reason = semantics.VerificationSatisfied, 0.99, "browser reported that authentication state was unchanged"
		case browserAuthenticationRequired(state):
			result.Status, result.Confidence, result.Reason = semantics.VerificationSatisfied, 0.97, "authentication is waiting for the user and Athena did not sign in automatically"
		case browserPlanAvoidedAuthentication(plan):
			result.Status, result.Confidence, result.Reason = semantics.VerificationSatisfied, 0.86, "the governed interaction trace contains no authentication action"
		default:
			result.Reason = "authentication preservation could not be established from the interaction trace"
		}
	case "browser.window_closed":
		closed, hasClosed := state["closed"].(bool)
		result.ObservedValue = closed
		result.EvidenceRefs = []string{"browser-state:window"}
		switch {
		case hasClosed && closed:
			result.Status, result.Confidence, result.Reason = semantics.VerificationUnsatisfied, 0.99, "browser window was closed"
		case sessionID != "" || (hasClosed && !closed):
			result.Status, result.Confidence, result.Reason = semantics.VerificationSatisfied, 0.95, "browser window remained available"
		}
	}
	return result
}

func classifyBrowserEffectFailure(state map[string]any, plan *browserTaskPlan, taskErr error) browserEffectFailure {
	newFailure := func(kind, reason string, recoverable, retryable bool) browserEffectFailure {
		return browserEffectFailure{
			Schema: browserEffectFailureSchema, Kind: kind, Reason: strings.TrimSpace(reason),
			Recoverable: recoverable, Retryable: retryable,
			EvidenceRefs: []string{"browser-failure:" + kind},
		}
	}
	if errors.Is(taskErr, context.Canceled) {
		return newFailure(browserFailureCancelled, "browser task was cancelled", false, false)
	}
	if browserAuthenticationRequired(state) {
		return newFailure(browserFailureAuthentication, "user authentication is required", false, false)
	}
	changed, explicit := browserAuthenticationStateChanged(state)
	closed, _ := state["closed"].(bool)
	if (explicit && changed) || closed {
		return newFailure(browserFailureForbiddenEffect, "a forbidden browser side effect was observed", false, false)
	}
	if taskErr != nil {
		reason := strings.TrimSpace(taskErr.Error())
		lower := strings.ToLower(reason)
		for _, marker := range []string{"page changed from", "stale page", "snapshot changed", "snapshot drift"} {
			if strings.Contains(lower, marker) {
				return newFailure(browserFailureSnapshotDrift, reason, true, true)
			}
		}
		for _, marker := range []string{"candidate_not_visible", "target_element_disappeared", "target disappeared", "target is no longer", "requested ordinal", "no eligible candidates"} {
			if strings.Contains(lower, marker) {
				return newFailure(browserFailureTargetMissing, reason, true, true)
			}
		}
		for _, marker := range []string{"execution budget", "deadline exceeded", "could not be verified", "retry exhausted"} {
			if strings.Contains(lower, marker) {
				return newFailure(browserFailureRetryExhausted, reason, false, false)
			}
		}
		return newFailure(browserFailureExecution, reason, false, false)
	}
	if plan != nil && plan.Resolution != nil && plan.Resolution.Decision == browserResolutionReobserve {
		reason := strings.TrimSpace(plan.Resolution.Reason)
		if reason == "" {
			reason = "target resolution requires a fresh observation"
		}
		return newFailure(browserFailureTargetMissing, reason, true, true)
	}
	return browserEffectFailure{}
}

func browserEffectClauses(outcome semantics.OutcomeSpec) []semantics.EffectClause {
	result := append([]semantics.EffectClause(nil), outcome.DesiredEffects...)
	result = append(result, outcome.MustPreserve...)
	result = append(result, outcome.ForbiddenEffects...)
	return result
}

func browserTargetEntityRef(candidate browserTargetCandidate) string {
	for _, value := range []string{candidate.ID, candidate.Ref, candidate.URL, candidate.Label} {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return semantics.NewID("browser-entity")
}

func browserPlanHasVerifiedSelection(plan *browserTaskPlan) bool {
	if plan == nil {
		return false
	}
	for _, interaction := range plan.Interactions {
		action := strings.ToLower(strings.TrimSpace(interaction.Action))
		if (action == "click" || action == "select" || action == "press") && interaction.Status == "verified" {
			return true
		}
	}
	return false
}

func browserAuthenticationRequired(state map[string]any) bool {
	intervention, _ := state["intervention"].(map[string]any)
	category := strings.ToLower(strings.TrimSpace(browserStringValue(intervention["category"])))
	kind := strings.ToLower(strings.TrimSpace(browserStringValue(intervention["kind"])))
	return category == "authentication" || strings.Contains(kind, "authentication") || strings.Contains(kind, "login") || strings.Contains(kind, "sign_in")
}

func browserAuthenticationStateChanged(state map[string]any) (bool, bool) {
	if changed, ok := state["auth_state_changed"].(bool); ok {
		return changed, true
	}
	authentication, _ := state["authentication_state"].(map[string]any)
	changed, ok := authentication["changed"].(bool)
	return changed, ok
}

func browserEffectContinuationRequired(state map[string]any) bool {
	if state == nil {
		return false
	}
	continuation, ok := state["continuation_required"].(map[string]any)
	return ok && len(continuation) > 0
}

func browserPlanAvoidedAuthentication(plan *browserTaskPlan) bool {
	if plan == nil {
		return false
	}
	text := strings.ToLower(strings.Join(append([]string{plan.Goal, plan.Message, plan.SelectedLabel}, plan.Steps...), " "))
	for _, marker := range []string{"log in", "login", "sign in", "signin", "authenticate", "登录", "登入", "扫码"} {
		if strings.Contains(text, marker) {
			return false
		}
	}
	for _, interaction := range plan.Interactions {
		if interaction.Action == "type" || interaction.Action == "press" {
			return false
		}
	}
	return true
}

func browserEffectExecutionStep(plan semantics.PlanCandidate) string {
	for _, step := range plan.Steps {
		if len(step.ExpectedEffectIDs) > 0 && step.Operation != "verify" {
			return step.StepID
		}
	}
	if len(plan.Steps) > 0 {
		return plan.Steps[0].StepID
	}
	return ""
}

func browserEffectSummaryMessage(summary semantics.OutcomeVerificationSummary) string {
	return fmt.Sprintf("Outcome verification %s: %d satisfied, %d unsatisfied, %d unknown, %d conflicting.",
		summary.Status, summary.Satisfied, summary.Unsatisfied, summary.Unknown, summary.Conflicting)
}

func browserEffectSuccessMessage(outcome semantics.OutcomeSpec, fallback string) string {
	for _, clause := range outcome.DesiredEffects {
		if clause.Predicate == "media.playback_state" {
			return "Media playback was verified."
		}
		if clause.Predicate == "target.selection_state" {
			return "The requested page control was selected and verified."
		}
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return "Browser outcome was verified."
}

func appendBrowserEffectObserveSuggestion(existing any, sessionID, currentURL string) any {
	suggestion := newBrowserSuggestion(
		sessionID, currentURL, browserSemanticElement{},
		"Observe this page again", "Gather the missing post-action evidence before deciding whether the outcome succeeded.",
		"observe", "browser.observe", map[string]any{"session_id": sessionID, "snapshot": true},
		map[string]any{"effect_verification_available": true},
	)
	switch values := existing.(type) {
	case []browserSuggestedAction:
		return append(values, suggestion)
	case []map[string]any:
		value, _ := semantics.ToMap(suggestion)
		return append(values, value)
	case []any:
		return append(values, suggestion)
	default:
		return []browserSuggestedAction{suggestion}
	}
}
