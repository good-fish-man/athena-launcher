package browser_runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"

	"athena-launcher/internal/runtime-system/browser-runtime/siteknowledge"
)

const universalMediaCandidatesScript = `(() => {
  const clean = value => String(value || "").replace(/\s+/g, " ").trim();
  const visible = element => {
    if (!element) return false;
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return rect.width > 2 && rect.height > 2 && style.display !== "none" && style.visibility !== "hidden";
  };
  const label = element => clean(element && (element.getAttribute("title") || element.getAttribute("aria-label") || element.textContent));
  const elementSignal = element => clean([
    element && element.getAttribute("class"), element && element.getAttribute("id"),
    element && element.getAttribute("role"), element && element.getAttribute("itemprop"),
    element && element.getAttribute("data-testid"), element && element.getAttribute("data-type")
  ].filter(Boolean).join(" ")).toLowerCase();
	const ancestrySignal = (element, stop) => {
		const nodes = [];
		for (let current = element, depth = 0; current && depth < 6; current = current.parentElement, depth += 1) {
			nodes.push(current);
			if (current === stop) break;
		}
		return clean(nodes.map(elementSignal).filter(Boolean).join(" ")).toLowerCase();
  };
  const sameTarget = (left, right) => {
    try {
      const a = new URL(left, location.href), b = new URL(right, location.href);
      a.hash = ""; b.hash = "";
      return a.href === b.href;
    } catch (_) { return false; }
  };
  const titleFor = (anchor, target, card) => {
    let title = label(anchor);
    if (title && !/^(?:play|播放)$/.test(title.toLowerCase())) return { title, node: anchor };
    for (const peer of card.querySelectorAll("a[href]")) {
      if (peer !== anchor && visible(peer) && sameTarget(peer.href, target.href)) {
        title = label(peer);
        if (title && !/^(?:play|播放)$/.test(title.toLowerCase())) return { title, node: peer };
      }
    }
    const heading = card.querySelector('h1,h2,h3,h4,[role="heading"]');
    title = label(heading);
    return { title, node: heading || anchor };
  };
  const classify = (target, anchor, card) => {
    const path = target.pathname.toLowerCase();
    const query = target.search.toLowerCase();
    const anchorSignal = elementSignal(anchor);
    if (/(?:username|author|creator|channel|profile|avatar)/.test(anchorSignal)) return "";
    if (/(?:^|\/)(?:watch|videos?|video[-_]?detail)(?:\/|$)/.test(path) || /[?&](?:v|video|video_id)=/.test(query)) return "video";
    if (/(?:^|\/)(?:songs?|tracks?|audio|song[-_]?detail|track[-_]?detail)(?:\/|$)/.test(path) || /[?&](?:song|song_id|track|track_id|audio_id)=/.test(query)) return "audio";
    const media = card.querySelector("video,audio");
    if (media) return media.tagName.toLowerCase();
		const signal = ancestrySignal(anchor, card);
		if (/(?:audible|audio|song|music)/.test(signal) || (/(?:track)/.test(signal) && /(?:playable|media|sound|waveform)/.test(signal))) return "audio";
    if (/(?:video|movie|clip|watchable)/.test(signal)) return "video";
    return "";
  };
  const items = new Map();
  let order = 0;
  for (const anchor of document.querySelectorAll("a[href]")) {
	const currentOrder = order++;
    if (!visible(anchor) || anchor.closest('nav,[role="navigation"]')) continue;
    let target;
    try { target = new URL(anchor.href, location.href); } catch (_) { continue; }
    if (!/^https?:$/.test(target.protocol) || target.origin !== location.origin) continue;
    const card = anchor.closest('article,li,[role="listitem"],[role="gridcell"],[data-testid*="card"],[class*="card"],[class*="item"],[class*="tile"]') || anchor;
    const resolved = titleFor(anchor, target, card);
    if (!resolved.title) continue;
    const kind = classify(target, anchor, card);
    if (!kind) continue;
    target.hash = "";
    const url = target.href;
    const rect = resolved.node.getBoundingClientRect();
    const candidate = { id: url, title: resolved.title, url, kind, position: Math.max(0, Math.round(rect.top + scrollY)), order: currentOrder };
    const previous = items.get(url);
	if (!previous || candidate.title.length > previous.title.length) {
	  if (previous) candidate.order = Math.min(previous.order, candidate.order);
	  items.set(url, candidate);
	}
  }
	return { candidates: [...items.values()].sort((a, b) => a.position - b.position || a.order - b.order).slice(0, 20) };
})()`

type browserMediaCandidate struct {
	ID       string
	Title    string
	URL      string
	Kind     string
	Ref      string
	Position int
	Order    int
}

func enrichBrowserMediaCandidates(run browserCommandRunner, sessionArgs []string, state map[string]any) map[string]any {
	if run == nil {
		return state
	}
	output, err := run(12*time.Second, append(sessionArgs, "eval", universalMediaCandidatesScript, "--json")...)
	if err != nil {
		return state
	}
	candidates := parseBrowserMediaCandidates(output, browserStringValue(state["url"]))
	if len(candidates) == 0 {
		if knowledge, matched := siteknowledge.MatchURL(browserStringValue(state["url"])); matched && knowledge.CandidateScript != "" {
			output, err = run(12*time.Second, append(sessionArgs, "eval", knowledge.CandidateScript, "--json")...)
			if err == nil {
				candidates = parseBrowserMediaCandidates(output, browserStringValue(state["url"]))
			}
		}
	}
	if len(candidates) == 0 {
		delete(state, "media_candidates")
		return state
	}
	return applyBrowserMediaCandidates(state, candidates)
}

func applyBrowserMediaCandidates(state map[string]any, candidates []browserMediaCandidate) map[string]any {
	if len(candidates) == 0 {
		delete(state, "media_candidates")
		return state
	}
	items := make([]map[string]any, 0, len(candidates))
	for index := range candidates {
		candidate := &candidates[index]
		if candidate.Ref == "" {
			candidate.Ref = browserMediaCandidateRef(state, candidate.URL)
		}
		items = append(items, map[string]any{
			"id": candidate.ID, "title": candidate.Title, "url": candidate.URL, "kind": candidate.Kind, "ref": candidate.Ref,
			"position": candidate.Position, "order": candidate.Order,
		})
	}
	state["media_candidates"] = items
	return state
}

func parseBrowserMediaCandidates(output, currentURL string) []browserMediaCandidate {
	var decoded any
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &decoded) != nil {
		return nil
	}
	raw := findBrowserMediaCandidateValues(decoded)
	result := make([]browserMediaCandidate, 0, len(raw))
	seen := make(map[string]bool)
	for _, item := range raw {
		title := truncateBrowserLabel(browserStringValue(item["title"]))
		resolved := resolveBrowserElementURL(currentURL, browserStringValue(item["url"]))
		kind, mediaID := browserMediaIdentity(resolved)
		if hintedKind := strings.ToLower(browserStringValue(item["kind"])); kind == "" && (hintedKind == "video" || hintedKind == "audio") {
			kind = hintedKind
		}
		if mediaID == "" && kind != "" {
			mediaID = stableBrowserMediaID(resolved)
		}
		if title == "" || mediaID == "" || seen[kind+":"+mediaID] || isPromotionalMediaTitle(title) {
			continue
		}
		seen[kind+":"+mediaID] = true
		position := int(int64Argument(item["position"]))
		result = append(result, browserMediaCandidate{
			ID: mediaID, Title: title, URL: resolved, Kind: kind,
			Ref: browserStringValue(item["ref"]), Position: position, Order: int(int64Argument(item["order"])),
		})
	}
	return result
}

func isPromotionalMediaTitle(title string) bool {
	lower := strings.ToLower(strings.TrimSpace(title))
	for _, marker := range []string{"ad:", "sponsored", "advertisement", "promoted", "广告", "廣告", "広告"} {
		if strings.HasPrefix(lower, marker) || strings.Contains(lower, " "+marker+" ") {
			return true
		}
	}
	return false
}

func findBrowserMediaCandidateValues(value any) []map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		if candidates, ok := typed["candidates"].([]any); ok {
			return browserMediaCandidateMaps(candidates)
		}
		for _, key := range []string{"data", "result", "value"} {
			if nested, ok := typed[key]; ok {
				if candidates := findBrowserMediaCandidateValues(nested); len(candidates) > 0 {
					return candidates
				}
			}
		}
	case []any:
		return browserMediaCandidateMaps(typed)
	}
	return nil
}

func browserMediaCandidateMaps(values []any) []map[string]any {
	result := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			result = append(result, item)
		}
	}
	return result
}

func browserMediaCandidates(state map[string]any) []browserMediaCandidate {
	values, ok := state["media_candidates"].([]map[string]any)
	if !ok {
		if raw, rawOK := state["media_candidates"].([]any); rawOK {
			values = browserMediaCandidateMaps(raw)
		}
	}
	result := make([]browserMediaCandidate, 0, len(values))
	for _, value := range values {
		candidate := browserMediaCandidate{
			ID:       browserStringValue(value["id"]),
			Title:    browserStringValue(value["title"]),
			URL:      browserStringValue(value["url"]),
			Kind:     browserStringValue(value["kind"]),
			Ref:      browserStringValue(value["ref"]),
			Position: int(int64Argument(value["position"])),
			Order:    int(int64Argument(value["order"])),
		}
		kind, mediaID := browserMediaIdentity(candidate.URL)
		if candidate.Kind == "" {
			candidate.Kind = kind
		}
		if candidate.ID == "" {
			candidate.ID = mediaID
		}
		if candidate.Title != "" && candidate.Kind != "" && candidate.ID != "" {
			result = append(result, candidate)
		}
	}
	sort.SliceStable(result, func(left, right int) bool {
		if result[left].Position != result[right].Position {
			return result[left].Position < result[right].Position
		}
		if result[left].Order != result[right].Order {
			return result[left].Order < result[right].Order
		}
		return false
	})
	return result
}

func browserMediaCandidateRef(state map[string]any, targetURL string) string {
	currentURL := browserStringValue(state["url"])
	wanted := normalizeBrowserPageURL(resolveBrowserElementURL(currentURL, targetURL))
	if wanted == "" {
		return ""
	}
	for _, element := range browserSemanticElements(state) {
		if !browserRefPattern.MatchString(element.Ref) || strings.TrimSpace(element.URL) == "" {
			continue
		}
		observed := normalizeBrowserPageURL(resolveBrowserElementURL(currentURL, element.URL))
		if observed == wanted {
			return element.Ref
		}
	}
	return ""
}

func findNthBrowserMediaCandidate(state map[string]any, ordinal int) (browserMediaCandidate, bool) {
	if ordinal <= 0 {
		return browserMediaCandidate{}, false
	}
	candidates := browserMediaCandidates(state)
	if ordinal > len(candidates) {
		return browserMediaCandidate{}, false
	}
	return candidates[ordinal-1], true
}

func isYouTubePage(state map[string]any) bool {
	return strings.Contains(strings.ToLower(browserURLHost(browserStringValue(state["url"]))), "youtube.com")
}

func isQQMusicPage(state map[string]any) bool {
	return strings.EqualFold(browserURLHost(browserStringValue(state["url"])), "y.qq.com")
}

func browserMediaIdentity(address string) (string, string) {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || parsed.Hostname() == "" {
		return "", ""
	}
	path := strings.ToLower(parsed.Path)
	kind := ""
	switch {
	case containsPathSemantic(path, "watch", "video", "videos") || firstQueryValue(parsed, "v", "video", "video_id") != "":
		kind = "video"
	case containsPathSemantic(path, "song", "songs", "track", "tracks", "audio") || firstQueryValue(parsed, "song", "song_id", "track", "track_id", "audio_id") != "":
		kind = "audio"
	default:
		return "", ""
	}
	if id := firstQueryValue(parsed, "v", "video", "video_id", "song", "song_id", "track", "track_id", "audio_id"); id != "" {
		return kind, id
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) > 1 {
		last := strings.TrimSpace(parts[len(parts)-1])
		if last != "" && last != "watch" && last != "video" && last != "song" && last != "track" {
			return kind, last
		}
	}
	return kind, stableBrowserMediaID(address)
}

func containsPathSemantic(path string, markers ...string) bool {
	parts := strings.Split(strings.Trim(strings.ToLower(path), "/"), "/")
	for _, part := range parts {
		for _, marker := range markers {
			if part == marker || strings.HasPrefix(part, marker+"-") || strings.HasSuffix(part, "-"+marker) ||
				(len(marker) >= 4 && strings.Contains(part, marker)) {
				return true
			}
		}
	}
	return false
}

func firstQueryValue(parsed *url.URL, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(parsed.Query().Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func stableBrowserMediaID(address string) string {
	sum := sha256.Sum256([]byte(normalizeBrowserPageURL(address)))
	return hex.EncodeToString(sum[:8])
}

func qqMusicSongID(address string) string {
	parsed, err := url.Parse(strings.TrimSpace(address))
	if err != nil || !strings.EqualFold(parsed.Hostname(), "y.qq.com") || !strings.Contains(parsed.Path, "/songDetail/") {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}
