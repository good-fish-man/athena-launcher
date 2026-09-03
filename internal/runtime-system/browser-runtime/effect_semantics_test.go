package browser_runtime

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	semantics "github.com/good-fish-man/athena-protocol/draft/v0alpha"
)

func TestBrowserEffectTraceVerifiesPlaybackAndPreservesSession(t *testing.T) {
	trace := testBrowserSemanticTrace(t, "media.playback_state", "playing", "athena-session", 2)
	plan := browserTaskPlan{Goal: trace.value.Outcome.Goal, Intent: "open_result", Completed: true}
	state := map[string]any{
		"session_id": "athena-session", "url": "https://example.test/watch/2",
		"playback": map[string]any{"playing": true, "verified": true},
	}
	if err := trace.finish(state, &plan, nil); err != nil {
		t.Fatal(err)
	}
	if !plan.Completed || trace.value.VerificationSummary.Status != semantics.OutcomeSucceeded {
		t.Fatalf("playback outcome was not verified: plan=%+v summary=%+v", plan, trace.value.VerificationSummary)
	}
	if trace.value.VerificationSummary.Satisfied != 4 {
		t.Fatalf("expected all effects satisfied: %+v", trace.value.VerificationSummary)
	}
}

func TestBrowserEffectTraceRejectsFalsePlaybackSuccess(t *testing.T) {
	trace := testBrowserSemanticTrace(t, "media.playback_state", "playing", "athena-session", 2)
	plan := browserTaskPlan{Goal: trace.value.Outcome.Goal, Intent: "open_result", Completed: true}
	state := map[string]any{
		"session_id": "athena-session", "url": "https://example.test/watch/2",
		"playback": map[string]any{"playing": false, "verified": true},
	}
	if err := trace.finish(state, &plan, nil); err == nil {
		t.Fatal("non-playing media must not be reported as success")
	}
	if plan.Completed || trace.value.VerificationSummary.Status != semantics.OutcomeFailed {
		t.Fatalf("false success survived verification: plan=%+v summary=%+v", plan, trace.value.VerificationSummary)
	}
}

func TestBrowserEffectTracePausesWhenEvidenceIsUnknown(t *testing.T) {
	trace := testBrowserSemanticTrace(t, "media.playback_state", "playing", "athena-session", 2)
	plan := browserTaskPlan{Goal: trace.value.Outcome.Goal, Intent: "open_result", Completed: true}
	state := map[string]any{"session_id": "athena-session", "url": "https://example.test/watch/2"}
	if err := trace.finish(state, &plan, nil); err != nil {
		t.Fatal(err)
	}
	if plan.Completed || trace.value.VerificationSummary.Status != semantics.OutcomeUnknown {
		t.Fatalf("unknown evidence must pause, not succeed: plan=%+v summary=%+v", plan, trace.value.VerificationSummary)
	}
	if trace.value.Attempts[0].Status != semantics.AttemptSucceeded {
		t.Fatalf("successful action execution was conflated with unknown goal verification: %#v", trace.value.Attempts[0])
	}
	if _, ok := state["continuation_required"].(map[string]any); !ok {
		t.Fatalf("unknown outcome has no continuation: %#v", state)
	}
}

func TestBrowserEffectTraceDetectsSessionConflict(t *testing.T) {
	trace := testBrowserSemanticTrace(t, "browser.page_state", "available", "athena-session-a", 0)
	plan := browserTaskPlan{Goal: trace.value.Outcome.Goal, Intent: "open", Completed: true}
	state := map[string]any{"session_id": "athena-session-b", "url": "https://example.test/"}
	if err := trace.finish(state, &plan, nil); err == nil {
		t.Fatal("session drift must fail the outcome")
	}
	if trace.value.VerificationSummary.Status != semantics.OutcomeConflicting {
		t.Fatalf("session drift did not produce conflict: %+v", trace.value.VerificationSummary)
	}
}

func TestBrowserEffectTraceBlocksPlaybackWithoutChangingAuthentication(t *testing.T) {
	trace := testBrowserSemanticTrace(t, "media.playback_state", "playing", "athena-session", 2)
	plan := browserTaskPlan{Goal: trace.value.Outcome.Goal, Intent: "open_result", Completed: true, Steps: []string{"open_result", "start_playback"}}
	state := map[string]any{
		"session_id": "athena-session", "url": "https://example.test/login",
		"user_intervention_required": true,
		"intervention": map[string]any{
			"kind": "authentication_required", "category": "authentication",
		},
	}
	if err := trace.finish(state, &plan, nil); err == nil {
		t.Fatal("authentication-blocked playback must not succeed")
	}
	if plan.Completed || trace.value.VerificationSummary.Status != semantics.OutcomeFailed {
		t.Fatalf("authentication block was not reflected in the outcome: plan=%+v summary=%+v", plan, trace.value.VerificationSummary)
	}
	var playback, authentication *semantics.VerificationResult
	for index := range trace.value.VerificationResults {
		result := &trace.value.VerificationResults[index]
		switch result.EffectClauseID {
		case "desired":
			playback = result
		case "preserve-auth":
			authentication = result
		}
	}
	if playback == nil || playback.Status != semantics.VerificationUnsatisfied {
		t.Fatalf("playback effect was not rejected: %#v", playback)
	}
	if authentication == nil || authentication.Status != semantics.VerificationSatisfied {
		t.Fatalf("manual authentication boundary was not preserved: %#v", authentication)
	}
	if trace.value.Attempts[0].Status != semantics.AttemptPending || !trace.value.Attempts[0].FinishedAt.IsZero() {
		t.Fatalf("user-intervention attempt should remain resumable: %#v", trace.value.Attempts[0])
	}
}

func TestBrowserEffectTraceMapsCancellationWithoutInventingFailure(t *testing.T) {
	trace := testBrowserSemanticTrace(t, "media.playback_state", "playing", "athena-session", 2)
	plan := browserTaskPlan{Goal: trace.value.Outcome.Goal, Intent: "open_result", Completed: true}
	state := map[string]any{"session_id": "athena-session", "url": "https://example.test/watch/2"}
	err := trace.finish(state, &plan, context.Canceled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error was lost: %v", err)
	}
	if trace.value.Run.Status != semantics.RunCancelled || trace.value.VerificationSummary.Status != semantics.OutcomeUnknown {
		t.Fatalf("cancellation was converted into an outcome failure: run=%#v summary=%#v", trace.value.Run, trace.value.VerificationSummary)
	}
	if trace.value.Attempts[0].Status != semantics.AttemptCancelled {
		t.Fatalf("cancelled action attempt was not preserved: %#v", trace.value.Attempts[0])
	}
	if _, exists := state["continuation_required"]; exists {
		t.Fatalf("cancelled execution must not request an automatic continuation: %#v", state["continuation_required"])
	}
	assertBrowserEffectFailure(t, state, browserFailureCancelled, false, false)
}

func TestBrowserEffectTracePausesAndInvalidatesStaleSnapshot(t *testing.T) {
	trace := testBrowserSemanticTrace(t, "media.playback_state", "playing", "athena-session", 2)
	selected := browserTargetCandidate{ID: "video-2", Label: "Second video", Order: 2, Confidence: 0.98}
	plan := browserTaskPlan{
		Goal: trace.value.Outcome.Goal, Intent: "open_result", Completed: true,
		Resolution: &browserTargetResolution{
			Decision: browserResolutionExecute, Confidence: 0.98, Reason: "target_confidence_sufficient",
			Selected: &selected, Candidates: []browserTargetCandidate{selected},
		},
	}
	state := map[string]any{"session_id": "athena-session", "url": "https://example.test/watch/2"}
	err := errors.New("browser page changed from snapshot-a to snapshot-b")
	if got := trace.finish(state, &plan, err); got != nil {
		t.Fatalf("recoverable snapshot drift escaped as a terminal error: %v", got)
	}
	if trace.value.VerificationSummary.Status != semantics.OutcomeUnknown || trace.value.Run.Status != semantics.RunPaused {
		t.Fatalf("snapshot drift must pause effect verification: run=%#v summary=%#v", trace.value.Run, trace.value.VerificationSummary)
	}
	resolution := trace.value.TargetResolution
	if resolution == nil || resolution.Status != semantics.TargetResolutionReobserve || resolution.SelectedEntityRef != "" {
		t.Fatalf("stale target resolution remained executable: %#v", resolution)
	}
	if trace.value.Attempts[0].Status != semantics.AttemptFailed {
		t.Fatalf("failed stale-snapshot attempt was hidden: %#v", trace.value.Attempts[0])
	}
	assertBrowserEffectFailure(t, state, browserFailureSnapshotDrift, true, true)
}

func TestBrowserEffectTracePausesWhenTargetNeedsReobservation(t *testing.T) {
	trace := testBrowserSemanticTrace(t, "target.selection_state", true, "athena-session", 2)
	plan := browserTaskPlan{
		Goal: trace.value.Outcome.Goal, Intent: "open_result", Completed: true,
		Resolution: &browserTargetResolution{Decision: browserResolutionReobserve, Reason: "requested_ordinal_not_observed"},
	}
	state := map[string]any{"session_id": "athena-session", "url": "https://example.test/feed"}
	if err := trace.finish(state, &plan, nil); err != nil {
		t.Fatalf("recoverable target miss escaped as a terminal error: %v", err)
	}
	if trace.value.VerificationSummary.Status != semantics.OutcomeUnknown || trace.value.Run.Status != semantics.RunPaused {
		t.Fatalf("target miss must pause effect verification: run=%#v summary=%#v", trace.value.Run, trace.value.VerificationSummary)
	}
	if trace.value.VerificationResults[0].Status != semantics.VerificationUnknown {
		t.Fatalf("missing target was incorrectly treated as an unsatisfied outcome: %#v", trace.value.VerificationResults[0])
	}
	assertBrowserEffectFailure(t, state, browserFailureTargetMissing, true, true)
}

func TestBrowserEffectTraceRejectsForbiddenSideEffect(t *testing.T) {
	trace := testBrowserSemanticTrace(t, "media.playback_state", "playing", "athena-session", 2)
	plan := browserTaskPlan{Goal: trace.value.Outcome.Goal, Intent: "open_result", Completed: true}
	state := map[string]any{
		"session_id": "athena-session", "url": "https://example.test/watch/2", "closed": true,
		"playback": map[string]any{"playing": true, "verified": true},
	}
	if err := trace.finish(state, &plan, nil); err == nil {
		t.Fatal("forbidden browser side effect must fail the outcome")
	}
	if trace.value.VerificationSummary.Status != semantics.OutcomeFailed || trace.value.Run.Status != semantics.RunFailed {
		t.Fatalf("forbidden effect did not fail the run: run=%#v summary=%#v", trace.value.Run, trace.value.VerificationSummary)
	}
	assertBrowserEffectFailure(t, state, browserFailureForbiddenEffect, false, false)
}

func TestBrowserEffectTraceReportsRetryExhaustion(t *testing.T) {
	trace := testBrowserSemanticTrace(t, "media.playback_state", "playing", "athena-session", 2)
	plan := browserTaskPlan{Goal: trace.value.Outcome.Goal, Intent: "open_result", Completed: true}
	state := map[string]any{"session_id": "athena-session", "url": "https://example.test/watch/2"}
	if err := trace.finish(state, &plan, errors.New("interaction execution budget exhausted")); err == nil {
		t.Fatal("retry exhaustion must remain a terminal execution error")
	}
	if trace.value.VerificationSummary.Status != semantics.OutcomeFailed || trace.value.Attempts[0].Status != semantics.AttemptFailed {
		t.Fatalf("retry exhaustion was not reflected in the run: summary=%#v attempt=%#v", trace.value.VerificationSummary, trace.value.Attempts[0])
	}
	assertBrowserEffectFailure(t, state, browserFailureRetryExhausted, false, false)
}

func TestBrowserEffectTraceReplayIsDeterministic(t *testing.T) {
	state := map[string]any{
		"session_id": "athena-session", "url": "https://example.test/watch/2",
		"playback": map[string]any{"playing": true, "verified": true},
	}
	var signatures [][]string
	for replay := 0; replay < 2; replay++ {
		trace := testBrowserSemanticTrace(t, "media.playback_state", "playing", "athena-session", 2)
		plan := browserTaskPlan{Goal: trace.value.Outcome.Goal, Intent: "open_result", Completed: true}
		if err := trace.finish(cloneBrowserEffectState(state), &plan, nil); err != nil {
			t.Fatal(err)
		}
		signature := []string{trace.value.VerificationSummary.Status}
		for _, result := range trace.value.VerificationResults {
			signature = append(signature, result.EffectClauseID+":"+result.Status)
		}
		signatures = append(signatures, signature)
	}
	if !reflect.DeepEqual(signatures[0], signatures[1]) {
		t.Fatalf("replay changed semantic outcome: first=%v second=%v", signatures[0], signatures[1])
	}
}

func TestBrowserEffectTraceKeepsExistingSuggestionsWhenEvidenceIsUnknown(t *testing.T) {
	trace := testBrowserSemanticTrace(t, "media.playback_state", "playing", "athena-session", 2)
	plan := browserTaskPlan{Goal: trace.value.Outcome.Goal, Intent: "open_result", Completed: true}
	state := map[string]any{
		"session_id": "athena-session", "url": "https://example.test/watch/2",
		"suggested_actions": []browserSuggestedAction{{ID: "existing", Label: "Existing action"}},
	}
	if err := trace.finish(state, &plan, nil); err != nil {
		t.Fatal(err)
	}
	suggestions, ok := state["suggested_actions"].([]browserSuggestedAction)
	if !ok || len(suggestions) != 2 || suggestions[0].ID != "existing" || suggestions[1].Capability != "browser.observe" {
		t.Fatalf("effect re-observation replaced page suggestions: %#v", state["suggested_actions"])
	}
}

func TestBrowserTargetResolutionDoesNotMutateOutcomeOrPlan(t *testing.T) {
	trace := testBrowserSemanticTrace(t, "browser.page_state", "available", "athena-session", 2)
	outcomeHash, planHash := semantics.Hash(trace.value.Outcome), semantics.Hash(trace.value.Plan)
	selected := browserTargetCandidate{
		ID: "video-2", Ref: "@e2", Label: "Second video", URL: "https://example.test/watch/2",
		Kind: "video", Order: 2, Source: "semantic_page", Confidence: 0.96,
	}
	plan := browserTaskPlan{
		Goal: trace.value.Outcome.Goal, Intent: "open_result", Completed: true,
		Resolution: &browserTargetResolution{
			Decision: browserResolutionExecute, Confidence: 0.96, Reason: "target_confidence_sufficient",
			Selected: &selected, Candidates: []browserTargetCandidate{selected},
		},
	}
	state := map[string]any{"session_id": "athena-session", "url": selected.URL, "title": "Second video"}
	if err := trace.finish(state, &plan, nil); err != nil {
		t.Fatal(err)
	}
	if semantics.Hash(trace.value.Outcome) != outcomeHash || semantics.Hash(trace.value.Plan) != planHash {
		t.Fatal("dynamic target grounding mutated immutable outcome or plan definitions")
	}
	resolution := trace.value.TargetResolution
	if resolution == nil || resolution.SelectedEntityRef != "video-2" || resolution.SourceSnapshotRef == "" || resolution.WorldReadSetHash == "" || resolution.ValidUntil.IsZero() {
		t.Fatalf("dynamic target resolution is incomplete: %#v", resolution)
	}
}

func testBrowserSemanticTrace(t *testing.T, predicate string, expected any, sessionID string, ordinal int) *browserEffectTrace {
	t.Helper()
	now := time.Now().UTC()
	outcome := semantics.OutcomeSpec{
		Schema: semantics.Schema, OutcomeID: "outcome-test", Goal: "play the second video", CreatedAt: now,
		TargetSpec:     semantics.TargetSpec{TargetSpecID: "target-test", Selector: semantics.TargetSelector{Type: "current_page", Ordinal: ordinal}},
		DesiredEffects: []semantics.EffectClause{{ClauseID: "desired", Kind: semantics.EffectDesired, Subject: "target.entity", Predicate: predicate, Operator: "equals", Expected: expected, Required: true}},
		MustPreserve: []semantics.EffectClause{
			{ClauseID: "preserve", Kind: semantics.EffectMustPreserve, Subject: "browser.session", Predicate: "browser.session_identity", Operator: "equals", Expected: sessionID, Required: true},
			{ClauseID: "preserve-auth", Kind: semantics.EffectMustPreserve, Subject: "browser.authentication", Predicate: "browser.authentication_state", Operator: "equals", Expected: "unchanged", Required: true},
		},
		ForbiddenEffects: []semantics.EffectClause{{ClauseID: "forbidden", Kind: semantics.EffectForbidden, Subject: "browser.window", Predicate: "browser.window_closed", Operator: "equals", Expected: false, Required: true}},
	}
	plan := semantics.PlanCandidate{
		PlanCandidateID: "plan-test", OutcomeRef: outcome.OutcomeID, CreatedAt: now,
		Steps: []semantics.PlanStep{{StepID: "step-test", Ordinal: 1, Capability: "browser.task", Operation: "play", ExpectedEffectIDs: []string{"desired"}}},
	}
	plan.DefinitionHash = semantics.Hash(plan.Steps)
	value, err := semantics.ToMap(&semantics.SemanticTrace{Schema: semantics.Schema, Outcome: outcome, Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	trace, err := newBrowserEffectTrace(value, "action-test", sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return trace
}

func cloneBrowserEffectState(state map[string]any) map[string]any {
	result := make(map[string]any, len(state))
	for key, value := range state {
		if nested, ok := value.(map[string]any); ok {
			copyNested := make(map[string]any, len(nested))
			for nestedKey, nestedValue := range nested {
				copyNested[nestedKey] = nestedValue
			}
			result[key] = copyNested
			continue
		}
		result[key] = value
	}
	return result
}

func assertBrowserEffectFailure(t *testing.T, state map[string]any, kind string, recoverable, retryable bool) {
	t.Helper()
	failure, ok := state["failure"].(browserEffectFailure)
	if !ok {
		t.Fatalf("structured browser failure is missing: %#v", state["failure"])
	}
	if failure.Schema != browserEffectFailureSchema || failure.Kind != kind || failure.Recoverable != recoverable || failure.Retryable != retryable {
		t.Fatalf("unexpected structured browser failure: %#v", failure)
	}
}
