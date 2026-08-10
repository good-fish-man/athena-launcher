package siteknowledge

import (
	"net/url"
	"strings"
)

// Definition contains declarative site knowledge only. Browser execution and
// verification remain in the universal runtime.
type Definition struct {
	ID                 string
	DisplayName        string
	Aliases            []string
	Hosts              []string
	HomeURL            string
	SearchURLTemplate  string
	SearchURLTemplates map[string]string
	CandidateScript    string
	Playback           *PlaybackHints
	MediaContinuation  *MediaContinuationHints
}

// MediaContinuationHints declares how a short title should continue an
// already-active media session. It contains no selectors or execution logic;
// the universal browser runtime still performs and verifies every action.
type MediaContinuationHints struct {
	PreferredKinds []string
	OpenFirst      bool
	Play           bool
}

type PlaybackHints struct {
	Controls []ControlHint    `json:"controls,omitempty"`
	Player   *PlayerStateHint `json:"player,omitempty"`
}

type ControlHint struct {
	ID             string   `json:"id"`
	Selectors      []string `json:"selectors"`
	PlayingClasses []string `json:"playing_classes,omitempty"`
	WaitMS         int      `json:"wait_ms,omitempty"`
}

type PlayerStateHint struct {
	PlayingSelectors  []string `json:"playing_selectors,omitempty"`
	TimeSelectors     []string `json:"time_selectors,omitempty"`
	ItemSelector      string   `json:"item_selector,omitempty"`
	ActiveClasses     []string `json:"active_classes,omitempty"`
	MediaLinkSelector string   `json:"media_link_selector,omitempty"`
}

var catalog = []Definition{
	{
		ID: "youtube", DisplayName: "YouTube",
		Aliases: []string{"youtube", "you tube", "youtub"}, Hosts: []string{"youtube.com", "www.youtube.com"},
		HomeURL: "https://www.youtube.com", SearchURLTemplate: "https://www.youtube.com/results?search_query={query}",
		MediaContinuation: &MediaContinuationHints{PreferredKinds: []string{"video"}, OpenFirst: true, Play: true},
		CandidateScript:   `(()=>{const clean=value=>String(value||"").replace(/\s+/g," ").trim();const visible=element=>{if(!element)return false;const rect=element.getBoundingClientRect();return rect.width>2&&rect.height>2};const label=element=>clean(element&&(element.getAttribute("title")||element.getAttribute("aria-label")||element.textContent));const items=new Map();let order=0;for(const anchor of document.querySelectorAll('a[href*="/watch"]')){const currentOrder=order++;let target;try{target=new URL(anchor.href,location.href)}catch(_){continue}if(target.pathname!=="/watch"||!target.searchParams.get("v")||!visible(anchor))continue;const card=anchor.closest('article,li,[role="listitem"],ytd-video-renderer,ytd-rich-item-renderer,ytd-grid-video-renderer,ytd-compact-video-renderer,ytd-playlist-video-renderer')||anchor;const peers=[anchor,...card.querySelectorAll('a[href*="/watch"]')];let titleNode=peers.find(peer=>{try{const other=new URL(peer.href,location.href);return visible(peer)&&other.pathname==="/watch"&&other.searchParams.get("v")===target.searchParams.get("v")&&label(peer)}catch(_){return false}})||anchor;const title=label(titleNode);if(!title)continue;const url=target.origin+target.pathname+"?v="+encodeURIComponent(target.searchParams.get("v"));const rect=titleNode.getBoundingClientRect();const candidate={id:target.searchParams.get("v"),title,url,kind:"video",position:Math.max(0,Math.round(rect.top+scrollY)),order:currentOrder};const previous=items.get(url);if(!previous||candidate.title.length>previous.title.length){if(previous)candidate.order=Math.min(previous.order,candidate.order);items.set(url,candidate)}}return {candidates:[...items.values()].sort((a,b)=>a.position-b.position||a.order-b.order).slice(0,16)}})()`,
	},
	{
		ID: "qq-music", DisplayName: "QQ Music",
		Aliases: []string{"qq music", "qqmusic", "qq 音乐", "qq音乐"}, Hosts: []string{"y.qq.com"},
		HomeURL: "https://y.qq.com/", SearchURLTemplate: "https://y.qq.com/n/ryqq/search?w={query}",
		MediaContinuation: &MediaContinuationHints{PreferredKinds: []string{"audio"}, OpenFirst: true, Play: true},
		CandidateScript:   `(()=>{const clean=value=>String(value||"").replace(/\s+/g," ").trim();const items=new Map();for(const anchor of document.querySelectorAll('a[href*="/songDetail/"]')){let target;try{target=new URL(anchor.href,location.href)}catch(_){continue}const id=target.pathname.split("/").filter(Boolean).pop();if(!id)continue;const rect=anchor.getBoundingClientRect();if(rect.width<2||rect.height<2)continue;const title=clean(anchor.getAttribute("title")||anchor.getAttribute("aria-label")||anchor.textContent);if(!title||/^(play|播放)$/i.test(title))continue;const url=target.origin+target.pathname;const candidate={id,title,url,kind:"audio",position:Math.max(0,Math.round(rect.top+scrollY))};const previous=items.get(url);if(!previous||candidate.position<previous.position)items.set(url,candidate)}return {candidates:[...items.values()].sort((a,b)=>a.position-b.position).slice(0,16)}})()`,
		Playback: &PlaybackHints{
			Controls: []ControlHint{
				{ID: "autoplay_confirmation", Selectors: []string{".yqq-dialog-wrap .upload_btns__item"}, WaitMS: 500},
				{ID: "primary_play", Selectors: []string{".btn_big_play"}, PlayingClasses: []string{"btn_big_play--pause"}, WaitMS: 1200},
			},
			Player: &PlayerStateHint{
				PlayingSelectors: []string{".btn_big_play--pause", ".songlist__item--playing"},
				TimeSelectors:    []string{".player_music__time", "[class*='player_time']", "[class*='music__time']"},
				ItemSelector:     ".songlist__item", ActiveClasses: []string{"songlist__item--playing"},
				MediaLinkSelector: "a[href*='songDetail']",
			},
		},
	},
	{ID: "bilibili", DisplayName: "Bilibili", Aliases: []string{"bilibili", "b站", "哔哩哔哩"}, Hosts: []string{"bilibili.com", "www.bilibili.com"}, HomeURL: "https://www.bilibili.com", SearchURLTemplate: "https://search.bilibili.com/all?keyword={query}", MediaContinuation: &MediaContinuationHints{PreferredKinds: []string{"video"}, OpenFirst: true, Play: true}},
	{ID: "youtube-music", DisplayName: "YouTube Music", Aliases: []string{"youtube music"}, Hosts: []string{"music.youtube.com"}, HomeURL: "https://music.youtube.com", SearchURLTemplate: "https://music.youtube.com/search?q={query}", MediaContinuation: &MediaContinuationHints{PreferredKinds: []string{"audio"}, OpenFirst: true, Play: true}},
	{ID: "netflix", DisplayName: "Netflix", Aliases: []string{"netflix", "网飞", "奈飞", "網飛"}, Hosts: []string{"netflix.com", "www.netflix.com"}, HomeURL: "https://www.netflix.com/browse", SearchURLTemplate: "https://www.netflix.com/search?q={query}", MediaContinuation: &MediaContinuationHints{PreferredKinds: []string{"video"}, OpenFirst: true, Play: true}},
	{ID: "spotify", DisplayName: "Spotify", Aliases: []string{"spotify"}, Hosts: []string{"open.spotify.com"}, HomeURL: "https://open.spotify.com", SearchURLTemplate: "https://open.spotify.com/search/{query}", MediaContinuation: &MediaContinuationHints{PreferredKinds: []string{"audio"}, OpenFirst: true, Play: true}},
	{ID: "github", DisplayName: "GitHub", Aliases: []string{"github", "git hub"}, Hosts: []string{"github.com"}, HomeURL: "https://github.com", SearchURLTemplate: "https://github.com/search?q={query}", SearchURLTemplates: map[string]string{"repository": "https://github.com/search?q={query}&type=repositories"}},
	{ID: "reddit", DisplayName: "Reddit", Aliases: []string{"reddit"}, Hosts: []string{"reddit.com", "www.reddit.com"}, HomeURL: "https://www.reddit.com", SearchURLTemplate: "https://www.reddit.com/search/?q={query}"},
	{ID: "zhihu", DisplayName: "Zhihu", Aliases: []string{"zhihu", "知乎"}, Hosts: []string{"zhihu.com", "www.zhihu.com"}, HomeURL: "https://www.zhihu.com", SearchURLTemplate: "https://www.zhihu.com/search?type=content&q={query}"},
	{ID: "stackoverflow", DisplayName: "Stack Overflow", Aliases: []string{"stack overflow", "stackoverflow"}, Hosts: []string{"stackoverflow.com"}, HomeURL: "https://stackoverflow.com", SearchURLTemplate: "https://stackoverflow.com/search?q={query}"},
	{ID: "google", DisplayName: "Google", Aliases: []string{"google", "谷歌"}, Hosts: []string{"google.com", "www.google.com"}, HomeURL: "https://www.google.com", SearchURLTemplate: "https://www.google.com/search?q={query}&num=8"},
}

func MatchTarget(value string) (Definition, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return Definition{}, false
	}
	bestLength := 0
	var best Definition
	for _, definition := range catalog {
		for _, alias := range definition.Aliases {
			alias = strings.ToLower(alias)
			if strings.Contains(value, alias) && len([]rune(alias)) > bestLength {
				best, bestLength = definition, len([]rune(alias))
			}
		}
	}
	return best, bestLength > 0
}

func MatchURL(value string) (Definition, bool) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Hostname() == "" {
		return Definition{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	bestLength := 0
	var best Definition
	for _, definition := range catalog {
		for _, expected := range definition.Hosts {
			expected = strings.ToLower(strings.TrimPrefix(expected, "*."))
			if (host == expected || strings.HasSuffix(host, "."+expected)) && len(expected) > bestLength {
				best, bestLength = definition, len(expected)
			}
		}
	}
	return best, bestLength > 0
}

func SearchURL(definition Definition, query string, preferredKinds ...string) string {
	query = url.QueryEscape(strings.TrimSpace(query))
	if query == "" {
		return ""
	}
	template := definition.SearchURLTemplate
	for _, kind := range preferredKinds {
		if scoped := definition.SearchURLTemplates[strings.ToLower(strings.TrimSpace(kind))]; scoped != "" {
			template = scoped
			break
		}
	}
	if template == "" {
		return ""
	}
	return strings.ReplaceAll(template, "{query}", query)
}

func HasScopedSearch(definition Definition, preferredKinds ...string) bool {
	for _, kind := range preferredKinds {
		if definition.SearchURLTemplates[strings.ToLower(strings.TrimSpace(kind))] != "" {
			return true
		}
	}
	return false
}

func Catalog() []Definition {
	return append([]Definition(nil), catalog...)
}
