package perception

import "strings"

func buildVisual(
	request Request,
	raw map[string]any,
	intent IntentSignals,
	decision CaptureDecision,
	budget Budget,
	providers Providers,
) (map[string]any, map[string]any, CaptureDecision) {
	visual := map[string]any{
		"capture_policy": decision,
		"providers": map[string]any{
			"viewport_screenshot":  true,
			"element_screenshot":   true,
			"full_page_screenshot": true,
			"annotated_screenshot": true,
			"ocr":                  providers.OCR != nil,
		},
		"viewport_screenshot":  unavailableEvidence("not_requested"),
		"element_screenshot":   unavailableEvidence("not_requested"),
		"full_page_screenshot": unavailableEvidence("not_requested"),
		"ocr":                  map[string]any{"requested": intent.OCR, "available": false, "reason": "not_requested"},
	}

	captured, _ := raw["screenshot"].(map[string]any)
	if captured != nil {
		decision.Capture = false
		decision.Reason = "screenshot_already_available"
	} else if decision.Capture && providers.Capture != nil && budget.MaxCaptures > 0 {
		captured = providers.Capture(CaptureRequest{
			Scope: decision.Scope, Reason: decision.Reason, Ref: decision.Ref,
			Annotate: shouldAnnotateCapture(intent, decision),
		})
	}
	if captured != nil {
		setCapturedEvidence(visual, captured, decision.Scope)
	}
	visual["capture_policy"] = decision

	if intent.OCR {
		visual["ocr"] = runOCR(request, captured, budget, providers.OCR)
	}
	return visual, captured, decision
}

func unavailableEvidence(reason string) map[string]any {
	return map[string]any{"available": false, "reason": reason}
}

func setCapturedEvidence(visual map[string]any, captured map[string]any, requestedScope string) {
	actualScope := strings.TrimSpace(stringValue(captured["scope"]))
	if actualScope == "" {
		actualScope = requestedScope
	}
	switch actualScope {
	case "element":
		visual["element_screenshot"] = captured
	case "full_page":
		visual["full_page_screenshot"] = captured
	default:
		visual["viewport_screenshot"] = captured
	}
}

func shouldAnnotateCapture(intent IntentSignals, decision CaptureDecision) bool {
	if decision.Scope == "element" || intent.OCR {
		return false
	}
	return intent.Visual || intent.Spatial
}

func runOCR(request Request, screenshot map[string]any, budget Budget, provider OCRFunc) map[string]any {
	if provider == nil {
		return map[string]any{"requested": true, "available": false, "reason": "ocr_provider_not_available"}
	}
	path := stringValue(screenshot["path"])
	if path == "" {
		return map[string]any{"requested": true, "available": false, "reason": "screenshot_not_available"}
	}
	result := provider(OCRRequest{
		Path: path, Language: stringValue(request.Arguments["ocr_language"]), MaxChars: budget.MaxOCRChars,
	})
	if result == nil {
		return map[string]any{"requested": true, "available": false, "reason": "ocr_provider_returned_no_result"}
	}
	result["requested"] = true
	return result
}

// NeedsSpatialEvidence lets the local browser adapter decide whether boxes are
// useful before the final Observation is built. This decision never calls an LLM.
func NeedsSpatialEvidence(request Request, raw map[string]any) bool {
	intent := analyzeIntent(request)
	if intent.Visual || intent.Spatial {
		return true
	}
	if stringValue(request.Arguments["ref"]) != "" {
		return true
	}
	classification := classifyPage(raw)
	return classification.Type == "map"
}
