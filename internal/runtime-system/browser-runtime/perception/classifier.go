package perception

import (
	"net/url"
	"strings"
)

func classifyPage(raw map[string]any) Classification {
	currentURL := stringValue(raw["url"])
	title := strings.ToLower(stringValue(raw["title"]))
	content := strings.ToLower(stringValue(raw["content"]))
	snapshot := strings.ToLower(stringValue(raw["snapshot"]))
	combined := title + "\n" + truncate(content, 5000) + "\n" + truncate(snapshot, 5000)
	parsed, _ := url.Parse(strings.TrimSpace(currentURL))
	path := strings.ToLower(parsed.Path)

	if boolValue(raw["challenge_detected"]) || containsAny(combined,
		"captcha", "verify you are human", "unusual traffic", "cloudflare ray id") {
		return Classification{Type: "challenge", Confidence: 0.98, Signals: []string{"verification_signal"}}
	}
	if isErrorPageObservation(raw) {
		return Classification{Type: "error_page", Confidence: 0.99, Signals: []string{"error_page_signal"}}
	}
	if containsAny(path, "/login", "/signin", "/auth/") ||
		(containsAny(combined, "password", "密码") && containsAny(combined, "log in", "login", "sign in", "登录")) {
		return Classification{Type: "authentication", Confidence: 0.94, Signals: []string{"authentication_url"}}
	}
	if hasSearchQuery(parsed) || strings.Contains(path, "search") && containsAny(snapshot, "link", "heading") {
		return Classification{Type: "search_results", Confidence: 0.92, Signals: []string{"search_url"}}
	}
	if containsAny(combined, "add to cart", "buy now", "checkout", "加入购物车", "立即购买") && pricePattern.MatchString(combined) {
		return Classification{Type: "commerce", Confidence: 0.9, Signals: []string{"commerce_pattern"}}
	}
	if containsAny(snapshot, "button \"play", "button \"pause", "播放", "暂停") || durationPattern.MatchString(snapshot) {
		return Classification{Type: "media_catalog", Confidence: 0.82, Signals: []string{"media_pattern"}}
	}
	if containsAny(path, ".pdf", ".doc", ".txt") || containsAny(title, "pdf viewer", "document") {
		return Classification{Type: "document", Confidence: 0.86, Signals: []string{"document_signal"}}
	}
	if containsAny(path, ".png", ".jpg", ".jpeg", ".webp", ".gif") || containsAny(title, "image viewer") {
		return Classification{Type: "image", Confidence: 0.86, Signals: []string{"image_signal"}}
	}
	if containsAny(snapshot, "canvas", "graphics-document") || containsAny(combined, "canvas application") {
		return Classification{Type: "visual_canvas", Confidence: 0.8, Signals: []string{"canvas_signal"}}
	}
	if containsAny(snapshot, "textbox", "combobox", "checkbox", "radio", "submit") {
		return Classification{Type: "interactive_form", Confidence: 0.78, Signals: []string{"form_controls"}}
	}
	if strings.TrimSpace(currentURL) == "" && strings.TrimSpace(title) == "" {
		return Classification{Type: "unknown", Confidence: 0.2, Signals: []string{"missing_page_identity"}}
	}
	return Classification{Type: "web_page", Confidence: 0.7, Signals: []string{"generic_page"}}
}

func isErrorPageObservation(raw map[string]any) bool {
	title := strings.ToLower(strings.Join(strings.Fields(stringValue(raw["title"])), " "))
	for _, marker := range []string{
		"page not found", "404 not found", "404 - not found", "404 | not found", "error 404",
		"页面不存在", "找不到页面", "页面未找到", "ページが見つかりません",
	} {
		if title == marker || strings.HasPrefix(title, marker+" ") || strings.HasPrefix(title, marker+" -") || strings.HasPrefix(title, marker+" |") {
			return true
		}
	}
	if title == "404" || strings.HasPrefix(title, "404 -") || strings.HasPrefix(title, "404 |") {
		return true
	}
	return false
}

func hasSearchQuery(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	for _, key := range []string{"q", "query", "search", "keyword", "keywords", "w", "wd"} {
		if strings.TrimSpace(parsed.Query().Get(key)) != "" {
			return true
		}
	}
	return false
}

func analyzeIntent(request Request) IntentSignals {
	var values []string
	values = append(values, request.Action)
	for key, value := range request.Arguments {
		values = append(values, key, stringValue(value))
	}
	combined := strings.ToLower(strings.Join(values, " "))
	signals := IntentSignals{}
	visualTerms := []string{
		"image", "picture", "photo", "thumbnail", "cover", "color", "icon", "look", "visual", "screenshot",
		"\u56fe\u7247", "\u7167\u7247", "\u5c01\u9762", "\u989c\u8272", "\u56fe\u6807", "\u622a\u56fe", "\u770b\u8d77\u6765",
	}
	spatialTerms := []string{
		"left", "right", "top", "bottom", "above", "below", "position", "coordinate", "near",
		"\u5de6\u8fb9", "\u53f3\u8fb9", "\u4e0a\u9762", "\u4e0b\u9762", "\u4f4d\u7f6e", "\u5750\u6807", "\u9644\u8fd1",
	}
	ocrTerms := []string{
		"ocr", "text in image", "read the image", "read screenshot",
		"\u8bc6\u522b\u56fe\u7247\u6587\u5b57", "\u8bfb\u53d6\u622a\u56fe", "\u56fe\u4e2d\u6587\u5b57",
	}
	signals.Visual, signals.Keywords = matchingTerms(combined, visualTerms)
	signals.Spatial, signals.Keywords = mergeMatchingTerms(combined, spatialTerms, signals.Keywords)
	signals.OCR, signals.Keywords = mergeMatchingTerms(combined, ocrTerms, signals.Keywords)
	if request.Action == "screenshot" {
		signals.Visual = true
		signals.ExplicitScreenshot = true
	}
	if request.Arguments != nil {
		if enabled, ok := request.Arguments["screenshot"].(bool); ok {
			signals.ExplicitScreenshot = enabled
			signals.ScreenshotDisabled = !enabled
		}
	}
	return signals
}

func matchingTerms(value string, terms []string) (bool, []string) {
	matches := make([]string, 0, 4)
	for _, term := range terms {
		if strings.Contains(value, term) {
			matches = append(matches, term)
		}
	}
	return len(matches) > 0, matches
}

func mergeMatchingTerms(value string, terms []string, existing []string) (bool, []string) {
	matched, matches := matchingTerms(value, terms)
	return matched, append(existing, matches...)
}
