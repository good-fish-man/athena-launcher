package browser_runtime

import (
	"testing"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"
)

func TestBrowserTaskActionPolicyAllowsReversibleActions(t *testing.T) {
	for _, action := range []string{"navigate", "click", "play", "type", "hover", "select", "press", "scroll", "back", "forward", "refresh", "wait", "extract", "screenshot"} {
		policy := browserTaskActionPolicy(action, map[string]any{"target_label": "Search"})
		if policy.Decision != "ALLOW" || policy.Risk != "LOW" {
			t.Fatalf("%s policy = %#v", action, policy)
		}
	}
}

func TestBrowserTaskActionPolicyRequiresConfirmationForDrag(t *testing.T) {
	policy := browserTaskActionPolicy("drag", map[string]any{"target_label": "Board item"})
	if policy.Decision != "ASK_USER" || policy.Risk != "MEDIUM" {
		t.Fatalf("drag policy = %#v", policy)
	}
}

func TestBrowserTaskActionPolicyRequiresConfirmationForSensitiveActions(t *testing.T) {
	tests := []struct {
		action string
		args   map[string]any
	}{
		{action: "click", args: map[string]any{"target_label": "Place order"}},
		{action: "type", args: map[string]any{"field_label": "Password"}},
		{action: "download", args: map[string]any{}},
	}
	for _, test := range tests {
		policy := browserTaskActionPolicy(test.action, test.args)
		if policy.Decision != "ASK_USER" {
			t.Fatalf("%s policy = %#v", test.action, policy)
		}
	}
}

func TestBrowserInteractionVerifiedUsesPerceptionVerification(t *testing.T) {
	verification := &perception.ActionVerification{Action: "click", Status: "verified", Reason: "post_action_state_changed"}
	if !browserInteractionVerified("click", nil, nil, verification) {
		t.Fatal("verified perception result was ignored")
	}
	verification.Status = "uncertain"
	if browserInteractionVerified("click", nil, nil, verification) {
		t.Fatal("uncertain click was accepted")
	}
	verification.Status = "observed"
	state := map[string]any{"playback": map[string]any{"playing": true, "verified": true}}
	if !browserInteractionVerified("play", nil, state, verification) {
		t.Fatal("an observed action did not fall through to its verified postcondition")
	}
}

func TestBrowserInteractionPostconditionVerifiesNavigationAndPlayback(t *testing.T) {
	if !browserInteractionPostcondition("navigate", map[string]any{"url": "https://example.com/page"}, map[string]any{"url": "https://example.com/page"}) {
		t.Fatal("navigation target was not verified")
	}
	state := map[string]any{"playback": map[string]any{"playing": true, "verified": true}}
	if !browserInteractionPostcondition("play", nil, state) {
		t.Fatal("verified playback was not accepted")
	}
}

func TestBrowserInteractionDoesNotRetrySideEffectfulActions(t *testing.T) {
	allow := browserActionPolicyDecision{Risk: "LOW", Decision: "ALLOW"}
	for _, action := range []string{"type", "select", "press", "download", "upload", "drag"} {
		if browserInteractionRetryable(action, nil, allow) {
			t.Fatalf("side-effectful action %q was marked retryable", action)
		}
	}
	if browserInteractionRetryable("click", map[string]any{"target_label": "Buy now"}, allow) {
		t.Fatal("an ungrounded click was marked retryable")
	}
	if !browserInteractionRetryable("navigate", nil, allow) {
		t.Fatal("reversible navigation was not retryable")
	}
}

func TestBrowserInteractionRetryArgumentsRebindsOnlyResolvedTargetPage(t *testing.T) {
	original := map[string]any{
		"target_url":        "https://video.example/watch/second",
		"expected_page_url": "https://video.example/home",
	}
	rebound := browserInteractionRetryArguments("play", original, map[string]any{
		"url": "https://video.example/watch/second/",
	})
	if got := browserStringValue(rebound["expected_page_url"]); got != "https://video.example/watch/second/" {
		t.Fatalf("retry precondition = %q", got)
	}
	if got := browserStringValue(original["expected_page_url"]); got != "https://video.example/home" {
		t.Fatalf("original arguments were mutated: %q", got)
	}

	unrelated := browserInteractionRetryArguments("play", original, map[string]any{
		"url": "https://video.example/watch/wrong",
	})
	if got := browserStringValue(unrelated["expected_page_url"]); got != "https://video.example/home" {
		t.Fatalf("unrelated page bypassed the original precondition: %q", got)
	}
}
