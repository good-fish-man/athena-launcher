package perception

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"time"
)

func (o *Orchestrator) Observe(request Request, raw map[string]any, providers Providers) map[string]any {
	if raw == nil {
		raw = make(map[string]any)
	}
	classification := classifyPage(raw)
	intent := analyzeIntent(request)
	policy := adaptivePolicy(o.budget, request, raw, classification, intent)
	semantic := buildSemantic(request, raw, policy.Budget, classification)
	uiTree := buildUITree(raw, semantic.Elements)
	patterns := recognizePatterns(uiTree, raw)
	pageModel := understandPage(raw, uiTree, patterns)
	redactSensitiveProfileTargets(&semantic, &uiTree, pageModel)
	classification = Classification{
		Type: pageModel.Type, Confidence: pageModel.Confidence,
		Signals: append([]string(nil), pageModel.Signals...),
	}
	semantic.Metadata["page_type"] = classification.Type
	intent = applyAutomaticOCRPolicy(intent, classification, semantic)
	confidence, factors := evaluateConfidence(raw, semantic, classification, intent)
	observedAt := o.now().UTC()
	incremental := o.observeIncremental(request, raw, semantic, observedAt)
	decision := decideCapture(request, raw, semantic, classification, intent, confidence, incremental)

	result := shallowClone(raw)
	result["content"] = semantic.Content
	result["snapshot"] = semantic.Snapshot
	result["key_elements"] = compatibleElements(semantic.Elements)
	result["ui_tree"] = uiTree
	result["page_patterns"] = patterns
	result["semantic_page"] = pageModel
	if page, ok := result["page"].(map[string]any); ok {
		page["type"] = classification.Type
		page["perception_confidence"] = confidence
	} else {
		result["page"] = map[string]any{
			"url": stringValue(raw["url"]), "title": stringValue(raw["title"]),
			"type": classification.Type, "perception_confidence": confidence,
		}
	}

	visual, captured, decision := buildVisual(request, raw, intent, decision, policy.Budget, providers)
	if captured != nil && result["screenshot"] == nil {
		result["screenshot"] = captured
	}

	originalContentChars := len(stringValue(raw["content"]))
	originalSnapshotChars := len(stringValue(raw["snapshot"]))
	verification := verifyAction(request, raw, classification, confidence, incremental, captured)
	recovery := o.recoveryPlan(request, verification)
	observationID := o.observationID(request, raw, observedAt)
	result["perception"] = map[string]any{
		"schema":         schemaVersion,
		"observation_id": observationID,
		"observed_at":    observedAt,
		"orchestrator": map[string]any{
			"page_classifier":        classification,
			"intent_signal_analyzer": intent,
			"confidence_evaluator": map[string]any{
				"score": confidence, "factors": factors,
			},
			"capture_policy":  decision,
			"adaptive_policy": policy,
			"observation_budget": map[string]any{
				"limits": policy.Budget,
				"used": map[string]any{
					"elements": len(semantic.Elements), "content_chars": len(semantic.Content),
					"snapshot_chars":  len(semantic.Snapshot),
					"capture_results": boolInt(captured != nil), "captures": boolInt(screenshotAvailable(captured)),
				},
			},
		},
		"semantic": map[string]any{
			"metadata": semantic.Metadata, "accessibility": semantic.Accessibility,
			"aria": semantic.ARIA, "focused_dom": semantic.FocusedDOM,
		},
		"ui_tree":       uiTree,
		"patterns":      patterns,
		"semantic_page": pageModel,
		"visual":        visual,
		"spatial":       buildSpatial(semantic.Elements),
		"incremental":   incremental,
		"verification":  verification,
		"recovery":      recovery,
		"privacy": map[string]any{
			"raw_page_forwarded": false, "raw_page_persisted": false,
			"original_content_chars": originalContentChars, "original_snapshot_chars": originalSnapshotChars,
		},
	}
	result["verification"] = verification
	return result
}

func redactSensitiveProfileTargets(semantic *semanticResult, tree *UITree, model SemanticPageModel) {
	if semantic == nil || tree == nil || !containsString(model.Signals, "profile_selection") {
		return
	}
	profileRefs := make(map[string]bool)
	for _, entity := range model.Entities {
		if entity.Kind == "profile" && entity.Ref != "" {
			profileRefs[entity.Ref] = true
		}
	}
	if len(profileRefs) == 0 {
		return
	}
	for index := range semantic.Elements {
		if profileRefs[semantic.Elements[index].Ref] {
			semantic.Elements[index].Label = redactElementTarget(semantic.Elements[index].Label)
		}
	}
	redactSemanticElementMetadata(semantic.Accessibility, "name", profileRefs)
	redactSemanticElementMetadata(semantic.ARIA, "accessible_name", profileRefs)
	snapshotLimit := len(semantic.Snapshot)
	if snapshotLimit <= 0 {
		snapshotLimit = DefaultBudget().MaxSnapshotChars
	}
	semantic.Snapshot = focusedSnapshot(semantic.Elements, snapshotLimit)
	if semantic.FocusedDOM != nil {
		semantic.FocusedDOM["interactive_snapshot"] = semantic.Snapshot
	}
	for index := range tree.Nodes {
		if !profileRefs[tree.Nodes[index].Ref] {
			continue
		}
		tree.Nodes[index].URL = ""
		if tree.Nodes[index].State == nil {
			tree.Nodes[index].State = make(map[string]any)
		}
		tree.Nodes[index].State["sensitive_target_omitted"] = true
	}
}

func redactSemanticElementMetadata(container map[string]any, field string, refs map[string]bool) {
	items, _ := container["elements"].([]map[string]any)
	for _, item := range items {
		if refs[stringValue(item["ref"])] {
			item[field] = redactElementTarget(stringValue(item[field]))
		}
	}
}

func redactElementTarget(label string) string {
	lower := strings.ToLower(label)
	urlIndex := strings.Index(lower, "url=")
	if urlIndex < 0 {
		return label
	}
	start := strings.LastIndex(label[:urlIndex], "[")
	endOffset := strings.Index(label[urlIndex:], "]")
	if start >= 0 && endOffset >= 0 {
		end := urlIndex + endOffset + 1
		return strings.TrimSpace(label[:start] + "[sensitive_target_omitted]" + label[end:])
	}
	return strings.TrimSpace(label[:urlIndex] + "sensitive_target_omitted")
}

func applyAutomaticOCRPolicy(intent IntentSignals, classification Classification, semantic semanticResult) IntentSignals {
	if intent.OCR {
		return intent
	}
	switch classification.Type {
	case "document", "image", "visual_canvas":
		if len(semantic.Content) < 200 || len(semantic.Elements) < 3 {
			intent.OCR = true
			intent.Visual = true
			intent.Keywords = append(intent.Keywords, "automatic_ocr_for_"+classification.Type)
		}
	}
	return intent
}

func evaluateConfidence(raw map[string]any, semantic semanticResult, classification Classification, intent IntentSignals) (float64, []string) {
	score := 0.2
	factors := make([]string, 0, 8)
	if strings.TrimSpace(stringValue(raw["url"])) != "" {
		score += 0.15
		factors = append(factors, "url_observed")
	}
	if strings.TrimSpace(stringValue(raw["title"])) != "" {
		score += 0.1
		factors = append(factors, "title_observed")
	}
	if len(semantic.Elements) > 0 {
		score += math.Min(0.3, float64(len(semantic.Elements))*0.025)
		factors = append(factors, "interactive_elements_observed")
	}
	if semantic.Content != "" {
		score += 0.1
		factors = append(factors, "text_content_observed")
	}
	if classification.Type != "unknown" {
		score += 0.1
		factors = append(factors, "page_classified")
	}
	if classification.Type == "challenge" {
		score -= 0.2
		factors = append(factors, "challenge_limits_automation")
	}
	if intent.Visual && countLocated(semantic.Elements) == 0 {
		score -= 0.12
		factors = append(factors, "visual_intent_without_spatial_evidence")
	}
	return roundConfidence(clamp(score, 0.05, 0.99)), factors
}

func decideCapture(request Request, raw map[string]any, semantic semanticResult, classification Classification, intent IntentSignals, confidence float64, incremental IncrementalObservation) CaptureDecision {
	decision := CaptureDecision{Scope: screenshotScope(request), Reason: "semantic_observation_sufficient"}
	if intent.ScreenshotDisabled {
		decision.Reason = "disabled_by_request"
		return decision
	}
	if intent.ExplicitScreenshot || request.Action == "screenshot" {
		decision.Capture = true
		decision.Reason = "explicit_screenshot_request"
		return decision
	}
	if classification.Type == "challenge" {
		decision.Capture = true
		decision.Reason = "challenge_requires_visual_evidence"
		return decision
	}
	if intent.Visual || intent.Spatial || intent.OCR {
		decision.Capture = true
		decision.Reason = "visual_or_spatial_intent"
		decision.Ref = stringValue(request.Arguments["ref"])
		return decision
	}
	if isChangeProducingAction(request.Action) && (!incremental.HasPrevious || !incremental.Changed) {
		decision.Capture = true
		decision.Reason = "post_action_has_no_semantic_change"
		return decision
	}
	if isInteractiveAction(request.Action) && confidence < 0.6 {
		decision.Capture = true
		decision.Reason = "low_confidence_after_interaction"
		return decision
	}
	if (classification.Type == "media_catalog" || classification.Type == "map") && len(semantic.Elements) < 3 {
		decision.Capture = true
		decision.Reason = "visual_page_has_insufficient_semantic_elements"
		return decision
	}
	if stringValue(raw["url"]) == "" && request.Action != "wait" {
		decision.Capture = true
		decision.Reason = "page_identity_missing"
	}
	return decision
}

func isChangeProducingAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "click", "play", "press", "scroll", "drag", "back", "forward", "refresh":
		return true
	default:
		return false
	}
}

func screenshotScope(request Request) string {
	scope := strings.ToLower(strings.TrimSpace(stringValue(request.Arguments["screenshot_scope"])))
	switch scope {
	case "element", "viewport", "full_page":
		return scope
	default:
		return "viewport"
	}
}

func isInteractiveAction(action string) bool {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "open", "navigate", "click", "play", "type", "hover", "select", "drag", "press", "scroll", "back", "forward", "refresh", "wait", "download":
		return true
	default:
		return false
	}
}

func (o *Orchestrator) observationID(request Request, raw map[string]any, observedAt time.Time) string {
	seed := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", request.RequestID, request.SessionID, request.Action, stringValue(raw["url"]), observedAt.Format("20060102150405.000000000"))
	sum := sha256.Sum256([]byte(seed))
	return "obs-" + hex.EncodeToString(sum[:8])
}

func shallowClone(value map[string]any) map[string]any {
	result := make(map[string]any, len(value)+1)
	for key, current := range value {
		result[key] = current
	}
	return result
}

func countLocated(elements []Element) int {
	count := 0
	for _, element := range elements {
		if element.Box != nil {
			count++
		}
	}
	return count
}

func roundConfidence(value float64) float64 {
	return math.Round(value*100) / 100
}

func clamp(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
