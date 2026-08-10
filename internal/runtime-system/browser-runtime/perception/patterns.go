package perception

import (
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	dynamicPathSegmentPattern = regexp.MustCompile(`(?i)^(?:\d{2,}|[a-f0-9]{8,}|[a-z0-9_-]{12,})$`)
	durationPattern           = regexp.MustCompile(`\b\d{1,2}:\d{2}(?::\d{2})?\b`)
	pricePattern              = regexp.MustCompile(`(?:[$€£¥￥]\s?\d|\d(?:[.,]\d{2})?\s?(?:usd|eur|gbp|jpy|cny))`)
)

func recognizePatterns(tree UITree, raw map[string]any) []Pattern {
	patterns := make([]Pattern, 0, 12)
	patterns = append(patterns, recognizeSearchPattern(tree)...)
	patterns = append(patterns, recognizeAuthenticationPattern(tree)...)
	profilePatterns := recognizeProfileSelectionPattern(tree, raw)
	patterns = append(patterns, profilePatterns...)
	patterns = append(patterns, recognizeMediaControlPattern(tree)...)
	if len(profilePatterns) == 0 {
		// Profile-switch URLs frequently carry opaque identity tokens. Keep a
		// chooser as a dedicated interaction surface instead of also exposing
		// those links through a generic collection model.
		patterns = append(patterns, recognizeCollections(tree)...)
	}
	patterns = append(patterns, recognizePaginationPattern(tree)...)
	patterns = append(patterns, recognizeCommercePattern(tree, raw)...)
	sort.SliceStable(patterns, func(i, j int) bool {
		if patterns[i].Confidence == patterns[j].Confidence {
			return patterns[i].ID < patterns[j].ID
		}
		return patterns[i].Confidence > patterns[j].Confidence
	})
	return patterns
}

func recognizeProfileSelectionPattern(tree UITree, raw map[string]any) []Pattern {
	combined := strings.ToLower(strings.Join([]string{
		stringValue(raw["title"]), truncate(stringValue(raw["content"]), 5000), truncate(stringValue(raw["snapshot"]), 5000),
	}, "\n"))
	if !containsAny(combined,
		"who's watching", "who is watching", "choose a profile", "select a profile", "select profile",
		"选择个人资料", "选择资料", "选择用户", "誰在觀看", "プロフィールを選択",
	) {
		return nil
	}

	refs := make([]string, 0, 8)
	for _, node := range tree.Nodes {
		if node.Ref == "" || (node.Role != "link" && node.Role != "button" && node.Role != "radio" && node.Role != "option") {
			continue
		}
		label := strings.ToLower(strings.TrimSpace(node.Name))
		if label == "" || containsAny(label,
			"add profile", "manage profile", "edit profile", "delete profile", "sign out", "log out",
			"添加个人资料", "管理个人资料", "编辑个人资料", "删除个人资料", "退出",
		) {
			continue
		}
		lowerURL := strings.ToLower(node.URL)
		profileURL := containsAny(lowerURL, "switchprofile", "switch-profile", "selectprofile", "select-profile", "/profiles/select")
		shortChoice := (node.Role == "button" || node.Role == "radio" || node.Role == "option") && len([]rune(node.Name)) <= 80
		if profileURL || shortChoice {
			refs = append(refs, node.Ref)
		}
	}
	if len(refs) == 0 {
		return nil
	}
	return []Pattern{{
		Schema: patternSchema, ID: stableUnderstandingID("pattern", "profile_selection", strings.Join(refs, ",")),
		Kind: "profile_selection", Label: "Profile selection", Confidence: 0.97, NodeRefs: refs,
		Attributes: map[string]any{"item_count": len(refs)},
	}}
}

func recognizeSearchPattern(tree UITree) []Pattern {
	inputs := nodesByKind(tree, "search_input")
	if len(inputs) == 0 {
		return nil
	}
	controls := nodesByKind(tree, "search_control")
	input := inputs[0]
	refs := []string{input.Ref}
	attributes := map[string]any{"input_ref": input.Ref}
	confidence := 0.82
	if control, ok := nearestNode(input, controls, 8); ok {
		refs = append(refs, control.Ref)
		attributes["submit_ref"] = control.Ref
		confidence = 0.95
	}
	return []Pattern{{
		Schema: patternSchema, ID: stableUnderstandingID("pattern", "search", input.ID),
		Kind: "search", Label: "Search", Confidence: confidence, NodeRefs: refs, Attributes: attributes,
	}}
}

func recognizeAuthenticationPattern(tree UITree) []Pattern {
	var credentialRefs, submitRefs []string
	for _, node := range tree.Nodes {
		lower := strings.ToLower(node.Name)
		if node.Kind == "password_input" || (node.Kind == "input" && containsAny(lower, "email", "username", "account", "邮箱", "账号", "用户")) {
			credentialRefs = append(credentialRefs, node.Ref)
		}
		if node.Kind == "control" && containsAny(lower, "log in", "login", "sign in", "continue", "登录", "登入", "继续") {
			submitRefs = append(submitRefs, node.Ref)
		}
	}
	if len(credentialRefs) == 0 {
		return nil
	}
	confidence := 0.76
	if len(submitRefs) > 0 {
		confidence = 0.94
	}
	refs := append(append([]string{}, credentialRefs...), submitRefs...)
	return []Pattern{{
		Schema: patternSchema, ID: stableUnderstandingID("pattern", "authentication", strings.Join(refs, ",")),
		Kind: "authentication", Label: "Authentication form", Confidence: confidence, NodeRefs: refs,
		Attributes: map[string]any{"credential_refs": credentialRefs, "submit_refs": submitRefs},
	}}
}

func recognizeMediaControlPattern(tree UITree) []Pattern {
	controls := nodesByKind(tree, "media_control")
	if len(controls) == 0 {
		return nil
	}
	refs := make([]string, 0, len(controls))
	transports := make([]string, 0, len(controls))
	for _, node := range controls {
		refs = append(refs, node.Ref)
		for _, signal := range node.Signals {
			if signal != "" {
				transports = append(transports, signal)
			}
		}
	}
	return []Pattern{{
		Schema: patternSchema, ID: stableUnderstandingID("pattern", "media_controls", strings.Join(refs, ",")),
		Kind: "media_controls", Label: "Media controls", Confidence: 0.96, NodeRefs: refs,
		Attributes: map[string]any{"transports": uniqueStrings(transports)},
	}}
}

func recognizeCollections(tree UITree) []Pattern {
	groups := make(map[string][]UINode)
	for _, node := range tree.Nodes {
		if node.Role != "link" || strings.TrimSpace(node.URL) == "" || isNavigationNode(node) {
			continue
		}
		shape := semanticURLShape(node.URL)
		if shape == "" {
			continue
		}
		groups[node.Kind+"|"+shape] = append(groups[node.Kind+"|"+shape], node)
	}
	patterns := make([]Pattern, 0, len(groups))
	for key, nodes := range groups {
		if len(nodes) < 2 {
			continue
		}
		sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Order < nodes[j].Order })
		refs := make([]string, 0, len(nodes))
		for _, node := range nodes {
			refs = append(refs, node.Ref)
		}
		parts := strings.SplitN(key, "|", 2)
		entityKind := parts[0]
		patternKind := "item_list"
		switch entityKind {
		case "video", "audio":
			patternKind = "media_list"
		case "media_collection":
			patternKind = "collection_list"
		case "discussion":
			patternKind = "discussion_list"
		case "product":
			patternKind = "product_list"
		case "article":
			patternKind = "article_list"
		}
		confidence := 0.68 + float64(minInt(len(nodes), 6))*0.04
		patterns = append(patterns, Pattern{
			Schema: patternSchema, ID: stableUnderstandingID("pattern", patternKind, key), Kind: patternKind,
			Label: humanPatternLabel(patternKind), Confidence: roundConfidence(clamp(confidence, 0.68, 0.94)), NodeRefs: refs,
			Attributes: map[string]any{"entity_kind": entityKind, "url_shape": parts[1], "item_count": len(nodes)},
		})
	}
	return patterns
}

func recognizePaginationPattern(tree UITree) []Pattern {
	refs := make([]string, 0, 8)
	for _, node := range tree.Nodes {
		if node.Role != "link" && node.Role != "button" {
			continue
		}
		label := strings.ToLower(strings.TrimSpace(node.Name))
		if isPageNumber(label) || exactSemanticLabel(label, "next", "previous", "下一页", "上一页", "下页", "上页") {
			refs = append(refs, node.Ref)
		}
	}
	if len(refs) < 2 {
		return nil
	}
	return []Pattern{{
		Schema: patternSchema, ID: stableUnderstandingID("pattern", "pagination", strings.Join(refs, ",")),
		Kind: "pagination", Label: "Pagination", Confidence: 0.86, NodeRefs: refs,
	}}
}

func recognizeCommercePattern(tree UITree, raw map[string]any) []Pattern {
	combined := strings.ToLower(stringValue(raw["content"]) + "\n" + stringValue(raw["snapshot"]))
	priceSignals := len(pricePattern.FindAllString(combined, 6))
	actionRefs := make([]string, 0, 4)
	for _, node := range tree.Nodes {
		if node.Role != "button" && node.Role != "link" {
			continue
		}
		if containsAny(strings.ToLower(node.Name), "add to cart", "buy now", "checkout", "加入购物车", "立即购买", "结账") {
			actionRefs = append(actionRefs, node.Ref)
		}
	}
	// Price-like text alone commonly appears in news, prize announcements and
	// technical articles. Commerce requires an actual transactional action.
	if len(actionRefs) == 0 {
		return nil
	}
	confidence := 0.7
	if priceSignals > 1 && len(actionRefs) > 0 {
		confidence = 0.94
	}
	return []Pattern{{
		Schema: patternSchema, ID: stableUnderstandingID("pattern", "commerce", strings.Join(actionRefs, ",")),
		Kind: "commerce", Label: "Commerce", Confidence: confidence, NodeRefs: actionRefs,
		Attributes: map[string]any{"price_signals": priceSignals},
	}}
}

func nodesByKind(tree UITree, kind string) []UINode {
	result := make([]UINode, 0, 4)
	for _, node := range tree.Nodes {
		if node.Kind == kind {
			result = append(result, node)
		}
	}
	return result
}

func nearestNode(source UINode, candidates []UINode, maxOrderDistance int) (UINode, bool) {
	bestDistance := maxOrderDistance + 1
	var best UINode
	for _, candidate := range candidates {
		distance := candidate.Order - source.Order
		if distance < 0 {
			distance = -distance
		}
		if source.Region == candidate.Region && distance < bestDistance {
			best, bestDistance = candidate, distance
		}
	}
	return best, bestDistance <= maxOrderDistance
}

func semanticURLShape(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Path == "" {
		return ""
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index, segment := range segments {
		if dynamicPathSegmentPattern.MatchString(segment) {
			segments[index] = "{id}"
		}
	}
	shape := "/" + strings.Join(segments, "/")
	keys := make([]string, 0, len(parsed.Query()))
	for key := range parsed.Query() {
		lower := strings.ToLower(key)
		if exactURLIdentityKey(lower) {
			keys = append(keys, lower)
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		shape += "?" + strings.Join(keys, "&")
	}
	return shape
}

func exactURLIdentityKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "id", "v", "video", "video_id", "song", "song_id", "songid", "track", "track_id", "trackid", "item", "item_id", "post", "post_id", "thread", "thread_id", "article", "article_id":
		return true
	default:
		return false
	}
}

func isNavigationNode(node UINode) bool {
	if node.Kind == "navigation" || node.Region == "navigation" || node.Region == "sidebar" {
		return true
	}
	label := strings.ToLower(strings.TrimSpace(node.Name))
	return exactSemanticLabel(label,
		"home", "menu", "settings", "history", "profile", "account", "login", "sign in", "log out",
		"首页", "主页", "菜单", "设置", "历史记录", "个人中心", "账号", "登录", "退出",
	)
}

func humanPatternLabel(kind string) string {
	switch kind {
	case "media_list":
		return "Media list"
	case "discussion_list":
		return "Discussion list"
	case "product_list":
		return "Product list"
	case "article_list":
		return "Article list"
	case "collection_list":
		return "Media collections"
	default:
		return "Item list"
	}
}

func isPageNumber(value string) bool {
	number, err := strconv.Atoi(strings.TrimSpace(value))
	return err == nil && number > 0 && number < 10000
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}
