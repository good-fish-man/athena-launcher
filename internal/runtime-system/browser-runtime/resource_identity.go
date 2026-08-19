package browser_runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"
)

const browserResourceSchema = "athena.browser.resource.v1"

// attachBrowserResourceIdentity turns a perception result into an optimistic
// concurrency token. The control plane uses this token for critical rechecks
// and action-scoped single-writer leases; the browser executor remains the
// authority for the identity of the active tab.
func attachBrowserResourceIdentity(request browserExecuteRequest, observation map[string]any) map[string]any {
	if observation == nil {
		observation = make(map[string]any)
	}
	sessionID := firstBrowserResourceString(observation["session_id"], request.SessionID, "unknown")
	tabID := firstBrowserResourceString(observation["tab_id"], nestedBrowserResourceString(observation, "browser_runtime", "tab", "id"), "tab-main")
	resourceRef := "browser://session/" + sessionID + "/tab/" + tabID
	version := browserPerceptionFingerprint(observation)
	if version == "" {
		version = browserResourceFallbackVersion(observation, sessionID, tabID)
	}
	identity := map[string]any{
		"schema": browserResourceSchema, "resource_ref": resourceRef,
		"resource_version": version, "session_id": sessionID, "tab_id": tabID,
	}
	observation["resource_ref"] = resourceRef
	observation["resource_version"] = version
	observation["resource_identity"] = identity
	return observation
}

func browserPerceptionFingerprint(observation map[string]any) string {
	perceptionValue, _ := observation["perception"].(map[string]any)
	if perceptionValue == nil {
		return ""
	}
	switch incremental := perceptionValue["incremental"].(type) {
	case perception.IncrementalObservation:
		return strings.TrimSpace(incremental.CurrentFingerprint)
	case *perception.IncrementalObservation:
		if incremental != nil {
			return strings.TrimSpace(incremental.CurrentFingerprint)
		}
	case map[string]any:
		return firstBrowserResourceString(incremental["current_fingerprint"])
	}
	return ""
}

func browserResourceFallbackVersion(observation map[string]any, sessionID, tabID string) string {
	payload := struct {
		SessionID string `json:"session_id"`
		TabID     string `json:"tab_id"`
		URL       any    `json:"url"`
		Title     any    `json:"title"`
		Playback  any    `json:"playback,omitempty"`
		Closed    any    `json:"closed,omitempty"`
	}{SessionID: sessionID, TabID: tabID, URL: observation["url"], Title: observation["title"], Playback: observation["playback"], Closed: observation["closed"]}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func nestedBrowserResourceString(value map[string]any, path ...string) string {
	var current any = value
	for _, segment := range path {
		object, _ := current.(map[string]any)
		if object == nil {
			return ""
		}
		current = object[segment]
	}
	return firstBrowserResourceString(current)
}

func firstBrowserResourceString(values ...any) string {
	for _, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}
