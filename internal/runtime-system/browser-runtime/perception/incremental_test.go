package perception

import (
	"testing"
	"time"
)

func TestIncrementalPerceptionCreatesSessionSequence(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	orchestrator.now = func() time.Time {
		now = now.Add(time.Second)
		return now
	}
	first := orchestrator.Observe(Request{
		SessionID: "session-one", Action: "navigate", Arguments: map[string]any{"url": "https://example.com"},
	}, perceptionPage("https://example.com", "Example", "First page"), Providers{})
	second := orchestrator.Observe(Request{
		SessionID: "session-one", Action: "observe",
	}, perceptionPage("https://example.com", "Example", "First page"), Providers{})

	firstDelta := perceptionIncremental(t, first)
	secondDelta := perceptionIncremental(t, second)
	if firstDelta.Sequence != 1 || !firstDelta.BaselineCreated || firstDelta.HasPrevious {
		t.Fatalf("unexpected first baseline: %#v", firstDelta)
	}
	if secondDelta.Sequence != 2 || !secondDelta.HasPrevious || secondDelta.Changed || secondDelta.Stability != "stable" {
		t.Fatalf("unexpected second observation: %#v", secondDelta)
	}
	verification := perceptionVerification(t, first)
	if verification.Status != "verified" || verification.Reason != "navigation_target_observed" {
		t.Fatalf("navigation was not verified: %#v", verification)
	}
}

func TestUnchangedClickCapturesVisualEvidenceAndStaysUncertain(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	page := perceptionPage("https://example.com", "Example", "No change")
	orchestrator.Observe(Request{SessionID: "session-click", Action: "observe"}, page, Providers{})
	captures := 0
	result := orchestrator.Observe(Request{
		SessionID: "session-click", Action: "click", Arguments: map[string]any{"ref": "@e1"},
	}, page, Providers{Capture: func(request CaptureRequest) map[string]any {
		captures++
		if request.Reason != "post_action_has_no_semantic_change" {
			t.Fatalf("unexpected capture reason: %#v", request)
		}
		return map[string]any{"available": true, "path": "/tmp/click.png", "scope": "viewport"}
	}})

	delta := perceptionIncremental(t, result)
	verification := perceptionVerification(t, result)
	if captures != 1 || delta.Changed || verification.Status != "uncertain" || verification.RetryAfterMS != 500 {
		t.Fatalf("unchanged click was not handled safely: captures=%d delta=%#v verification=%#v", captures, delta, verification)
	}
	if len(verification.Evidence) != 1 || verification.Evidence[0] != "visual_evidence_captured" {
		t.Fatalf("visual evidence was not linked to verification: %#v", verification)
	}
}

func TestChangedClickIsVerifiedWithoutUnnecessaryCapture(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	orchestrator.Observe(Request{SessionID: "session-change", Action: "observe"},
		perceptionPage("https://example.com", "Before", "Before content"), Providers{})
	captures := 0
	result := orchestrator.Observe(Request{
		SessionID: "session-change", Action: "click", Arguments: map[string]any{"ref": "@e1"},
	}, perceptionPage("https://example.com/next", "After", "After content"), Providers{
		Capture: func(CaptureRequest) map[string]any {
			captures++
			return nil
		},
	})

	delta := perceptionIncremental(t, result)
	verification := perceptionVerification(t, result)
	if !delta.Changed || !delta.URLChanged || !delta.TitleChanged || verification.Status != "verified" {
		t.Fatalf("changed click was not verified: delta=%#v verification=%#v", delta, verification)
	}
	if captures != 0 {
		t.Fatalf("verified semantic change triggered %d unnecessary capture(s)", captures)
	}
}

func TestPointerClickUsesPostActionScreenshotForVisualVerification(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	page := perceptionPage("https://example.com/canvas", "Canvas", "Unchanged semantic content")
	orchestrator.Observe(Request{SessionID: "session-pointer", Action: "screenshot"}, page, Providers{})
	result := orchestrator.Observe(Request{
		SessionID: "session-pointer", Action: "pointer", Arguments: map[string]any{
			"pointer_executed": true, "pointer_operation": "click", "pointer_before_sha256": "before",
			"screenshot": true,
		},
	}, page, Providers{Capture: func(CaptureRequest) map[string]any {
		return map[string]any{
			"available": true, "scope": "viewport",
			"artifact": map[string]any{"sha256": "after"},
		}
	}})
	verification := perceptionVerification(t, result)
	if verification.Status != "verified" || verification.Reason != "pointer_action_changed_visual_state" {
		t.Fatalf("pointer visual verification = %#v", verification)
	}
}

func TestPointerClickFailsClosedWhenNothingChanges(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	page := perceptionPage("https://example.com/canvas", "Canvas", "Unchanged semantic content")
	orchestrator.Observe(Request{SessionID: "session-pointer-stable", Action: "screenshot"}, page, Providers{})
	result := orchestrator.Observe(Request{
		SessionID: "session-pointer-stable", Action: "pointer", Arguments: map[string]any{
			"pointer_executed": true, "pointer_operation": "click", "pointer_before_sha256": "same",
			"screenshot": true,
		},
	}, page, Providers{Capture: func(CaptureRequest) map[string]any {
		return map[string]any{
			"available": true, "scope": "viewport",
			"artifact": map[string]any{"sha256": "same"},
		}
	}})
	verification := perceptionVerification(t, result)
	if verification.Status != "uncertain" || verification.Reason != "pointer_action_produced_no_observable_change" {
		t.Fatalf("unchanged pointer verification = %#v", verification)
	}
}

func TestPointerMoveIsObservedWithoutClaimingPageMutation(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	result := orchestrator.Observe(Request{
		SessionID: "session-pointer-move", Action: "pointer", Arguments: map[string]any{
			"pointer_executed": true, "pointer_operation": "move", "screenshot": true,
		},
	}, perceptionPage("https://example.com/canvas", "Canvas", "Canvas page"), Providers{})
	verification := perceptionVerification(t, result)
	if verification.Status != "observed" || verification.Reason != "pointer_move_dispatched_and_page_observed" {
		t.Fatalf("pointer move verification = %#v", verification)
	}
}

func TestChallengeBlocksActionVerification(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	result := orchestrator.Observe(Request{SessionID: "session-blocked", Action: "navigate"}, map[string]any{
		"url": "https://www.google.com/sorry", "title": "Unusual traffic",
		"content": "Verify you are human", "challenge_detected": true,
	}, Providers{})
	verification := perceptionVerification(t, result)
	if verification.Status != "blocked" || verification.RecommendedNext != "request_user_takeover" {
		t.Fatalf("challenge did not block verification: %#v", verification)
	}
}

func TestErrorPageFailsNavigationVerification(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	result := orchestrator.Observe(Request{
		SessionID: "session-not-found", Action: "navigate", Arguments: map[string]any{"url": "https://forum.example/u/missing"},
	}, perceptionPage("https://forum.example/u/missing", "Page Not Found - Example Forum", "The requested page could not be found"), Providers{})
	verification := perceptionVerification(t, result)
	if verification.Status != "failed" || verification.Reason != "navigation_reached_error_page" {
		t.Fatalf("error page navigation was not rejected: %#v", verification)
	}
	model, ok := SemanticPage(result)
	if !ok || model.Type != "error_page" {
		t.Fatalf("error page semantic type = %#v, ok=%v", model, ok)
	}
}

func TestPlaybackAndPauseUseObservedMediaState(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	playing := perceptionPage("https://video.example/watch/second", "Second video", "Second video")
	playing["playback"] = map[string]any{"found": true, "playing": true, "paused": false, "verified": true}
	playResult := orchestrator.Observe(Request{SessionID: "session-media", Action: "play"}, playing, Providers{})
	playVerification := perceptionVerification(t, playResult)
	if playVerification.Status != "verified" || playVerification.Reason != "media_playback_observed" {
		t.Fatalf("playback verification = %#v", playVerification)
	}

	paused := perceptionPage("https://video.example/watch/second", "Second video", "Second video")
	paused["playback"] = map[string]any{"found": true, "playing": false, "paused": true, "verified": true}
	pauseResult := orchestrator.Observe(Request{SessionID: "session-media", Action: "pause"}, paused, Providers{})
	pauseVerification := perceptionVerification(t, pauseResult)
	if pauseVerification.Status != "verified" || pauseVerification.Reason != "media_pause_observed" {
		t.Fatalf("pause verification = %#v", pauseVerification)
	}
}

func TestClearSessionDropsIncrementalBaseline(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	request := Request{SessionID: "session-clear", Action: "observe"}
	orchestrator.Observe(request, perceptionPage("https://example.com", "One", "One"), Providers{})
	orchestrator.ClearSession(request.SessionID)
	result := orchestrator.Observe(request, perceptionPage("https://example.com", "Two", "Two"), Providers{})
	delta := perceptionIncremental(t, result)
	if delta.Sequence != 1 || !delta.BaselineCreated || delta.HasPrevious {
		t.Fatalf("cleared session retained baseline: %#v", delta)
	}
}

func TestRepeatedUnchangedActionOpensRecoveryCircuit(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	page := perceptionPage("https://example.com", "Example", "unchanged")
	orchestrator.Observe(Request{SessionID: "session-recovery", Action: "observe"}, page, Providers{})
	var result map[string]any
	for i := 0; i < 3; i++ {
		result = orchestrator.Observe(Request{
			SessionID: "session-recovery", Action: "click", Arguments: map[string]any{"ref": "@e1"},
		}, page, Providers{})
	}
	metadata := result["perception"].(map[string]any)
	recovery, ok := metadata["recovery"].(RecoveryPlan)
	if !ok || !recovery.CircuitOpen || recovery.Strategy != "stop_repeating_action" {
		t.Fatalf("recovery = %#v", metadata["recovery"])
	}
}

func perceptionPage(url, title, content string) map[string]any {
	return map[string]any{
		"url": url, "title": title, "content": content,
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": "button Continue"},
			{"ref": "@e2", "label": "link Details"},
			{"ref": "@e3", "label": "textbox Search"},
			{"ref": "@e4", "label": "button Submit"},
		},
	}
}

func perceptionIncremental(t *testing.T, result map[string]any) IncrementalObservation {
	t.Helper()
	metadata := result["perception"].(map[string]any)
	delta, ok := metadata["incremental"].(IncrementalObservation)
	if !ok {
		t.Fatalf("incremental observation missing: %#v", metadata)
	}
	return delta
}

func perceptionVerification(t *testing.T, result map[string]any) ActionVerification {
	t.Helper()
	verification, ok := result["verification"].(ActionVerification)
	if !ok {
		t.Fatalf("action verification missing: %#v", result)
	}
	return verification
}
