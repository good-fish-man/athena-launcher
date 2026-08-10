package browser_runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"

	"golang.org/x/net/publicsuffix"
)

const browserSuggestionSchema = "athena.browser.suggestion.v1"

var browserQuotedLabelPattern = regexp.MustCompile(`"([^"]+)"`)

type browserSuggestedAction struct {
	Schema        string         `json:"schema"`
	ID            string         `json:"id"`
	Label         string         `json:"label"`
	Description   string         `json:"description,omitempty"`
	Kind          string         `json:"kind"`
	Capability    string         `json:"capability"`
	Arguments     map[string]any `json:"arguments"`
	Risk          string         `json:"risk"`
	Precondition  map[string]any `json:"precondition,omitempty"`
	Postcondition map[string]any `json:"postcondition,omitempty"`
}

func browserSuggestedActions(sessionID string, state map[string]any) []browserSuggestedAction {
	currentURL := browserStringValue(state["url"])
	if currentURL == "" {
		return nil
	}
	if browserInterventionRequired(state) {
		if intervention, _ := state["intervention"].(map[string]any); browserStringValue(intervention["kind"]) == "target_confirmation" {
			if resolution, ok := browserTargetResolutionFromState(state); ok {
				return browserTargetConfirmationSuggestions(sessionID, state, &resolution)
			}
		}
		label := "I've completed sign-in"
		description := "After completing the visible sign-in or verification step, observe this same browser session and continue."
		postcondition := map[string]any{"intervention_cleared": true}
		if browserChallengeDetected(state) {
			label = "I've completed verification"
			postcondition["challenge_cleared"] = true
		}
		return []browserSuggestedAction{newBrowserSuggestion(
			sessionID, currentURL, browserSemanticElement{},
			label, description,
			"resume", "browser.observe", map[string]any{"snapshot": true}, postcondition,
		)}
	}

	result := make([]browserSuggestedAction, 0, 4)
	seen := make(map[string]bool)
	appendAction := func(action browserSuggestedAction) {
		labelKey := "label:" + action.Capability + ":" + strings.ToLower(strings.TrimSpace(action.Label))
		if len(result) >= 4 || action.ID == "" || seen[action.ID] || seen[labelKey] {
			return
		}
		seen[action.ID] = true
		seen[labelKey] = true
		result = append(result, action)
	}

	for _, action := range semanticBrowserSuggestions(sessionID, currentURL, state) {
		appendAction(action)
	}
	if len(result) > 0 {
		return result
	}

	playbackActive := browserPlaybackIsActive(state)
	currentMediaID := ""
	if playback, ok := state["playback"].(map[string]any); ok {
		currentMediaID = browserStringValue(playback["media_id"])
	}
	qqPlayer := isQQMusicPlayerURL(currentURL)
	if qqPlayer && !playbackActive {
		appendAction(newBrowserSuggestion(sessionID, currentURL, browserSemanticElement{}, "Resume playback", "Start or resume the current song and verify playback.", "media", "browser.play", map[string]any{
			"expected_page_url": currentURL,
			"verify_playback":   true,
			"snapshot":          true,
		}, map[string]any{"media_playing": true}))
	}
	if isYouTubeWatchPage(state) && !playbackActive {
		appendAction(newBrowserSuggestion(sessionID, currentURL, browserSemanticElement{}, "Play current video", "Start or resume this video and verify playback.", "media", "browser.play", map[string]any{
			"expected_page_url": currentURL,
			"verify_playback":   true,
			"snapshot":          true,
		}, map[string]any{"media_playing": true}))
	}
	if playbackActive {
		for _, element := range browserSemanticElements(state) {
			label, description, ok := browserTransportControl(element)
			if !ok || !browserTransportAvailable(state, label) {
				continue
			}
			appendAction(newBrowserSuggestion(sessionID, currentURL, element, label, description, "media", "browser.play", map[string]any{
				"ref":               element.Ref,
				"target_label":      browserElementTitle(element),
				"expected_page_url": currentURL,
				"verify_playback":   true,
				"snapshot":          true,
			}, map[string]any{"media_playing": true}))
		}
	}

	for _, candidate := range browserMediaCandidates(state) {
		if playbackActive && candidate.Kind == "audio" && candidate.ID == currentMediaID {
			continue
		}
		key := candidate.Kind + ":" + candidate.ID
		if seen[key] {
			continue
		}
		description := "Open this video and verify that playback starts."
		urlKind := "youtube_watch"
		if candidate.Kind == "audio" {
			description = "Open this song and verify that playback starts."
			urlKind = "music_track"
		}
		action := newBrowserSuggestion(sessionID, currentURL, browserSemanticElement{}, candidate.Title, description, "media", "browser.play", map[string]any{
			"target_url":        candidate.URL,
			"target_label":      candidate.Title,
			"expected_page_url": currentURL,
			"verify_playback":   true,
			"snapshot":          true,
		}, map[string]any{"url_kind": urlKind, "media_playing": true})
		appendActionWithKey(&result, seen, key, action, 4)
	}

	for _, element := range browserSemanticElements(state) {
		targetURL := resolveBrowserElementURL(currentURL, element.URL)
		if targetURL == "" {
			continue
		}
		if videoID := youtubeVideoID(targetURL); videoID != "" {
			key := "youtube:" + videoID
			if seen[key] {
				continue
			}
			seen[key] = true
			action := newBrowserSuggestion(sessionID, currentURL, element, browserElementTitle(element), "Open this video and verify that playback starts.", "media", "browser.play", map[string]any{
				"ref":               element.Ref,
				"target_url":        targetURL,
				"target_label":      browserElementTitle(element),
				"expected_page_url": currentURL,
				"verify_playback":   true,
				"snapshot":          true,
			}, map[string]any{"url_kind": "youtube_watch", "media_playing": true})
			// Use the stable video identity for de-duplication while keeping a
			// deterministic action id for transport and idempotency.
			appendActionWithKey(&result, seen, key, action, 4)
			continue
		}
		if isMusicResultElement(currentURL, element, targetURL) {
			key := "media:" + normalizeBrowserPageURL(targetURL)
			if seen[key] {
				continue
			}
			action := newBrowserSuggestion(sessionID, currentURL, element, browserElementTitle(element), "Open this item and verify that audio playback starts.", "media", "browser.play", map[string]any{
				"ref":               element.Ref,
				"target_url":        targetURL,
				"target_label":      browserElementTitle(element),
				"expected_page_url": currentURL,
				"verify_playback":   true,
				"snapshot":          true,
			}, map[string]any{"media_playing": true})
			appendActionWithKey(&result, seen, key, action, 4)
		}
	}

	if !isYouTubeWatchPage(state) && !qqPlayer && !playbackActive {
		for _, element := range browserSemanticElements(state) {
			if !isBrowserPlayButton(element) {
				continue
			}
			label := browserElementTitle(element)
			if label == "" {
				label = "Play media"
			}
			appendAction(newBrowserSuggestion(sessionID, currentURL, element, label, "Start this page item and verify that playback begins.", "media", "browser.play", map[string]any{
				"ref":               element.Ref,
				"target_label":      label,
				"expected_page_url": currentURL,
				"verify_playback":   true,
				"snapshot":          true,
			}, map[string]any{"media_playing": true}))
		}
	}

	if len(result) == 0 && !isMediaCatalogPage(state) {
		for _, element := range browserSemanticElements(state) {
			if !isSafeSuggestedElement(element) {
				continue
			}
			targetURL := resolveBrowserElementURL(currentURL, element.URL)
			arguments := map[string]any{
				"ref":               element.Ref,
				"target_label":      browserElementTitle(element),
				"expected_page_url": currentURL,
				"snapshot":          true,
			}
			if targetURL != "" {
				arguments["target_url"] = targetURL
			}
			appendAction(newBrowserSuggestion(sessionID, currentURL, element, browserElementTitle(element), "Continue with this visible page option.", "navigate", "browser.click", arguments, map[string]any{"page_changed": true}))
		}
	}
	return result
}

func browserTargetResolutionFromState(state map[string]any) (browserTargetResolution, bool) {
	switch value := state["target_resolution"].(type) {
	case browserTargetResolution:
		return value, true
	case *browserTargetResolution:
		if value != nil {
			return *value, true
		}
	}
	return browserTargetResolution{}, false
}

func browserTargetConfirmationSuggestions(sessionID string, state map[string]any, resolution *browserTargetResolution) []browserSuggestedAction {
	if resolution == nil {
		return nil
	}
	currentURL := browserStringValue(state["url"])
	if currentURL == "" {
		return nil
	}
	candidates := append([]browserTargetCandidate(nil), resolution.Candidates...)
	if resolution.Selected != nil {
		candidates = append([]browserTargetCandidate{*resolution.Selected}, candidates...)
	}
	result := make([]browserSuggestedAction, 0, 4)
	seen := make(map[string]bool)
	for _, candidate := range candidates {
		identity := browserCandidateIdentity(candidate)
		if identity == "" || seen[identity] || len(result) >= 4 {
			continue
		}
		seen[identity] = true
		element := browserSemanticElement{
			Ref: candidate.Ref, Label: candidate.Label, URL: candidate.URL, Kind: candidate.Kind,
		}
		arguments := map[string]any{
			"target_label": candidate.Label, "snapshot": true,
		}
		capability, kind := "browser.navigate", "navigate"
		postcondition := map[string]any{"url": candidate.URL}
		if candidate.Playable || candidate.Kind == "video" || candidate.Kind == "audio" {
			capability, kind = "browser.play", "media"
			arguments["expected_page_url"] = currentURL
			arguments["verify_playback"] = true
			postcondition = map[string]any{"media_playing": true}
		}
		if candidate.URL != "" {
			if capability == "browser.navigate" {
				arguments["url"] = candidate.URL
			} else {
				arguments["target_url"] = candidate.URL
			}
		}
		if browserRefPattern.MatchString(candidate.Ref) {
			arguments["ref"] = candidate.Ref
		}
		if browserStringValue(arguments["url"])+browserStringValue(arguments["target_url"])+browserStringValue(arguments["ref"]) == "" {
			continue
		}
		description := fmt.Sprintf("Use this observed candidate (confidence %.0f%%).", candidate.Confidence*100)
		result = append(result, newBrowserSuggestion(
			sessionID, currentURL, element, candidate.Label, description, kind, capability, arguments, postcondition,
		))
	}
	return result
}

func semanticBrowserSuggestions(sessionID, currentURL string, state map[string]any) []browserSuggestedAction {
	interactions := perception.InteractionCandidates(state)
	page, hasPage := perception.SemanticPage(state)
	result := make([]browserSuggestedAction, 0, 4)
	for _, interaction := range interactions {
		if len(result) >= 4 {
			break
		}
		if isLowValueBrowserInteraction(interaction) {
			continue
		}
		capability := ""
		arguments := map[string]any{
			"expected_page_url": currentURL,
			"target_label":      interaction.Label,
			"snapshot":          true,
		}
		kind := "navigate"
		switch interaction.Kind {
		case "play", "play_current", "media_play", "media_next", "media_previous":
			capability = "browser.play"
			kind = "media"
			arguments["verify_playback"] = true
		case "open":
			if interaction.Ref != "" {
				capability = "browser.click"
			} else if interaction.TargetURL != "" {
				capability = "browser.navigate"
				arguments["url"] = interaction.TargetURL
				arguments["open_mode"] = "current"
			}
		default:
			continue
		}
		if interaction.Ref != "" {
			arguments["ref"] = interaction.Ref
		}
		if interaction.TargetURL != "" {
			arguments["target_url"] = interaction.TargetURL
		}
		if hasPage && strings.EqualFold(strings.TrimSpace(page.Type), "profile_selection") && capability == "browser.click" {
			arguments["wait_for_document_transition"] = true
		}
		if capability == "" || (interaction.Ref == "" && interaction.TargetURL == "") {
			continue
		}
		element := browserSemanticElement{Ref: interaction.Ref, Label: interaction.Label, URL: interaction.TargetURL}
		action := newBrowserSuggestion(
			sessionID, currentURL, element, interaction.Label, interaction.Description,
			kind, capability, arguments, interaction.Postcondition,
		)
		if interaction.Risk != "" {
			action.Risk = interaction.Risk
		}
		result = append(result, action)
	}
	return result
}

func browserInterventionRequired(state map[string]any) bool {
	if state == nil {
		return false
	}
	required, _ := state["user_intervention_required"].(bool)
	return required || browserChallengeDetected(state)
}

func isLowValueBrowserInteraction(interaction perception.Interaction) bool {
	return isLowValueBrowserTarget(interaction.Label, interaction.TargetURL)
}

func isLowValueBrowserTarget(rawLabel, targetURL string) bool {
	label := strings.ToLower(strings.Join(strings.Fields(rawLabel), " "))
	if label == "" {
		return true
	}
	if strings.Contains(label, "©") || strings.Contains(label, "all rights reserved") || strings.Contains(label, "保留所有权利") || strings.Contains(label, "版权所有") {
		return true
	}
	if auxiliaryContentLabel.MatchString(label) || strings.HasPrefix(label, "google account:") {
		return true
	}
	for _, utility := range []string{
		"privacy policy", "cookie policy", "terms of service", "terms and conditions", "acceptable use policy",
		"隐私政策", "隐私声明", "服务条款", "用户协议", "使用条款", "营业执照",
	} {
		if label == utility {
			return true
		}
	}
	if strings.HasSuffix(label, "'s profile") || strings.HasSuffix(label, "’s profile") || strings.HasSuffix(label, "'s avatar") || strings.HasSuffix(label, "’s avatar") {
		return true
	}
	parsed, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil {
		return false
	}
	if isBrowserIdentityFlowURL(parsed) {
		return true
	}
	host := strings.ToLower(parsed.Hostname())
	pathAndQuery := strings.ToLower(parsed.EscapedPath() + "?" + parsed.RawQuery)
	if (host == "facebook.com" || strings.HasSuffix(host, ".facebook.com") || host == "x.com" || strings.HasSuffix(host, ".x.com") || host == "twitter.com" || strings.HasSuffix(host, ".twitter.com") || host == "linkedin.com" || strings.HasSuffix(host, ".linkedin.com")) &&
		(strings.Contains(pathAndQuery, "share") || strings.Contains(pathAndQuery, "intent") || strings.Contains(pathAndQuery, "sharer")) {
		return true
	}
	path := strings.ToLower(strings.Trim(parsed.Path, "/"))
	for _, prefix := range []string{"privacy", "policies", "policy", "terms", "legal", "copyright", "cookies", "preferences", "preference", "settings", "personalization", "personalisation", "account", "accounts", "u", "user", "users", "profile", "profiles", "member", "members", "people"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func isBrowserIdentityFlowURL(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	path := strings.ToLower(strings.Trim(parsed.Path, "/"))
	first, _, _ := strings.Cut(path, "/")
	switch first {
	case "login", "log-in", "logout", "log-out", "signin", "sign-in", "signup", "sign-up",
		"register", "registration", "join", "enter", "auth", "oauth", "onboarding":
		return true
	}
	for key, values := range parsed.Query() {
		lowerKey := strings.ToLower(strings.TrimSpace(key))
		identityKey := false
		for _, marker := range []string{"login", "signin", "signup", "register", "registration", "onboarding", "oauth"} {
			if strings.Contains(lowerKey, marker) {
				identityKey = true
				break
			}
		}
		if !identityKey {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return true
			}
		}
	}
	return false
}

func isQQMusicPlayerURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), "y.qq.com") && strings.Contains(strings.ToLower(parsed.Path), "/player")
}

func browserTransportAvailable(state map[string]any, label string) bool {
	playback, _ := state["playback"].(map[string]any)
	key := ""
	switch label {
	case "Next track":
		key = "can_next"
	case "Previous track":
		key = "can_previous"
	}
	if key == "" {
		return true
	}
	available, known := playback[key].(bool)
	return !known || available
}

func browserPlaybackIsActive(state map[string]any) bool {
	playback, _ := state["playback"].(map[string]any)
	playing, _ := playback["playing"].(bool)
	verified, _ := playback["verified"].(bool)
	return playing && verified
}

func browserTransportControl(element browserSemanticElement) (string, string, bool) {
	title := strings.ToLower(strings.TrimSpace(browserElementTitle(element)))
	switch title {
	case "next", "next track", "next song", "下一首":
		return "Next track", "Play the next track and verify playback.", true
	case "previous", "previous track", "previous song", "上一首":
		return "Previous track", "Play the previous track and verify playback.", true
	default:
		return "", "", false
	}
}

func appendActionWithKey(result *[]browserSuggestedAction, seen map[string]bool, key string, action browserSuggestedAction, limit int) {
	if len(*result) >= limit || action.ID == "" || seen[action.ID] {
		return
	}
	seen[key] = true
	seen[action.ID] = true
	*result = append(*result, action)
}

func newBrowserSuggestion(sessionID, currentURL string, element browserSemanticElement, label, description, kind, capability string, arguments, postcondition map[string]any) browserSuggestedAction {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "Continue"
	}
	seed := strings.Join([]string{sessionID, normalizeBrowserPageURL(currentURL), element.Ref, browserStringValue(arguments["target_url"]), capability}, "\x00")
	sum := sha256.Sum256([]byte(seed))
	return browserSuggestedAction{
		Schema:      browserSuggestionSchema,
		ID:          "browser-option-" + hex.EncodeToString(sum[:8]),
		Label:       label,
		Description: description,
		Kind:        kind,
		Capability:  capability,
		Arguments:   arguments,
		Risk:        "LOW",
		Precondition: map[string]any{
			"session_id": sessionID,
			"page_url":   currentURL,
		},
		Postcondition: postcondition,
	}
}

func browserElementTitle(element browserSemanticElement) string {
	if match := browserQuotedLabelPattern.FindStringSubmatch(element.Label); len(match) > 1 {
		return truncateBrowserLabel(match[1])
	}
	label := element.Label
	if index := strings.Index(strings.ToLower(label), "[url="); index >= 0 {
		label = label[:index]
	}
	for _, prefix := range []string{"link ", "button ", "checkbox ", "menuitem "} {
		label = strings.TrimPrefix(strings.TrimSpace(label), prefix)
	}
	return truncateBrowserLabel(strings.Trim(label, ` "`))
}

func truncateBrowserLabel(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if len(runes) > 72 {
		return string(runes[:69]) + "..."
	}
	return value
}

func resolveBrowserElementURL(currentURL, target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	base, err := url.Parse(strings.TrimSpace(currentURL))
	if err != nil || base.Host == "" {
		return ""
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(parsed)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return ""
	}
	if !sameBrowserHost(base.Hostname(), resolved.Hostname()) {
		return ""
	}
	resolved.Fragment = ""
	return resolved.String()
}

func sameBrowserHost(left, right string) bool {
	left = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(left)), "www.")
	right = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(right)), "www.")
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}
	leftSite, leftErr := publicsuffix.EffectiveTLDPlusOne(left)
	rightSite, rightErr := publicsuffix.EffectiveTLDPlusOne(right)
	return leftErr == nil && rightErr == nil && leftSite == rightSite
}

func isMusicResultElement(currentURL string, element browserSemanticElement, targetURL string) bool {
	host := strings.ToLower(browserURLHost(currentURL))
	if !strings.Contains(host, "music") && !strings.Contains(host, "spotify") && !strings.Contains(host, "qq.com") {
		return false
	}
	label := strings.ToLower(element.Label)
	path := strings.ToLower(targetURL)
	if containsBrowserNavigationNoise(label) {
		return false
	}
	for _, marker := range []string{"/song", "/track", "/watch", "songid=", "mid="} {
		if strings.Contains(path, marker) {
			return true
		}
	}
	return strings.Contains(label, "play") || strings.Contains(label, "播放")
}

func isSafeSuggestedElement(element browserSemanticElement) bool {
	label := strings.ToLower(strings.TrimSpace(element.Label))
	if element.Ref == "" || label == "" || containsBrowserNavigationNoise(label) {
		return false
	}
	return strings.Contains(label, "link") || strings.Contains(label, "button")
}

func containsBrowserNavigationNoise(label string) bool {
	for _, marker := range []string{
		"home", "shorts", "subscription", "history", "liked video", "settings", "account", "sign in", "log out",
		"youtube music", "youtube kids", "try premium", "your videos", "watch later", "playlists", "downloads",
		"clear search", "skip navigation", `button "guide"`, "search with your voice", `button "search"`, "notifications", "create",
		"delete", "remove", "unsubscribe", "like this", "dislike", "buy now", "checkout", "purchase", "place order",
		"submit", "send", "book now", "pay now", "confirm", "accept terms", "agree and continue",
		"首页", "主页", "历史记录", "设置", "退出", "删除", "取消订阅", "购买", "结账", "下单", "提交", "发送", "预订", "支付", "确认订单", "同意并继续",
	} {
		if strings.Contains(label, marker) {
			return true
		}
	}
	return false
}

func isMediaCatalogPage(state map[string]any) bool {
	if page, ok := state["page"].(map[string]any); ok && browserStringValue(page["type"]) == "media_catalog" {
		return true
	}
	return isYouTubePage(state) || isQQMusicPage(state)
}

func browserURLHost(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func normalizeBrowserPageURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		return strings.TrimSpace(value)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	if parsed.Path != "/" {
		parsed.Path = strings.TrimRight(parsed.Path, "/")
	}
	query := parsed.Query()
	for _, key := range []string{"reload", "pbj"} {
		query.Del(key)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}
