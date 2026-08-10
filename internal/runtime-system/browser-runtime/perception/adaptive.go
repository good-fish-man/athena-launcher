package perception

import "strings"

func adaptivePolicy(base Budget, request Request, raw map[string]any, classification Classification, intent IntentSignals) AdaptivePolicy {
	level := 1
	profile := "semantic_minimum"
	reasons := []string{"minimum_sufficient_observation"}
	action := strings.ToLower(strings.TrimSpace(request.Action))
	if isInteractiveAction(action) {
		level = 2
		profile = "interactive"
		reasons = []string{"interactive_action_requires_verification"}
	}
	if stabilization, ok := raw["stabilization"].(map[string]any); ok && !boolValue(stabilization["stable"]) {
		level = 3
		profile = "unstable_page"
		reasons = append(reasons, "page_did_not_reach_stability")
	}
	if intent.Visual || intent.Spatial || intent.OCR || classification.Type == "challenge" || classification.Type == "document" || classification.Type == "image" || classification.Type == "visual_canvas" {
		level = 4
		profile = "multimodal"
		reasons = append(reasons, "visual_or_spatial_evidence_required")
	}
	budget := budgetForLevel(base, level)
	return AdaptivePolicy{Level: level, Profile: profile, Reasons: uniqueStrings(reasons), Budget: budget, MultimodalEvidenceDue: level >= 4}
}

func budgetForLevel(base Budget, level int) Budget {
	result := base
	switch level {
	case 0:
		result.MaxElements = min(base.MaxElements, 6)
		result.MaxContentChars = min(base.MaxContentChars, 800)
		result.MaxSnapshotChars = min(base.MaxSnapshotChars, 1500)
	case 1:
		result.MaxElements = min(base.MaxElements, 12)
		result.MaxContentChars = min(base.MaxContentChars, 2000)
		result.MaxSnapshotChars = min(base.MaxSnapshotChars, 4000)
	case 2:
		result.MaxElements = min(base.MaxElements, 20)
		result.MaxContentChars = min(base.MaxContentChars, 4000)
		result.MaxSnapshotChars = min(base.MaxSnapshotChars, 8000)
	}
	return result
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}
