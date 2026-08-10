package browser_runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

type browserStabilityPolicy struct {
	MaxWait  time.Duration
	Interval time.Duration
	Probes   int
}

func stabilizeBrowserObservation(
	ctx context.Context,
	request browserExecuteRequest,
	initial map[string]any,
	observe func() (map[string]any, error),
) map[string]any {
	policy, enabled := browserActionStabilityPolicy(request)
	if !enabled || observe == nil {
		return initial
	}
	started := time.Now()
	current := initial
	previousFingerprint := browserObservationFingerprint(initial)
	stable := false
	attempts := 0
	var probeError string
	for attempts < policy.Probes && time.Since(started) < policy.MaxWait {
		if policy.Interval > 0 {
			timer := time.NewTimer(policy.Interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				probeError = ctx.Err().Error()
				attempts = policy.Probes
				continue
			case <-timer.C:
			}
		}
		probe, err := observe()
		attempts++
		if err != nil {
			probeError = err.Error()
			break
		}
		current = probe
		fingerprint := browserObservationFingerprint(probe)
		if fingerprint == previousFingerprint {
			stable = true
			break
		}
		previousFingerprint = fingerprint
	}
	if current == nil {
		current = make(map[string]any)
	}
	reason := "page_state_stable"
	if !stable {
		reason = "stability_window_exhausted"
	}
	if probeError != "" {
		reason = "stability_probe_failed"
	}
	current["stabilization"] = map[string]any{
		"stable": stable, "attempts": attempts, "elapsed_ms": time.Since(started).Milliseconds(),
		"reason": reason, "error": probeError,
	}
	return current
}

func browserActionStabilityPolicy(request browserExecuteRequest) (browserStabilityPolicy, bool) {
	if disabled, ok := request.Arguments["stabilize"].(bool); ok && !disabled {
		return browserStabilityPolicy{}, false
	}
	switch strings.ToLower(strings.TrimSpace(request.Action)) {
	case "navigate", "open":
		return browserStabilityPolicy{MaxWait: 2500 * time.Millisecond, Interval: 250 * time.Millisecond, Probes: 4}, true
	case "click", "press":
		return browserStabilityPolicy{MaxWait: 1800 * time.Millisecond, Interval: 200 * time.Millisecond, Probes: 4}, true
	case "type", "scroll":
		return browserStabilityPolicy{MaxWait: time.Second, Interval: 150 * time.Millisecond, Probes: 3}, true
	default:
		return browserStabilityPolicy{}, false
	}
}

func browserObservationFingerprint(observation map[string]any) string {
	selected := map[string]any{
		"url": observation["url"], "title": observation["title"],
		"content": observation["content"], "snapshot": observation["snapshot"],
		"key_elements": observation["key_elements"],
	}
	encoded, _ := json.Marshal(selected)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:8])
}
