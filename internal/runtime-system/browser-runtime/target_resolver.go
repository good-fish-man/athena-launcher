package browser_runtime

import (
	"math"
	"sort"
	"strings"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"
)

const browserTargetResolutionSchema = "athena.browser.target-resolution.v3"

type browserResolutionDecision string

const (
	browserResolutionExecute   browserResolutionDecision = "execute"
	browserResolutionReobserve browserResolutionDecision = "reobserve"
	browserResolutionAskUser   browserResolutionDecision = "ask_user"
	browserResolutionBlock     browserResolutionDecision = "block"
)

type browserTargetEvidence struct {
	Semantic float64 `json:"semantic"`
	Kind     float64 `json:"kind"`
	Ordinal  float64 `json:"ordinal"`
	Source   float64 `json:"source"`
	Page     float64 `json:"page"`
	Visual   float64 `json:"visual,omitempty"`
	Spatial  float64 `json:"spatial,omitempty"`
}

type browserTargetCandidate struct {
	ID         string                  `json:"id,omitempty"`
	Label      string                  `json:"label"`
	URL        string                  `json:"url,omitempty"`
	Ref        string                  `json:"ref,omitempty"`
	Role       string                  `json:"role,omitempty"`
	Kind       string                  `json:"kind,omitempty"`
	Position   int                     `json:"position,omitempty"`
	Order      int                     `json:"order,omitempty"`
	Playable   bool                    `json:"playable,omitempty"`
	Box        *perception.BoundingBox `json:"bounding_box,omitempty"`
	Source     string                  `json:"source"`
	Confidence float64                 `json:"confidence"`
	Evidence   browserTargetEvidence   `json:"evidence"`
}

type browserTargetResolution struct {
	Schema           string                    `json:"schema"`
	Decision         browserResolutionDecision `json:"decision"`
	Risk             string                    `json:"risk"`
	Threshold        float64                   `json:"threshold"`
	ObserveThreshold float64                   `json:"observe_threshold"`
	Confidence       float64                   `json:"confidence"`
	Reason           string                    `json:"reason"`
	RequestedOrdinal int                       `json:"requested_ordinal"`
	RequiresVisual   bool                      `json:"requires_visual"`
	RequiresSpatial  bool                      `json:"requires_spatial"`
	SpatialRelation  *browserSpatialRelation   `json:"spatial_relation,omitempty"`
	Selected         *browserTargetCandidate   `json:"selected,omitempty"`
	Candidates       []browserTargetCandidate  `json:"candidates,omitempty"`
	FallbackUsed     bool                      `json:"fallback_used,omitempty"`
}

type browserCandidateResolver struct{}

type browserTargetResolver struct {
	candidates browserCandidateResolver
}

func newBrowserTargetResolver() *browserTargetResolver {
	return &browserTargetResolver{}
}

func (r *browserTargetResolver) Resolve(state map[string]any, task inferredBrowserTask, ordinal int, risk string) browserTargetResolution {
	if ordinal <= 0 {
		ordinal = 1
	}
	result := browserTargetResolution{
		Schema: browserTargetResolutionSchema, Risk: normalizeBrowserRisk(risk), RequestedOrdinal: ordinal,
	}
	result.Threshold, result.ObserveThreshold = browserResolutionThresholds(result.Risk)
	result.RequiresVisual = browserTaskRequiresVisualGrounding(task)
	result.RequiresSpatial = browserTaskRequiresSpatialGrounding(task)
	result.SpatialRelation = resolveBrowserSpatialRelation(state, task)
	result.Candidates = r.candidates.Resolve(state, task)
	if result.SpatialRelation != nil && !result.SpatialRelation.Resolved {
		result.Decision, result.Reason = browserResolutionReobserve, "spatial_anchor_not_observed"
		return result
	}

	eligible := make([]browserTargetCandidate, 0, len(result.Candidates))
	for _, candidate := range result.Candidates {
		if browserCandidateEligible(candidate, task) {
			eligible = append(eligible, candidate)
		}
	}
	if len(eligible) == 0 {
		result.Decision, result.Reason = browserResolutionReobserve, "no_eligible_candidates"
		if result.SpatialRelation != nil {
			result.Reason = "spatial_relation_not_satisfied"
		}
		return result
	}

	orderBrowserCandidates(eligible, task, result.SpatialRelation)
	if ordinal > len(eligible) {
		result.Decision, result.Reason = browserResolutionReobserve, "requested_ordinal_not_observed"
		return result
	}
	selected := eligible[ordinal-1]
	selected.Evidence.Ordinal = browserOrdinalEvidence(ordinal, len(eligible), selected)
	selected.Confidence = browserCandidateConfidence(selected.Evidence, result.RequiresVisual)
	result.Confidence = selected.Confidence
	result.Selected = &selected

	if result.RequiresVisual && selected.Evidence.Visual < 0.55 {
		result.Decision, result.Reason = browserResolutionReobserve, "visual_evidence_required"
		return result
	}
	if result.RequiresSpatial && selected.Evidence.Spatial < 0.65 {
		result.Decision, result.Reason = browserResolutionReobserve, "spatial_evidence_required"
		return result
	}
	if selected.Confidence >= result.Threshold {
		result.Decision, result.Reason = browserResolutionExecute, "target_confidence_sufficient"
		return result
	}
	if selected.Confidence >= result.ObserveThreshold {
		result.Decision, result.Reason = browserResolutionReobserve, "target_needs_additional_evidence"
		return result
	}
	if result.Risk == "HIGH" {
		result.Decision, result.Reason = browserResolutionBlock, "high_risk_target_confidence_too_low"
		return result
	}
	result.Decision, result.Reason = browserResolutionAskUser, "target_is_ambiguous"
	return result
}

func (browserCandidateResolver) Resolve(state map[string]any, task inferredBrowserTask) []browserTargetCandidate {
	page, hasPage := perception.SemanticPage(state)
	pageConfidence := 0.62
	if hasPage && page.Confidence > 0 {
		pageConfidence = page.Confidence
	}
	result := make([]browserTargetCandidate, 0, 32)
	seen := make(map[string]int)
	semanticQuery := browserTaskSemanticQuery(task)
	spatialRelation := resolveBrowserSpatialRelation(state, task)
	appendCandidate := func(candidate browserTargetCandidate) {
		candidate.Label = strings.TrimSpace(candidate.Label)
		candidate.URL = strings.TrimSpace(candidate.URL)
		candidate.Ref = strings.TrimSpace(candidate.Ref)
		candidate.Role = strings.ToLower(strings.TrimSpace(candidate.Role))
		candidate.Kind = strings.ToLower(strings.TrimSpace(candidate.Kind))
		if candidate.Label == "" || (candidate.URL == "" && candidate.Ref == "") {
			return
		}
		if isLowValueBrowserTarget(candidate.Label, candidate.URL) {
			return
		}
		candidate.Evidence.Semantic = browserCandidateSemanticEvidence(semanticQuery, candidate)
		candidate.Evidence.Kind = browserKindEvidence(task, candidate)
		candidate.Evidence.Source = browserCandidateSourceEvidence(candidate.Source)
		candidate.Evidence.Page = clampBrowserScore(pageConfidence)
		candidate.Box = browserCandidateBox(state, candidate.Ref)
		candidate.Evidence.Visual = browserVisualEvidenceScore(state, candidate, task)
		candidate.Evidence.Spatial = browserSpatialEvidenceScore(candidate, task, spatialRelation)
		candidate.Confidence = browserCandidateConfidence(candidate.Evidence, false)
		key := browserCandidateIdentity(candidate)
		if index, ok := seen[key]; ok {
			if candidate.Confidence > result[index].Confidence || (result[index].Ref == "" && candidate.Ref != "") {
				result[index] = candidate
			}
			return
		}
		seen[key] = len(result)
		result = append(result, candidate)
	}

	if hasPage {
		for _, entity := range page.Entities {
			source := "semantic_page"
			if value, ok := entity.Attributes["source"].(string); ok && strings.TrimSpace(value) != "" {
				source = strings.TrimSpace(value)
			}
			appendCandidate(browserTargetCandidate{
				ID: entity.ID, Label: entity.Label, URL: entity.URL, Ref: entity.Ref,
				Role: strings.ToLower(browserStringValue(entity.Attributes["role"])), Kind: entity.Kind,
				Position: entity.Position, Order: entity.Position, Playable: entity.Playable,
				Source: source,
			})
		}
	}
	for _, candidate := range browserMediaCandidates(state) {
		appendCandidate(browserTargetCandidate{
			ID: candidate.ID, Label: candidate.Title, URL: candidate.URL, Ref: candidate.Ref, Kind: candidate.Kind,
			Role:     "link",
			Position: candidate.Position, Order: candidate.Order, Playable: true, Source: "observed_media",
		})
	}
	for _, value := range browserContentCandidateValues(state) {
		kind := strings.ToLower(strings.TrimSpace(browserStringValue(value["kind"])))
		appendCandidate(browserTargetCandidate{
			ID: browserStringValue(value["id"]), Label: browserStringValue(value["title"]),
			URL: browserStringValue(value["url"]), Ref: browserStringValue(value["ref"]),
			Role: strings.ToLower(browserStringValue(value["role"])), Kind: kind,
			Position: int(int64Argument(value["position"])), Order: int(int64Argument(value["order"])),
			Playable: kind == "video" || kind == "audio", Source: "structured_content",
		})
	}
	for index, element := range browserSemanticElements(state) {
		if !isResolvableBrowserElement(element) {
			continue
		}
		resolvedURL := resolveBrowserElementURL(browserStringValue(state["url"]), element.URL)
		kind := element.Kind
		if kind == "" {
			kind, _ = browserMediaIdentity(resolvedURL)
		}
		appendCandidate(browserTargetCandidate{
			Label: browserElementTitle(element), URL: resolvedURL, Ref: element.Ref, Role: element.Role, Kind: kind,
			Position: index + 1, Order: index + 1, Playable: kind == "video" || kind == "audio", Source: "key_element",
		})
	}
	return result
}

func isResolvableBrowserElement(element browserSemanticElement) bool {
	if element.Ref == "" || strings.TrimSpace(element.Label) == "" {
		return false
	}
	for _, role := range []string{"link", "button", "option", "menuitem", "tab", "radio", "checkbox"} {
		if element.Role == role || strings.Contains(strings.ToLower(element.Label), role) {
			return true
		}
	}
	return false
}

func browserContentCandidateValues(state map[string]any) []map[string]any {
	if values, ok := state["content_candidates"].([]map[string]any); ok {
		return values
	}
	if values, ok := state["content_candidates"].([]any); ok {
		return browserMediaCandidateMaps(values)
	}
	return nil
}

func browserCandidateEligible(candidate browserTargetCandidate, task inferredBrowserTask) bool {
	if browserTaskRequiresSpatialGrounding(task) && candidate.Evidence.Spatial < 0.65 {
		return false
	}
	if task.PlayResult && !candidate.Playable && candidate.Kind != "video" && candidate.Kind != "audio" {
		return false
	}
	if len(task.PreferredRoles) > 0 {
		matched := false
		for _, role := range task.PreferredRoles {
			if strings.EqualFold(strings.TrimSpace(role), candidate.Role) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if len(task.PreferredKinds) > 0 {
		matched := false
		for _, kind := range task.PreferredKinds {
			if strings.EqualFold(strings.TrimSpace(kind), candidate.Kind) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}
	if strings.TrimSpace(task.Query) != "" && candidate.Evidence.Semantic < 0.55 {
		return false
	}
	return true
}

func orderBrowserCandidates(candidates []browserTargetCandidate, task inferredBrowserTask, relation *browserSpatialRelation) {
	spatial := browserTaskSpatialIntent(task)
	sort.SliceStable(candidates, func(left, right int) bool {
		if spatial != "" {
			leftValue, leftOK := browserSpatialOrderValue(candidates[left], spatial, relation)
			rightValue, rightOK := browserSpatialOrderValue(candidates[right], spatial, relation)
			if leftOK != rightOK {
				return leftOK
			}
			if leftOK && leftValue != rightValue {
				return leftValue < rightValue
			}
		}
		leftPosition, rightPosition := candidates[left].Position, candidates[right].Position
		if leftPosition > 0 || rightPosition > 0 {
			if leftPosition == 0 {
				return false
			}
			if rightPosition == 0 {
				return true
			}
			if leftPosition != rightPosition {
				return leftPosition < rightPosition
			}
		}
		if candidates[left].Order != candidates[right].Order {
			return candidates[left].Order < candidates[right].Order
		}
		if candidates[left].Confidence != candidates[right].Confidence {
			return candidates[left].Confidence > candidates[right].Confidence
		}
		return candidates[left].Label < candidates[right].Label
	})
}

func browserCandidateSemanticEvidence(query string, candidate browserTargetCandidate) float64 {
	if query == "" {
		return 0.82
	}
	text := strings.Join([]string{candidate.Label, candidate.Role, candidate.Kind}, " ")
	return browserSemanticEvidence(query, browserSpatialSemanticText(text))
}

func browserSemanticEvidence(query, label string) float64 {
	query = normalizeCurrentPageSelection(query)
	label = normalizeCurrentPageSelection(label)
	if query == "" {
		return 0.82
	}
	score := currentPageSelectionScore(query, label)
	if score <= 0 {
		return 0
	}
	return clampBrowserScore(float64(score) / 100)
}

func browserKindEvidence(task inferredBrowserTask, candidate browserTargetCandidate) float64 {
	if len(task.PreferredRoles) > 0 {
		matched := false
		for _, role := range task.PreferredRoles {
			if strings.EqualFold(strings.TrimSpace(role), candidate.Role) {
				matched = true
				break
			}
		}
		if !matched {
			return 0.1
		}
		if len(task.PreferredKinds) == 0 {
			return 0.96
		}
	}
	if len(task.PreferredKinds) == 0 {
		if task.PlayResult && candidate.Playable {
			return 0.9
		}
		return 0.78
	}
	for _, kind := range task.PreferredKinds {
		if strings.EqualFold(strings.TrimSpace(kind), candidate.Kind) {
			return 0.96
		}
	}
	return 0.1
}

func browserOrdinalEvidence(ordinal, count int, candidate browserTargetCandidate) float64 {
	if ordinal <= 0 || count < ordinal {
		return 0
	}
	if candidate.Position > 0 || candidate.Order > 0 {
		return 0.96
	}
	return 0.82
}

func browserCandidateSourceEvidence(source string) float64 {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "focused_dom_content_candidate", "observed_media", "structured_content":
		return 0.94
	case "semantic_page":
		return 0.86
	case "accessibility", "key_element":
		return 0.74
	case "legacy":
		return 0.66
	default:
		return 0.8
	}
}

func browserCandidateConfidence(evidence browserTargetEvidence, visualRequired bool) float64 {
	weights := browserTargetEvidence{Semantic: 0.31, Kind: 0.17, Ordinal: 0.18, Source: 0.13, Page: 0.13, Spatial: 0.08}
	if visualRequired {
		weights = browserTargetEvidence{Semantic: 0.22, Kind: 0.13, Ordinal: 0.14, Source: 0.09, Page: 0.09, Visual: 0.25, Spatial: 0.08}
	}
	score := evidence.Semantic*weights.Semantic + evidence.Kind*weights.Kind + evidence.Ordinal*weights.Ordinal +
		evidence.Source*weights.Source + evidence.Page*weights.Page + evidence.Visual*weights.Visual + evidence.Spatial*weights.Spatial
	return math.Round(clampBrowserScore(score)*100) / 100
}

func browserCandidateIdentity(candidate browserTargetCandidate) string {
	if normalized := normalizeBrowserPageURL(candidate.URL); normalized != "" {
		return "url:" + normalized
	}
	if candidate.Ref != "" {
		return "ref:" + candidate.Ref
	}
	return "label:" + normalizeCurrentPageSelection(candidate.Label)
}

func browserTaskRequiresVisualGrounding(task inferredBrowserTask) bool {
	goal := strings.ToLower(strings.Join([]string{task.Query, task.Target}, " "))
	for _, marker := range []string{
		"red", "blue", "green", "yellow", "black", "white", "image", "thumbnail", "showing", "looks like",
		"红色", "紅色", "蓝色", "藍色", "绿色", "綠色", "黄色", "黃色", "黑色", "白色",
		"图片", "圖片", "缩略图", "縮略圖", "封面", "看起来", "看起來",
	} {
		if strings.Contains(goal, marker) {
			return true
		}
	}
	return false
}

func browserScreenshotEvidenceAvailable(value map[string]any) bool {
	if value == nil {
		return false
	}
	available, _ := value["available"].(bool)
	return available || browserStringValue(value["path"]) != ""
}

func browserResolutionThresholds(risk string) (execute, observe float64) {
	switch normalizeBrowserRisk(risk) {
	case "HIGH":
		return 0.92, 0.8
	case "MEDIUM":
		return 0.84, 0.68
	default:
		return 0.72, 0.52
	}
}

func normalizeBrowserRisk(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "HIGH":
		return "HIGH"
	case "MEDIUM":
		return "MEDIUM"
	default:
		return "LOW"
	}
}

func clampBrowserScore(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
