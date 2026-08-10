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
	if browserInteractionRetryable("type", nil, allow) || browserInteractionRetryable("download", nil, allow) {
		t.Fatal("side-effectful action was marked retryable")
	}
	if !browserInteractionRetryable("navigate", nil, allow) {
		t.Fatal("reversible navigation was not retryable")
	}
}
