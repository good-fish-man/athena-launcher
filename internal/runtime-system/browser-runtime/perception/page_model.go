package perception

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

func understandPage(raw map[string]any, tree UITree, patterns []Pattern) SemanticPageModel {
	model := SemanticPageModel{
		Schema: pageModelSchema, Type: "web_page", Confidence: 0.62,
		Title: stringValue(raw["title"]), URL: stringValue(raw["url"]),
		Sections: make([]SemanticSection, 0, 6), Entities: make([]SemanticEntity, 0, 24),
		Interactions: make([]Interaction, 0, 16), PatternIDs: make([]string, 0, len(patterns)),
	}
	nodes := make(map[string]UINode, len(tree.Nodes))
	for _, node := range tree.Nodes {
		if node.Ref != "" {
			nodes[node.Ref] = node
		}
	}

	for _, pattern := range patterns {
		model.PatternIDs = append(model.PatternIDs, pattern.ID)
		applyPatternToPageModel(&model, pattern, nodes)
	}
	appendObservedCandidates(&model, raw)
	appendObservedContentCandidates(&model, raw)
	appendStandaloneEntities(&model, tree)
	appendCurrentMediaInteraction(&model, raw)
	appendControlInteractions(&model, tree)
	appendEntityInteractions(&model)
	classifySemanticPage(&model, raw, patterns)
	limitSemanticPageModel(&model, 24, 16)
	return model
}

func appendObservedContentCandidates(model *SemanticPageModel, raw map[string]any) {
	items := mapSliceValue(raw["content_candidates"])
	if len(items) == 0 {
		return
	}
	seen := make(map[string]bool, len(model.Entities)+len(items))
	for _, entity := range model.Entities {
		seen[entity.URL] = true
	}
	position := len(model.Entities)
	for _, item := range items {
		label := compactElementName(stringValue(item["title"]))
		targetURL := stringValue(item["url"])
		if label == "" || targetURL == "" || seen[targetURL] {
			continue
		}
		kind := inferLinkedEntityKind(targetURL, label)
		if kind == "link" {
			kind = "content"
		}
		seen[targetURL] = true
		position++
		context := strings.ToLower(stringValue(item["context"]))
		score := intValue(item["score"])
		confidence := 0.72
		switch context {
		case "collection":
			confidence = 0.92
		case "heading":
			confidence = 0.96
		case "standalone":
			confidence = 0.78
		case "article_body":
			confidence = 0.62
		}
		model.Entities = append(model.Entities, SemanticEntity{
			ID: stableUnderstandingID("entity", kind, targetURL, label), Kind: kind, Label: label,
			URL: targetURL, Position: position, Playable: kind == "video" || kind == "audio", Confidence: confidence,
			Attributes: map[string]any{"source": "focused_dom_content_candidate", "context": context, "score": score},
		})
	}
}

func appendCurrentMediaInteraction(model *SemanticPageModel, raw map[string]any) {
	kind := inferLinkedEntityKind(model.URL, "")
	if kind != "video" && kind != "audio" && !containsString(model.Signals, "media_controls") {
		return
	}
	if playback, ok := raw["playback"].(map[string]any); ok && boolValue(playback["playing"]) && boolValue(playback["verified"]) {
		return
	}
	model.Interactions = append(model.Interactions, Interaction{
		Schema: interactionSchema, ID: stableUnderstandingID("interaction", "play_current", model.URL),
		Kind: "play_current", Label: "Play current media", Description: "Start or resume the current page media and verify playback.",
		TargetURL: model.URL, Risk: "LOW", Confidence: 0.9,
		Postcondition: map[string]any{"media_playing": true},
	})
}

func appendObservedCandidates(model *SemanticPageModel, raw map[string]any) {
	items := mapSliceValue(raw["media_candidates"])
	if len(items) == 0 {
		return
	}
	seen := make(map[string]bool, len(model.Entities)+len(items))
	for _, entity := range model.Entities {
		seen[entity.URL] = true
	}
	position := 0
	for _, item := range items {
		label := compactElementName(stringValue(item["title"]))
		targetURL := stringValue(item["url"])
		kind := strings.ToLower(stringValue(item["kind"]))
		if kind != "video" && kind != "audio" {
			kind = inferLinkedEntityKind(targetURL, label)
		}
		if label == "" || targetURL == "" || seen[targetURL] || (kind != "video" && kind != "audio") {
			continue
		}
		seen[targetURL] = true
		position++
		model.Entities = append(model.Entities, SemanticEntity{
			ID: stableUnderstandingID("entity", kind, targetURL, label), Kind: kind, Label: label,
			Ref: stringValue(item["ref"]), URL: targetURL, Position: position, Playable: true, Confidence: 0.94,
			Attributes: map[string]any{"source": "focused_dom_candidate"},
		})
	}
}

func applyPatternToPageModel(model *SemanticPageModel, pattern Pattern, nodes map[string]UINode) {
	switch pattern.Kind {
	case "search":
		inputRef := stringValue(pattern.Attributes["input_ref"])
		submitRef := stringValue(pattern.Attributes["submit_ref"])
		model.Interactions = append(model.Interactions, Interaction{
			Schema: interactionSchema, ID: stableUnderstandingID("interaction", "search", inputRef, submitRef),
			Kind: "search", Label: "Search this page", Description: "Enter a query in the page search control.",
			InputRef: inputRef, Ref: submitRef, Risk: "LOW", Confidence: pattern.Confidence,
			Postcondition: map[string]any{"results_changed": true},
		})
	case "authentication":
		model.Signals = append(model.Signals, "authentication_form")
	case "profile_selection":
		model.Signals = append(model.Signals, "profile_selection")
		sectionID := stableUnderstandingID("section", pattern.ID)
		section := SemanticSection{ID: sectionID, Kind: "profile_selection", Label: pattern.Label, EntityIDs: make([]string, 0, len(pattern.NodeRefs))}
		position := 0
		for _, ref := range pattern.NodeRefs {
			node, ok := nodes[ref]
			if !ok || node.Name == "" {
				continue
			}
			position++
			entity := SemanticEntity{
				ID: stableUnderstandingID("entity", "profile", node.Ref, node.Name), Kind: "profile", Label: node.Name,
				Ref: node.Ref, Position: position, SectionID: sectionID, Confidence: pattern.Confidence,
				Attributes: map[string]any{"region": node.Region, "role": node.Role, "sensitive_target_omitted": true},
			}
			model.Entities = append(model.Entities, entity)
			section.EntityIDs = append(section.EntityIDs, entity.ID)
		}
		if len(section.EntityIDs) > 0 {
			model.Sections = append(model.Sections, section)
		}
	case "media_controls":
		model.Signals = append(model.Signals, "media_controls")
	case "item_list", "media_list", "collection_list", "discussion_list", "product_list", "article_list":
		sectionID := stableUnderstandingID("section", pattern.ID)
		section := SemanticSection{ID: sectionID, Kind: pattern.Kind, Label: pattern.Label, EntityIDs: make([]string, 0, len(pattern.NodeRefs))}
		entityKind := stringValue(pattern.Attributes["entity_kind"])
		position := 0
		seenURLs := make(map[string]bool)
		for _, ref := range pattern.NodeRefs {
			node, ok := nodes[ref]
			if !ok || node.Name == "" || node.URL == "" || seenURLs[node.URL] {
				continue
			}
			seenURLs[node.URL] = true
			position++
			kind := entityKind
			if kind == "" || kind == "link" {
				kind = node.Kind
			}
			entity := semanticEntityFromNode(node, kind, sectionID, position, pattern.Confidence)
			model.Entities = append(model.Entities, entity)
			section.EntityIDs = append(section.EntityIDs, entity.ID)
		}
		if len(section.EntityIDs) >= 2 {
			model.Sections = append(model.Sections, section)
		}
	case "commerce":
		model.Signals = append(model.Signals, "commerce_controls")
	}
}

func appendStandaloneEntities(model *SemanticPageModel, tree UITree) {
	seenRefs := make(map[string]bool, len(model.Entities))
	seenURLs := make(map[string]bool, len(model.Entities))
	for _, entity := range model.Entities {
		seenRefs[entity.Ref] = true
		seenURLs[entity.URL] = true
	}
	sorted := append([]UINode(nil), tree.Nodes...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Order < sorted[j].Order })
	position := len(model.Entities)
	for _, node := range sorted {
		if node.Role != "link" || node.Ref == "" || node.URL == "" || node.Name == "" || seenRefs[node.Ref] || seenURLs[node.URL] || isNavigationNode(node) {
			continue
		}
		if node.Kind == "link" && !hasContentLinkSignal(node) {
			continue
		}
		position++
		entity := semanticEntityFromNode(node, node.Kind, "", position, 0.64)
		model.Entities = append(model.Entities, entity)
		seenRefs[node.Ref], seenURLs[node.URL] = true, true
	}
}

func semanticEntityFromNode(node UINode, kind, sectionID string, position int, confidence float64) SemanticEntity {
	if kind == "" || kind == "link" {
		kind = "content"
	}
	playable := kind == "video" || kind == "audio"
	return SemanticEntity{
		ID: stableUnderstandingID("entity", kind, node.URL, node.Name), Kind: kind, Label: node.Name,
		Ref: node.Ref, URL: node.URL, Position: position, SectionID: sectionID,
		Playable: playable, Confidence: roundConfidence(confidence),
		Attributes: map[string]any{"region": node.Region, "role": node.Role},
	}
}

func appendEntityInteractions(model *SemanticPageModel) {
	for _, entity := range model.Entities {
		kind := "open"
		description := "Open this visible page item and verify the page changed."
		postcondition := map[string]any{"page_changed": true}
		if entity.Playable {
			kind = "play"
			description = "Open this media item and verify that playback starts."
			postcondition = map[string]any{"media_playing": true}
		}
		model.Interactions = append(model.Interactions, Interaction{
			Schema: interactionSchema, ID: stableUnderstandingID("interaction", kind, entity.ID),
			Kind: kind, Label: entity.Label, Description: description, Ref: entity.Ref,
			TargetURL: entity.URL, EntityID: entity.ID, Risk: "LOW", Confidence: entity.Confidence,
			Postcondition: postcondition,
		})
	}
}

func appendControlInteractions(model *SemanticPageModel, tree UITree) {
	for _, node := range tree.Nodes {
		if node.Kind != "media_control" || node.Ref == "" || boolMapValue(node.State, "disabled") {
			continue
		}
		transport := ""
		if len(node.Signals) > 0 {
			transport = node.Signals[0]
		}
		if transport == "" {
			continue
		}
		label := node.Name
		if label == "" {
			label = strings.Title(transport) //nolint:staticcheck // User-facing fallback for a fixed ASCII verb.
		}
		model.Interactions = append(model.Interactions, Interaction{
			Schema: interactionSchema, ID: stableUnderstandingID("interaction", "transport", transport, node.Ref),
			Kind: "media_" + transport, Label: label, Description: "Control the current media and verify its state.",
			Ref: node.Ref, Risk: "LOW", Confidence: 0.96,
			Postcondition: map[string]any{"media_state_changed": true},
		})
	}
}

func classifySemanticPage(model *SemanticPageModel, raw map[string]any, patterns []Pattern) {
	if boolValue(raw["challenge_detected"]) || semanticTextContains(raw, "captcha", "verify you are human", "unusual traffic", "cloudflare ray id") {
		model.Type, model.Confidence = "challenge", 0.99
		model.Signals = append(model.Signals, "verification_signal")
		model.RequiresVisual = true
		return
	}
	if isErrorPageObservation(raw) {
		model.Type, model.Confidence = "error_page", 0.99
		model.Signals = append(model.Signals, "error_page_signal")
		return
	}
	headingCandidates, collectionCandidates := 0, 0
	for _, candidate := range mapSliceValue(raw["content_candidates"]) {
		switch strings.ToLower(strings.TrimSpace(stringValue(candidate["context"]))) {
		case "heading":
			headingCandidates++
		case "collection":
			collectionCandidates++
		}
	}
	mediaCandidateCount := len(mapSliceValue(raw["media_candidates"]))
	contentDominant := (headingCandidates >= 3 || collectionCandidates >= 5) && mediaCandidateCount < 2
	bestType, bestConfidence := "web_page", 0.62
	if contentDominant {
		bestType, bestConfidence = "content_feed", 0.9
	}
	parsedURL, _ := url.Parse(model.URL)
	for _, pattern := range patterns {
		candidate := ""
		switch pattern.Kind {
		case "authentication":
			candidate = "authentication"
		case "profile_selection":
			candidate = "profile_selection"
		case "media_list":
			candidate = "media_catalog"
		case "media_controls":
			// A single embedded player is an interaction inside the page, not
			// evidence that a repeated article/feed page is a media catalog.
			if !contentDominant {
				candidate = "media_catalog"
			}
		case "discussion_list":
			candidate = "discussion"
		case "product_list", "commerce":
			candidate = "commerce"
		case "article_list":
			candidate = "content_feed"
		case "item_list":
			candidate = "collection"
		case "search":
			if len(model.Entities) >= 2 && (hasSearchQuery(parsedURL) || semanticTextContains(raw, "search results", "results for", "搜索结果", "查询结果")) {
				candidate = "search_results"
			}
		}
		if candidate != "" && pattern.Confidence > bestConfidence {
			bestType, bestConfidence = candidate, pattern.Confidence
		}
	}
	entityKinds := make(map[string]int)
	for _, entity := range model.Entities {
		entityKinds[entity.Kind]++
	}
	for _, candidate := range []struct{ kind, pageType string }{
		{kind: "video", pageType: "media_catalog"}, {kind: "audio", pageType: "media_catalog"},
		{kind: "discussion", pageType: "discussion"}, {kind: "product", pageType: "commerce"},
		{kind: "article", pageType: "content_feed"},
	} {
		if entityKinds[candidate.kind] >= 2 && bestConfidence < 0.84 {
			bestType, bestConfidence = candidate.pageType, 0.84
		}
	}
	if hasDocumentSignal(model.URL, model.Title) {
		bestType, bestConfidence = "document", 0.88
	}
	if hasImageSignal(model.URL, model.Title) {
		bestType, bestConfidence, model.RequiresVisual = "image", 0.88, true
	}
	model.Type, model.Confidence = bestType, roundConfidence(bestConfidence)
	model.Signals = uniqueStrings(model.Signals)
}

func limitSemanticPageModel(model *SemanticPageModel, maxEntities, maxInteractions int) {
	if len(model.Entities) > maxEntities {
		model.Entities = model.Entities[:maxEntities]
	}
	validEntity := make(map[string]bool, len(model.Entities))
	for _, entity := range model.Entities {
		validEntity[entity.ID] = true
	}
	for index := range model.Sections {
		filtered := model.Sections[index].EntityIDs[:0]
		for _, id := range model.Sections[index].EntityIDs {
			if validEntity[id] {
				filtered = append(filtered, id)
			}
		}
		model.Sections[index].EntityIDs = filtered
	}
	if len(model.Interactions) > maxInteractions {
		model.Interactions = model.Interactions[:maxInteractions]
	}
}

func semanticTextContains(raw map[string]any, needles ...string) bool {
	combined := strings.ToLower(strings.Join([]string{
		stringValue(raw["url"]), stringValue(raw["title"]), truncate(stringValue(raw["content"]), 6000), truncate(stringValue(raw["snapshot"]), 6000),
	}, "\n"))
	return containsAny(combined, needles...)
}

func hasDocumentSignal(rawURL, title string) bool {
	combined := strings.ToLower(rawURL + " " + title)
	return containsAny(combined, ".pdf", ".doc", ".docx", ".txt", "pdf viewer", "document viewer")
}

func hasImageSignal(rawURL, title string) bool {
	combined := strings.ToLower(rawURL + " " + title)
	return containsAny(combined, ".png", ".jpg", ".jpeg", ".webp", ".gif", "image viewer")
}

func hasContentLinkSignal(node UINode) bool {
	if node.Kind != "link" {
		return true
	}
	name := strings.ToLower(node.Name)
	if durationPattern.MatchString(name) || len([]rune(node.Name)) >= 18 {
		return true
	}
	return containsAny(name, "read", "view", "open", "details", "more", "阅读", "查看", "详情", "更多")
}

func boolMapValue(values map[string]any, key string) bool {
	if values == nil {
		return false
	}
	value, _ := values[key].(bool)
	return value
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func mapSliceValue(value any) []map[string]any {
	switch typed := value.(type) {
	case []map[string]any:
		return typed
	case []any:
		result := make([]map[string]any, 0, len(typed))
		for _, item := range typed {
			if mapped, ok := item.(map[string]any); ok {
				result = append(result, mapped)
			}
		}
		return result
	default:
		return nil
	}
}

// SemanticPage returns the in-process page model without requiring a JSON
// marshal round-trip. External callers receive the same typed contract.
func SemanticPage(observation map[string]any) (SemanticPageModel, bool) {
	if observation == nil {
		return SemanticPageModel{}, false
	}
	switch typed := observation["semantic_page"].(type) {
	case SemanticPageModel:
		return typed, true
	case *SemanticPageModel:
		return *typed, typed != nil
	default:
		return SemanticPageModel{}, false
	}
}

func EntityAt(observation map[string]any, ordinal int, preferredKinds ...string) (SemanticEntity, bool) {
	model, ok := SemanticPage(observation)
	if !ok || ordinal <= 0 {
		return SemanticEntity{}, false
	}
	preferred := make(map[string]bool, len(preferredKinds))
	for _, kind := range preferredKinds {
		preferred[kind] = true
	}
	matched := 0
	for _, entity := range model.Entities {
		if len(preferred) > 0 && !preferred[entity.Kind] {
			continue
		}
		matched++
		if matched == ordinal {
			return entity, true
		}
	}
	return SemanticEntity{}, false
}

func InteractionCandidates(observation map[string]any) []Interaction {
	model, ok := SemanticPage(observation)
	if !ok {
		return nil
	}
	return append([]Interaction(nil), model.Interactions...)
}

func describeSemanticModel(model SemanticPageModel) string {
	return fmt.Sprintf("type=%s entities=%d interactions=%d confidence=%.2f", model.Type, len(model.Entities), len(model.Interactions), model.Confidence)
}
