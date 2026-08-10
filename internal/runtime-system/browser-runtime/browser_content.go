package browser_runtime

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

const universalContentCandidatesScript = `(() => {
  const clean = value => String(value || "").replace(/\s+/g, " ").trim();
  const visible = element => {
    if (!element) return false;
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return rect.width > 2 && rect.height > 2 && style.display !== "none" && style.visibility !== "hidden";
  };
  const rejected = /^(?:home|new|past|comments?|ask|show|jobs|submit|login|log in|sign in|sign up|profile|settings|menu|more|next|previous|hide|reply|share(?:\s+(?:to|on|via|this)\b.*)?|follow(?:\s+us)?|save(?:\s+(?:article|story|post))?|subscribe|print(?:\s+(?:article|story|page))?|copy\s+link|open in app|manage preferences|首页|主页|登录|注册|菜单|设置|更多|下一页|上一页|回复|分享(?:到|至|本文)?|关注|收藏|订阅|打印|复制链接)$/i;
  const metadata = /^(?:\d+[.)]?|\d+\s*(?:points?|comments?|views?|replies?)\b|by\s+\S+|\d+\s*(?:分钟前|小时前|天前|评论|回复|浏览))/i;
  const linkShape = anchor => {
    try {
      const target = new URL(anchor.href, location.href);
      const keys = [...new Set([...target.searchParams.keys()].filter(key => !/^(?:utm_.+|ref|source)$/i.test(key)))].sort();
      return target.origin + target.pathname + (keys.length ? "?" + keys.join("&") : "");
    } catch (_) { return ""; }
  };
  const repeatedShapes = new Map();
  for (const anchor of document.querySelectorAll("a[href]")) {
    if (!visible(anchor) || anchor.closest('header,nav,footer,[role="navigation"]')) continue;
    const shape = linkShape(anchor);
    if (shape) repeatedShapes.set(shape, (repeatedShapes.get(shape) || 0) + 1);
  }
  const items = new Map();
  let order = 0;
  for (const anchor of document.querySelectorAll("a[href]")) {
    order += 1;
    if (!visible(anchor) || anchor.closest('header,nav,footer,[role="navigation"]')) continue;
    const title = clean(anchor.getAttribute("title") || anchor.getAttribute("aria-label") || anchor.innerText || anchor.textContent);
    if (!title || rejected.test(title) || metadata.test(title)) continue;
    let target;
    try { target = new URL(anchor.href, location.href); } catch (_) { continue; }
    if (!/^https?:$/.test(target.protocol)) continue;
    target.hash = "";
    const current = new URL(location.href); current.hash = "";
    if (target.href === current.href) continue;
    const path = target.pathname.toLowerCase();
    if (/(?:^|\/)(?:u|users?|profiles?|accounts?|authors?|members?|people|login|logout|signup|register|from|hide|vote|reply)(?:\/|$)/.test(path)) continue;
    if (/(?:'s|’s)?\s*(?:profile|avatar)$/i.test(title)) continue;
    if (/^(?:www\.)?[a-z0-9.-]+\.[a-z]{2,}(?:\.[a-z]{2,})?$/i.test(title)) continue;
    const heading = anchor.closest('h1,h2,h3,h4,h5,h6') || anchor.querySelector('h1,h2,h3,h4,h5,h6');
    const paragraph = anchor.closest('p');
    const article = anchor.closest('article');
    const card = anchor.closest('li,tr,td,[role="listitem"],[role="row"],[role="gridcell"],[data-testid*="card"],[data-testid*="post"],[class*="card"],[class*="item"],[class*="post"]') || (article && !paragraph ? article : null);
    if (card && !heading) {
      const primary = [...card.querySelectorAll('a[href]')].find(candidate => {
        if (!visible(candidate)) return false;
        const candidateTitle = clean(candidate.getAttribute("title") || candidate.getAttribute("aria-label") || candidate.innerText || candidate.textContent);
        if (!candidateTitle || rejected.test(candidateTitle) || metadata.test(candidateTitle)) return false;
        return !!(candidate.closest('h1,h2,h3,h4,h5,h6') || candidate.querySelector('h1,h2,h3,h4,h5,h6')) || candidateTitle.length >= 12;
      });
      if (primary && primary !== anchor) continue;
    }
    const articleBody = !card && !!paragraph && !!anchor.closest('article,main,[role="main"]');
    const repeated = (repeatedShapes.get(linkShape(anchor)) || 0) >= 3;
    const context = heading ? "heading" : card || repeated ? "collection" : articleBody ? "article_body" : "standalone";
    const structured = context === "collection" || context === "heading";
    if (title.length < 12 && !(structured && title.split(/\s+/).length >= 3)) continue;
    if (/\.(?:png|jpe?g|gif|webp|svg)(?:$|\?)/i.test(target.pathname + target.search)) continue;
    const rect = anchor.getBoundingClientRect();
    const score = context === "heading" ? 7 : context === "collection" ? 5 : context === "standalone" ? 2 : 0;
    const candidate = { title, url: target.href, position: Math.max(0, Math.round(rect.top + scrollY)), order, structured, context, score };
    const previous = items.get(target.href);
    if (!previous || candidate.score > previous.score || (candidate.score === previous.score && candidate.title.length > previous.title.length)) {
      items.set(target.href, candidate);
    }
  }
  return { candidates: [...items.values()].sort((a, b) => b.score - a.score || a.position - b.position || a.order - b.order).slice(0, 32) };
})()`

type browserContentCandidate struct {
	Title      string
	URL        string
	Position   int
	Order      int
	Context    string
	Score      int
	Structured bool
}

var (
	bareHostnameContentLabel = regexp.MustCompile(`(?i)^(?:www\.)?[a-z0-9.-]+\.[a-z]{2,}(?:\.[a-z]{2,})?$`)
	metadataContentLabel     = regexp.MustCompile(`(?i)^(?:\d+[.)]?|\d+\s*(?:points?|comments?|views?|replies?)\b|by\s+\S+)`)
	auxiliaryContentLabel    = regexp.MustCompile(`(?i)^(?:https?://|prefer\s+.+\s+on\s+google\b|add\s+as\s+preferred\s+on\s+google\b|source\s+preferences\b|open\s+in\s+(?:the\s+)?app\b|manage\s+.+(?:preferences|personalisation|personalization)\b|share\s+(?:to|on|via|this)\b|follow(?:\s+us)?\b|save\s+(?:article|story|post)\b|print\s+(?:article|story|page)\b|copy\s+link\b|subscribe\b|分享(?:到|至|本文)?\b|关注\b|收藏\b|打印\b|复制链接\b)`)
)

func enrichBrowserContentCandidates(run browserCommandRunner, sessionArgs []string, state map[string]any) map[string]any {
	if run == nil {
		return state
	}
	output, err := run(10*time.Second, append(sessionArgs, "eval", universalContentCandidatesScript, "--json")...)
	if err != nil {
		return state
	}
	candidates := parseBrowserContentCandidates(output, browserStringValue(state["url"]))
	if len(candidates) == 0 {
		delete(state, "content_candidates")
		return state
	}
	return applyBrowserContentCandidates(state, candidates)
}

func applyBrowserContentCandidates(state map[string]any, candidates []browserContentCandidate) map[string]any {
	if len(candidates) == 0 {
		delete(state, "content_candidates")
		return state
	}
	items := make([]map[string]any, 0, len(candidates))
	for _, candidate := range candidates {
		items = append(items, map[string]any{
			"title": candidate.Title, "url": candidate.URL, "position": candidate.Position, "order": candidate.Order,
			"context": candidate.Context, "score": candidate.Score, "structured": candidate.Structured,
		})
	}
	state["content_candidates"] = items
	return state
}

func parseBrowserContentCandidates(output, currentURL string) []browserContentCandidate {
	var decoded any
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &decoded) != nil {
		return nil
	}
	raw := findBrowserMediaCandidateValues(decoded)
	result := make([]browserContentCandidate, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	current := normalizeBrowserPageURL(currentURL)
	currentParsed, _ := url.Parse(strings.TrimSpace(currentURL))
	for _, item := range raw {
		title := truncateBrowserLabel(browserStringValue(item["title"]))
		resolved := resolveObservedContentURL(currentURL, browserStringValue(item["url"]))
		parsed, err := url.Parse(resolved)
		normalized := normalizeBrowserPageURL(resolved)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" ||
			title == "" || normalized == "" || normalized == current || seen[normalized] || isPromotionalMediaTitle(title) {
			continue
		}
		if isAuxiliaryContentCandidate(title, parsed, currentParsed) {
			continue
		}
		seen[normalized] = true
		context := strings.ToLower(strings.TrimSpace(browserStringValue(item["context"])))
		structured, _ := item["structured"].(bool)
		score := int(int64Argument(item["score"]))
		if score == 0 && context != "article_body" {
			score = defaultBrowserContentScore(context, structured)
		}
		result = append(result, browserContentCandidate{
			Title: title, URL: resolved, Position: int(int64Argument(item["position"])), Order: int(int64Argument(item["order"])),
			Context: context, Score: score, Structured: structured,
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		if result[i].Position != result[j].Position {
			return result[i].Position < result[j].Position
		}
		return result[i].Order < result[j].Order
	})
	return result
}

func defaultBrowserContentScore(context string, structured bool) int {
	switch context {
	case "heading":
		return 7
	case "collection":
		return 5
	case "article_body":
		return 0
	case "standalone":
		return 2
	default:
		if structured {
			return 4
		}
		return 1
	}
}

func resolveObservedContentURL(currentURL, target string) string {
	base, err := url.Parse(strings.TrimSpace(currentURL))
	if err != nil || base.Hostname() == "" {
		return ""
	}
	parsed, err := url.Parse(strings.TrimSpace(target))
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(parsed)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return ""
	}
	resolved.Fragment = ""
	return resolved.String()
}

func isAuxiliaryContentCandidate(title string, parsed, current *url.URL) bool {
	title = strings.TrimSpace(title)
	if parsed == nil || bareHostnameContentLabel.MatchString(title) || metadataContentLabel.MatchString(title) || auxiliaryContentLabel.MatchString(title) {
		return true
	}
	if isBrowserIdentityFlowURL(parsed) {
		return true
	}
	lowerTitle := strings.ToLower(title)
	if strings.HasSuffix(lowerTitle, "'s profile") || strings.HasSuffix(lowerTitle, "’s profile") || strings.HasSuffix(lowerTitle, "'s avatar") || strings.HasSuffix(lowerTitle, "’s avatar") {
		return true
	}
	if isSearchFacetContentCandidate(parsed, current) {
		return true
	}
	if len([]rune(title)) < 12 && len(strings.Fields(title)) < 3 {
		return true
	}
	parts := strings.Split(strings.Trim(strings.ToLower(parsed.Path), "/"), "/")
	if len(parts) == 0 {
		return false
	}
	switch parts[0] {
	case "u", "user", "users", "profile", "profiles", "account", "accounts", "author", "authors", "member", "members", "people", "preferences", "preference", "personalization", "personalisation", "settings", "from", "hide", "vote", "reply":
		return true
	default:
		return false
	}
}

func isSearchFacetContentCandidate(candidate, current *url.URL) bool {
	if candidate == nil || current == nil || !strings.EqualFold(candidate.Hostname(), current.Hostname()) ||
		strings.TrimRight(candidate.Path, "/") != strings.TrimRight(current.Path, "/") {
		return false
	}
	currentQuery := current.Query()
	candidateQuery := candidate.Query()
	matchedSearch := false
	for _, key := range []string{"q", "query", "search", "keyword", "keywords", "w"} {
		value := strings.TrimSpace(currentQuery.Get(key))
		if value != "" && strings.EqualFold(value, strings.TrimSpace(candidateQuery.Get(key))) {
			matchedSearch = true
			break
		}
	}
	if !matchedSearch {
		return false
	}
	for _, key := range []string{"type", "filter", "category", "tab", "scope"} {
		if value := strings.TrimSpace(candidateQuery.Get(key)); value != "" && !strings.EqualFold(value, strings.TrimSpace(currentQuery.Get(key))) {
			return true
		}
	}
	return false
}
