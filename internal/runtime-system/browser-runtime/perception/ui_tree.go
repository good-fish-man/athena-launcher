package perception

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
)

func buildUITree(raw map[string]any, elements []Element) UITree {
	rootID := stableUnderstandingID("page", stringValue(raw["url"]), stringValue(raw["title"]))
	tree := UITree{Schema: uiTreeSchema, RootID: rootID, Nodes: make([]UINode, 0, len(elements)+4)}
	regions := make(map[string]string)

	sorted := append([]Element(nil), elements...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Order < sorted[j].Order })
	for _, element := range sorted {
		region := inferUIRegion(element)
		parentID := rootID
		if region != "main" {
			parentID = regions[region]
			if parentID == "" {
				parentID = stableUnderstandingID("region", rootID, region)
				regions[region] = parentID
				tree.Nodes = append(tree.Nodes, UINode{
					ID: parentID, ParentID: rootID, Role: "region", Kind: "region",
					Name: region, Region: region, Order: -1,
				})
			}
		}
		name := cleanElementName(element.Name, element.Label)
		currentURL := elementURL(element.Label)
		kind, signals := inferUINodeKind(element.Role, name, currentURL)
		state := make(map[string]any, 3)
		if element.Focused {
			state["focused"] = true
		}
		if element.Disabled {
			state["disabled"] = true
		}
		if isSelectedElement(element.Label) {
			state["selected"] = true
		}
		node := UINode{
			ID:  stableUnderstandingID("node", element.Ref, element.Role, name, currentURL),
			Ref: element.Ref, ParentID: parentID, Role: normalizedRole(element.Role), Kind: kind,
			Name: name, URL: currentURL, Region: region, Order: element.Order, Box: element.Box,
			State: state, Signals: signals,
		}
		tree.Nodes = append(tree.Nodes, node)
		if isInteractiveRole(node.Role) {
			tree.InteractiveCount++
		}
		if node.Box != nil {
			tree.LocatedCount++
		}
	}
	return tree
}

func stableUnderstandingID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "ui-" + hex.EncodeToString(sum[:8])
}

func normalizedRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" || role == "element" {
		return "generic"
	}
	return role
}

func cleanElementName(name, label string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSpace(label)
	}
	if first := strings.Index(name, `"`); first >= 0 {
		if second := strings.Index(name[first+1:], `"`); second >= 0 {
			return compactElementName(name[first+1 : first+1+second])
		}
	}
	if index := strings.Index(strings.ToLower(name), "[url="); index >= 0 {
		name = name[:index]
	}
	for _, prefix := range []string{"button ", "link ", "textbox ", "combobox ", "menuitem ", "tab ", "option ", "heading ", "img ", "image "} {
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			name = strings.TrimSpace(name[len(prefix):])
			break
		}
	}
	return compactElementName(strings.Trim(name, ` "`))
}

func compactElementName(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > 160 {
		return string(runes[:157]) + "..."
	}
	return value
}

func elementURL(label string) string {
	lower := strings.ToLower(label)
	index := strings.Index(lower, "url=")
	if index < 0 {
		return ""
	}
	value := strings.TrimSpace(label[index+len("url="):])
	if end := strings.IndexAny(value, "] ,\t\r\n"); end >= 0 {
		value = value[:end]
	}
	return strings.Trim(value, `"'`)
}

func inferUIRegion(element Element) string {
	label := strings.ToLower(element.Label)
	if containsAny(label, "navigation", "navbar", "menuitem") {
		return "navigation"
	}
	if element.Box == nil {
		return "main"
	}
	if element.Box.Y >= 0 && element.Box.Y < 180 {
		return "header"
	}
	if element.Box.X >= 0 && element.Box.X < 280 && element.Box.Y < 900 {
		return "sidebar"
	}
	return "main"
}

func inferUINodeKind(role, name, targetURL string) (string, []string) {
	role = normalizedRole(role)
	lower := strings.ToLower(name)
	signals := make([]string, 0, 4)
	switch role {
	case "textbox", "combobox":
		if hasSearchSignal(lower) {
			return "search_input", []string{"search_semantics"}
		}
		if containsAny(lower, "password", "密码") {
			return "password_input", []string{"credential_semantics"}
		}
		return "input", nil
	case "button", "switch", "checkbox", "radio":
		if transport := transportKind(lower); transport != "" {
			return "media_control", []string{transport}
		}
		if hasSearchSignal(lower) {
			return "search_control", []string{"search_semantics"}
		}
		return "control", nil
	case "link":
		kind := inferLinkedEntityKind(targetURL, name)
		if kind != "link" {
			signals = append(signals, kind+"_url_pattern")
		}
		return kind, signals
	case "menuitem", "tab", "option":
		return "navigation", nil
	case "img", "image":
		return "visual", nil
	case "heading":
		return "heading", nil
	default:
		return "content", nil
	}
}

func inferLinkedEntityKind(rawURL, name string) string {
	parsed, _ := url.Parse(strings.TrimSpace(rawURL))
	path := strings.ToLower(parsed.Path)
	query := strings.ToLower(parsed.RawQuery)
	label := strings.ToLower(name)
	switch {
	case isRepositoryLink(parsed, label):
		return "repository"
	case hasURLPathSemantic(path, "watch", "video", "videos", "video-detail", "video_detail") || hasURLQueryKey(parsed, "v", "video", "video_id"):
		return "video"
	case hasURLPathSemantic(path, "song", "songs", "track", "tracks", "audio", "song-detail", "song_detail", "track-detail", "track_detail") || hasURLQueryKey(parsed, "song", "song_id", "songid", "track", "track_id", "trackid", "audio_id"):
		return "audio"
	case hasURLPathSemantic(path, "playlist", "playlists", "album", "albums", "collection", "collections"):
		return "media_collection"
	case containsAny(path+" "+query+" "+label, "/thread", "/topic", "/discussion", "/comments", "/question", "/post/", "帖子", "话题", "讨论"):
		return "discussion"
	case containsAny(path+" "+query+" "+label, "/product", "/products", "/item", "/goods", "/dp/", "productid=", "商品", "价格"):
		return "product"
	case containsAny(path+" "+query+" "+label, "/article", "/news", "/story", "/blog", "articleid=", "新闻", "文章"):
		return "article"
	case containsAny(path+" "+query+" "+label, "/download", ".zip", ".tar", ".dmg", ".exe", ".pdf", "下载"):
		return "download"
	default:
		return "link"
	}
}

func isRepositoryLink(parsed *url.URL, label string) bool {
	if parsed == nil {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	reserved := map[string]bool{
		"account": true, "accounts": true, "article": true, "articles": true, "blog": true,
		"category": true, "categories": true, "collections": true, "docs": true, "explore": true,
		"login": true, "logout": true, "marketplace": true, "news": true, "orgs": true,
		"post": true, "posts": true, "profile": true, "profiles": true, "questions": true,
		"r": true, "search": true, "settings": true, "signup": true, "tag": true, "tags": true,
		"topic": true, "topics": true, "u": true, "user": true, "users": true, "watch": true,
	}
	if reserved[strings.ToLower(parts[0])] {
		return false
	}
	want := strings.ToLower(parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"))
	label = strings.TrimSpace(strings.TrimSuffix(label, ".git"))
	label = strings.ReplaceAll(strings.ReplaceAll(label, " /", "/"), "/ ", "/")
	return label == want
}

func hasURLPathSemantic(path string, markers ...string) bool {
	for _, part := range strings.Split(strings.Trim(strings.ToLower(path), "/"), "/") {
		for _, marker := range markers {
			if part == marker || strings.HasPrefix(part, marker+"-") || strings.HasPrefix(part, marker+"_") ||
				strings.HasPrefix(part, marker+"detail") {
				return true
			}
		}
	}
	return false
}

func hasURLQueryKey(parsed *url.URL, keys ...string) bool {
	if parsed == nil {
		return false
	}
	query := parsed.Query()
	for _, key := range keys {
		if strings.TrimSpace(query.Get(key)) != "" {
			return true
		}
	}
	return false
}

func isInteractiveRole(role string) bool {
	switch normalizedRole(role) {
	case "button", "link", "textbox", "combobox", "checkbox", "radio", "menuitem", "tab", "option", "slider", "switch":
		return true
	default:
		return false
	}
}

func isSelectedElement(label string) bool {
	lower := strings.ToLower(label)
	return containsAny(lower, " selected", " current", " active", "已选择", "当前")
}

func hasSearchSignal(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return containsAny(value, "search", "find", "query", "搜索", "搜尋", "查找", "查询")
}

func transportKind(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch {
	case exactSemanticLabel(value, "play", "播放", "start", "resume", "继续播放"):
		return "play"
	case exactSemanticLabel(value, "pause", "暂停"):
		return "pause"
	case exactSemanticLabel(value, "next", "next track", "next song", "下一首", "下一个"):
		return "next"
	case exactSemanticLabel(value, "previous", "previous track", "previous song", "上一首", "上一个"):
		return "previous"
	default:
		return ""
	}
}

func exactSemanticLabel(value string, options ...string) bool {
	value = strings.Trim(strings.TrimSpace(value), ` "`)
	for _, option := range options {
		if value == option || strings.HasPrefix(value, option+" ") || strings.HasPrefix(value, option+" (") {
			return true
		}
	}
	return false
}
