package browser_runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"
)

func TestInferBrowserTaskForYouTubeSearchFirstVideo(t *testing.T) {
	task := inferBrowserTask("Open YouTube search AI Agent tutorial and open the first video", "", "")
	if !isYouTubeName(task.Target) {
		t.Fatalf("target=%q, want YouTube", task.Target)
	}
	if task.Query != "AI Agent tutorial" {
		t.Fatalf("query=%q, want AI Agent tutorial", task.Query)
	}
	if !task.OpenFirstResult || task.Intent != "open_first_result" {
		t.Fatalf("unexpected task intent: %+v", task)
	}
}

func TestInferBrowserTaskRemovesSiteNameFromScopedSearchQuery(t *testing.T) {
	task := inferBrowserTask("Search Netflix for Stranger Things and open the first result", "", "")
	if task.Target != "Netflix" || task.Query != "Stranger Things" || task.ResultOrdinal != 1 {
		t.Fatalf("scoped search task = %+v", task)
	}
}

func TestInferBrowserTaskUsesExplicitQuery(t *testing.T) {
	task := inferBrowserTask("打开 YouTube 搜索第一个视频", "YouTube", "机器学习教程")
	if task.Query != "机器学习教程" || !task.OpenFirstResult {
		t.Fatalf("explicit query not preserved: %+v", task)
	}
	if got := taskSearchURL(task); got != "https://www.youtube.com/results?search_query=%E6%9C%BA%E5%99%A8%E5%AD%A6%E4%B9%A0%E6%95%99%E7%A8%8B" {
		t.Fatalf("unexpected YouTube search URL: %s", got)
	}
}

func TestInferBrowserTaskTreatsNamedVideoAsContentQuery(t *testing.T) {
	task := inferBrowserTask("帮我播放周杰伦《晴天》的视频", "", "")
	if task.Target != "" {
		t.Fatalf("content title became a website target: %+v", task)
	}
	if task.Query != "周杰伦《晴天》" {
		t.Fatalf("query=%q, want named video title", task.Query)
	}
	if task.ResultOrdinal != 1 || !task.PlayResult || !task.OpenFirstResult {
		t.Fatalf("named video should open and play its best matching result: %+v", task)
	}
}

func TestInferContextualMediaTaskKeepsTitleAsQuery(t *testing.T) {
	task := inferContextualMediaTask("Adele Hello")
	if task.Query != "Adele Hello" || task.Target != "" || !task.ContextualMediaTitle || task.ResultOrdinal != 0 {
		t.Fatalf("contextual media task = %+v", task)
	}
}

func TestContextualMediaTaskUsesCurrentKnownSite(t *testing.T) {
	task, err := contextualizeMediaTask(inferContextualMediaTask("Adele Hello"), map[string]any{
		"url": "https://www.youtube.com/watch?v=old",
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.Target != "YouTube" || task.Query != "Adele Hello" || task.ResultOrdinal != 1 || !task.PlayResult || !task.OpenFirstResult {
		t.Fatalf("known-site contextual task = %+v", task)
	}
	if got := knownTaskSearchURL(task); got != "https://www.youtube.com/results?search_query=Adele+Hello" {
		t.Fatalf("known-site contextual search URL = %q", got)
	}
}

func TestContextualMediaTaskUsesGenericPageModel(t *testing.T) {
	task, err := contextualizeMediaTask(inferContextualMediaTask("Independent Film"), map[string]any{
		"url": "https://media.example/home",
		"semantic_page": perception.SemanticPageModel{
			Type:     "media_catalog",
			Entities: []perception.SemanticEntity{{Kind: "video", Label: "Featured video"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.Target != "" || task.ResultOrdinal != 1 || !task.PlayResult || len(task.PreferredKinds) == 0 || task.PreferredKinds[0] != "video" {
		t.Fatalf("generic contextual task = %+v", task)
	}
}

func TestContextualMediaTaskRejectsOrdinaryCurrentPage(t *testing.T) {
	_, err := contextualizeMediaTask(inferContextualMediaTask("Adele Hello"), map[string]any{
		"url":           "https://example.com/docs",
		"semantic_page": perception.SemanticPageModel{Type: "article"},
	})
	if err == nil {
		t.Fatal("ordinary page accepted a contextual media title")
	}
}

func TestInferBrowserTaskTreatsNamedYouTubeVideoAsScopedQuery(t *testing.T) {
	task := inferBrowserTask("在 YouTube 打开周杰伦《晴天》的视频", "", "")
	if !isYouTubeName(task.Target) {
		t.Fatalf("target=%q, want YouTube", task.Target)
	}
	if task.Query != "周杰伦《晴天》" || task.ResultOrdinal != 1 || !task.PlayResult {
		t.Fatalf("named YouTube video was not converted to scoped playback: %+v", task)
	}
}

func TestKnownSiteMediaSearchUsesDeterministicURL(t *testing.T) {
	task := inferBrowserTask("帮我播放 Adele Hello 的视频", "YouTube", "")
	if got := knownTaskSearchURL(task); got != "https://www.youtube.com/results?search_query=Adele+Hello" {
		t.Fatalf("known search URL=%q", got)
	}
}

func TestInferBrowserTaskDoesNotTreatGenericMediaReferenceAsTitle(t *testing.T) {
	task := inferBrowserTask("播放当前视频", "", "")
	if task.Query != "" || task.Target != "" || task.ResultOrdinal != 0 || !task.PlayResult {
		t.Fatalf("current video must remain a current-page operation: %+v", task)
	}
}

func TestInferBrowserTaskControlsCurrentMedia(t *testing.T) {
	tests := map[string]string{
		"Quite current video": "leave",
		"Quit current video":  "leave",
		"Close this movie":    "leave",
		"Pause current video": "pause",
		"停止播放当前视频":            "pause",
		"退出当前视频":              "leave",
	}
	for command, want := range tests {
		task := inferBrowserTask(command, "", "")
		if task.MediaControl != want || task.Intent != want+"_current_media" || task.Target != "" || task.Query != "" {
			t.Fatalf("inferBrowserTask(%q) = %+v, want media control %q", command, task, want)
		}
	}
}

func TestInferBrowserTaskOpensSecondYouTubeHomeVideo(t *testing.T) {
	task := inferBrowserTask("Open youtub home page and play the second vido", "", "the 2nd video in the homepage feed and click it to start playback")
	if !isYouTubeName(task.Target) {
		t.Fatalf("target=%q, want YouTube", task.Target)
	}
	if task.Query != "" {
		t.Fatalf("query=%q, want empty homepage query", task.Query)
	}
	if task.ResultOrdinal != 2 || task.Intent != "open_result" {
		t.Fatalf("unexpected task: %+v", task)
	}
	if !task.PlayResult {
		t.Fatalf("playback intent was not preserved: %+v", task)
	}
	if got := taskTargetURL(task.Target); got != "https://www.youtube.com" {
		t.Fatalf("target URL=%q", got)
	}
}

func TestInferBrowserTaskPrefersExplicitQQMusicOverCurrentPage(t *testing.T) {
	task := inferBrowserTask("Open QQ Music home page and play the first recommended song", "", "")
	if !isQQMusicName(task.Target) {
		t.Fatalf("target=%q, want QQ Music", task.Target)
	}
	if task.ResultOrdinal != 1 || !task.PlayResult {
		t.Fatalf("unexpected QQ Music task: %+v", task)
	}
	if got := taskTargetURL(task.Target); got != "https://y.qq.com/" {
		t.Fatalf("target URL=%q", got)
	}
	if isYouTubeTarget(task.Target, map[string]any{"url": "https://www.youtube.com/watch?v=old"}) {
		t.Fatal("an explicit QQ Music target inherited the current YouTube page")
	}
}

func TestInferBrowserTaskPrefersExplicitURL(t *testing.T) {
	task := inferBrowserTask("Open https://news.ycombinator.com/ and open the second story", "", "")
	if task.Target != "https://news.ycombinator.com/" || task.ResultOrdinal != 2 {
		t.Fatalf("explicit URL task = %+v", task)
	}
	if got := taskTargetURL(task.Target); got != "https://news.ycombinator.com/" {
		t.Fatalf("explicit target URL = %q", got)
	}
}

func TestInferBrowserTaskExtractsUnregisteredNamedWebsite(t *testing.T) {
	task := inferBrowserTask("Open Vimeo home page and play the first video", "", "")
	if task.Target != "Vimeo" || task.ResultOrdinal != 1 || !task.PlayResult {
		t.Fatalf("unregistered website task = %+v", task)
	}
}

func TestUnknownWebsiteUsesSearchCapabilityHandoff(t *testing.T) {
	request := browserTaskRequest{SessionID: "athena-11111111111111111111111111111111", Goal: "Open Vimeo home page"}
	task := inferredBrowserTask{Intent: "open", Target: "Vimeo"}
	plan := browserTaskPlan{Goal: request.Goal, Target: task.Target}
	state, err := (&browserController{}).openTaskTarget(context.Background(), request, task, &plan)
	if err != nil {
		t.Fatal(err)
	}
	handoff, ok := state["capability_handoff"].(map[string]any)
	if !ok || handoff["to"] != "internet.search" || handoff["query"] != "Vimeo official website" {
		t.Fatalf("handoff = %#v", state)
	}
	if len(plan.Steps) != 1 || plan.Steps[0] != "request_search_capability" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestInferBrowserTaskKeepsCurrentPageContinuationTargetless(t *testing.T) {
	task := inferBrowserTask("Open the first related article on the current page", "", "")
	if task.Target != "" || task.ResultOrdinal != 1 {
		t.Fatalf("current-page continuation = %+v", task)
	}
}

func TestInferBrowserTaskTreatsSelectionAsCurrentPageAction(t *testing.T) {
	task := inferBrowserTask("Select the yufu Netflix profile", "Select the yufu Netflix profile", "")
	if task.Intent != "select_current" || task.Selection != "yufu Netflix profile" || task.Target != "" || task.Query != "" {
		t.Fatalf("current-page selection = %+v", task)
	}
}

func TestInferBrowserTaskUnderstandsEmbeddedCurrentPageClick(t *testing.T) {
	task := inferBrowserTask(
		"On the current YouTube page, click the Shorts filter in the top filter bar, immediately to the right of All.",
		"", "",
	)
	if task.Intent != "select_current" || task.Selection != "shorts filter" {
		t.Fatalf("embedded current-page click = %+v", task)
	}
	if task.GroundingQuery != "shorts filter immediately to the right of all" {
		t.Fatalf("grounding query = %q", task.GroundingQuery)
	}
	if task.Target != "" || task.Query != "" {
		t.Fatalf("current-page click must not navigate or search: %+v", task)
	}
}

func TestCurrentPageSelectionMatchesNamedProfileWithoutUsingSensitiveURL(t *testing.T) {
	state := map[string]any{"key_elements": []map[string]string{
		{"ref": "@e1", "label": `link "Netflix" [url=https://www.netflix.com/]`},
		{"ref": "@e4", "label": `link "yufu" [url=https://www.netflix.com/SwitchProfile?tkn=secret]`},
		{"ref": "@e5", "label": `link "Kids" [url=https://www.netflix.com/SwitchProfile?tkn=other]`},
		{"ref": "@e3", "label": `link "Manage Profiles" [url=https://www.netflix.com/ManageProfiles]`},
	}}
	element, ok := currentPageSelectionCandidate(state, "yufu Netflix profile")
	if !ok || element.Ref != "@e4" {
		t.Fatalf("profile selection candidate = %#v, ok=%v", element, ok)
	}
}

func TestBrowserPageSelectionRequiresContinuation(t *testing.T) {
	state := map[string]any{"semantic_page": perception.SemanticPageModel{Type: "profile_selection"}}
	if !browserPageSelectionRequired(state) {
		t.Fatal("profile selection page should pause the current browser task")
	}
	if browserPageSelectionRequired(map[string]any{"semantic_page": perception.SemanticPageModel{Type: "search_results"}}) {
		t.Fatal("ordinary search results must not pause for profile selection")
	}
}

func TestTaskResultCandidateSkipsStaleUnrelatedPageContent(t *testing.T) {
	state := map[string]any{
		"url": "https://www.netflix.com/search?q=Stranger+Things",
		"semantic_page": perception.SemanticPageModel{
			Type: "collection",
			Entities: []perception.SemanticEntity{
				{Kind: "content", Label: "Help Center", URL: "https://help.netflix.com/"},
				{Kind: "content", Label: "Stranger Things", URL: "https://www.netflix.com/search?q=Stranger+Things&jbv=80057281"},
			},
		},
	}
	candidate, ok := taskResultCandidate(state, 1, inferredBrowserTask{Query: "Stranger Things"})
	if !ok || candidate.Title != "Stranger Things" {
		t.Fatalf("query-aware candidate = %#v, found=%v", candidate, ok)
	}
}

func TestTaskResultCandidateRejectsEntirelyStalePage(t *testing.T) {
	state := map[string]any{
		"url": "https://www.netflix.com/search?q=Stranger+Things",
		"semantic_page": perception.SemanticPageModel{
			Type: "collection",
			Entities: []perception.SemanticEntity{
				{Kind: "content", Label: "Help Center", URL: "https://help.netflix.com/"},
				{Kind: "content", Label: "Media Center", URL: "https://media.netflix.com/"},
			},
		},
	}
	if candidate, ok := taskResultCandidate(state, 1, inferredBrowserTask{Query: "Stranger Things"}); ok {
		t.Fatalf("stale candidate must not be selected: %#v", candidate)
	}
}

func TestTaskResultCandidatePrefersRepeatedResultCardOverSearchSuggestion(t *testing.T) {
	state := map[string]any{
		"url": "https://www.netflix.com/search?q=Stranger+Things",
		"content_candidates": []map[string]any{
			{
				"context": "collection", "title": "Stranger Things",
				"url": "https://www.netflix.com/search?q=Stranger+Things&suggestionId=Video%3A70170287", "position": 100, "order": 5,
			},
			{
				"context": "collection", "title": "Stranger Things",
				"url": "https://www.netflix.com/search?q=Stranger+Things&jbv=80057281", "position": 200, "order": 33,
			},
			{
				"context": "collection", "title": "Stranger Things: Tales From '85",
				"url": "https://www.netflix.com/search?q=Stranger+Things&jbv=81398721", "position": 200, "order": 34,
			},
		},
		"semantic_page": perception.SemanticPageModel{
			Type: "search_results",
			Entities: []perception.SemanticEntity{
				{Kind: "content", Label: "Stranger Things", URL: "https://www.netflix.com/search?q=Stranger+Things&suggestionId=Video%3A70170287"},
				{Kind: "content", Label: "Stranger Things", URL: "https://www.netflix.com/search?q=Stranger+Things&jbv=80057281"},
			},
		},
	}
	candidate, ok := taskResultCandidate(state, 1, inferredBrowserTask{Query: "Stranger Things"})
	if !ok || !strings.Contains(candidate.URL, "jbv=80057281") {
		t.Fatalf("structured result card was not preferred: %#v, found=%v", candidate, ok)
	}
}

func TestSemanticTaskCandidateUsesRequestedEntityKind(t *testing.T) {
	task := inferBrowserTask("Open the second discussion", "", "")
	state := map[string]any{"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
		{Kind: "content", Label: "Unrelated navigation", URL: "https://forum.example/about", Position: 1},
		{Kind: "discussion", Label: "First discussion", URL: "https://forum.example/thread/one", Position: 2},
		{Kind: "discussion", Label: "Second discussion", URL: "https://forum.example/thread/two", Position: 3},
	}}}
	candidate, ok := semanticTaskCandidate(state, 2, task.PlayResult, task.PreferredKinds...)
	if !ok || candidate.Title != "Second discussion" {
		t.Fatalf("typed result selection = %#v, ok=%v, task=%+v", candidate, ok, task)
	}
}

func TestSemanticTaskCandidateSkipsCurrentSiteHomeNavigation(t *testing.T) {
	state := map[string]any{
		"url": "https://stream.example/search?q=topic",
		"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
			{Kind: "content", Label: "Stream", URL: "https://stream.example/", Position: 1},
			{Kind: "content", Label: "First matching title", URL: "https://stream.example/search?q=topic&item=1", Position: 2},
		}},
	}
	candidate, ok := semanticTaskCandidate(state, 1, false)
	if !ok || candidate.Title != "First matching title" {
		t.Fatalf("navigation-safe candidate = %#v, ok=%v", candidate, ok)
	}
}

func TestSemanticTaskCandidatePrefersRepeatedFeedOrderOverWeakURLKind(t *testing.T) {
	state := map[string]any{"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
		{ID: "first", Kind: "content", Label: "First forum story", URL: "https://source.example/first", Position: 1, Attributes: map[string]any{"context": "heading"}},
		{ID: "second", Kind: "content", Label: "Second forum story", URL: "https://source.example/second", Position: 2, Attributes: map[string]any{"context": "heading"}},
		{ID: "weak-post", Kind: "discussion", Label: "Unrelated post-shaped URL", URL: "https://third-party.example/post/unrelated", Position: 9, Attributes: map[string]any{"context": "heading"}},
	}}}
	candidate, ok := semanticTaskCandidate(state, 2, false, "discussion")
	if !ok || candidate.Title != "Second forum story" {
		t.Fatalf("structured feed order = %#v, ok=%v", candidate, ok)
	}
}

func TestSemanticTaskCandidateSkipsUtilityPreferenceLinks(t *testing.T) {
	state := map[string]any{"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
		{Kind: "content", Label: "Add as preferred on Google", URL: "https://www.google.com/preferences/source?q=example.com", Position: 1},
		{Kind: "article", Label: "A related investigation", URL: "https://news.example/articles/investigation", Position: 2},
	}}}
	candidate, ok := semanticTaskCandidate(state, 1, false, "article", "content")
	if !ok || candidate.Title != "A related investigation" {
		t.Fatalf("utility link was selected before article: candidate=%#v ok=%v", candidate, ok)
	}
}

func TestSemanticTaskCandidateSkipsSocialShareLinks(t *testing.T) {
	state := map[string]any{"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
		{Kind: "content", Label: "Share to Facebook", URL: "https://www.facebook.com/sharer/sharer.php?u=https://news.example/current", Position: 1},
		{Kind: "content", Label: "Share to X", URL: "https://x.com/intent/post?url=https://news.example/current", Position: 2},
		{Kind: "article", Label: "A related analysis", URL: "https://news.example/articles/analysis", Position: 3},
	}}}
	candidate, ok := semanticTaskCandidate(state, 1, false, "article", "content")
	if !ok || candidate.Title != "A related analysis" {
		t.Fatalf("social share link was selected before article: candidate=%#v ok=%v", candidate, ok)
	}
}

func TestSemanticTaskCandidateSkipsIdentityAndOnboardingFlows(t *testing.T) {
	state := map[string]any{"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
		{Kind: "content", Label: "Join the DEV Community", URL: "https://dev.to/enter?signup_subforem=1&state=new-user", Position: 1},
		{Kind: "article", Label: "First engineering article", URL: "https://dev.to/example/first-article", Position: 2},
	}}}
	candidate, ok := semanticTaskCandidate(state, 1, false, "article", "content")
	if !ok || candidate.Title != "First engineering article" {
		t.Fatalf("identity flow was selected before content: candidate=%#v ok=%v", candidate, ok)
	}
}

func TestSemanticTaskCandidatePrefersStructuredHeadingsOverAuthorEntities(t *testing.T) {
	state := map[string]any{
		"url": "https://community.example/",
		"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
			{Kind: "content", Label: "Editorial Team", URL: "https://community.example/editorial", Position: 1, SectionID: "feed"},
			{Kind: "content", Label: "Another Author", URL: "https://community.example/author", Position: 2, SectionID: "feed"},
		}},
		"content_candidates": []map[string]any{
			{"title": "First engineering article", "url": "https://community.example/team/first-article", "context": "heading", "position": 200},
			{"title": "Second engineering article", "url": "https://community.example/team/second-article", "context": "heading", "position": 500},
			{"title": "Editorial Team", "url": "https://community.example/editorial", "context": "collection", "position": 210},
		},
	}
	candidate, ok := semanticTaskCandidate(state, 2, false, "article", "content")
	if !ok || candidate.Title != "Second engineering article" {
		t.Fatalf("structured heading candidate = %#v, ok=%v", candidate, ok)
	}
}

func TestSemanticMediaCandidatePreservesObservedRef(t *testing.T) {
	state := map[string]any{"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
		{Kind: "audio", Label: "First song", Ref: "@e42", URL: "https://music.example/song/one", Position: 1, Playable: true},
	}}}
	candidate, ok := semanticTaskCandidate(state, 1, true, "audio")
	if !ok || candidate.Ref != "@e42" || candidate.URL != "https://music.example/song/one" {
		t.Fatalf("media candidate lost observed ref: %#v, ok=%v", candidate, ok)
	}
}

func TestSemanticMediaCandidateAcceptsRefBeforeDynamicURLIsAvailable(t *testing.T) {
	state := map[string]any{"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
		{ID: "video-one", Kind: "video", Label: "First recommended video", Ref: "@e41", Position: 1, Playable: true},
		{ID: "video-two", Kind: "video", Label: "Second recommended video", Ref: "@e42", Position: 2, Playable: true},
	}}}
	candidate, ok := semanticTaskCandidate(state, 2, true, "video")
	if !ok || candidate.ID != "video-two" || candidate.Ref != "@e42" || candidate.URL != "" {
		t.Fatalf("dynamic media ref candidate = %#v, ok=%v", candidate, ok)
	}
}

func TestSemanticMediaCandidateUsesRepeatedCollectionWithoutURLKeywords(t *testing.T) {
	state := map[string]any{"semantic_page": perception.SemanticPageModel{
		Type: "media_catalog", URL: "https://music.example/", Signals: []string{"media_controls"},
		Sections: []perception.SemanticSection{
			{ID: "artists", Kind: "item_list", EntityIDs: []string{"artist-1", "artist-2"}},
			{ID: "tracks", Kind: "item_list", EntityIDs: []string{"track-1", "track-2"}},
		},
		Entities: []perception.SemanticEntity{
			{ID: "artist-1", Kind: "content", Label: "First artist", URL: "https://music.example/first-artist", Position: 3},
			{ID: "artist-2", Kind: "content", Label: "Second artist", URL: "https://music.example/second-artist", Position: 4},
			{ID: "track-1", Kind: "content", Label: "First track", Ref: "@e21", URL: "https://music.example/first-artist/first-track", Position: 1},
			{ID: "track-2", Kind: "content", Label: "Second track", Ref: "@e22", URL: "https://music.example/second-artist/second-track", Position: 2},
		},
	}}
	candidate, ok := semanticTaskCandidate(state, 1, true, "audio")
	if !ok || candidate.Title != "First track" || candidate.Ref != "@e21" {
		t.Fatalf("generic media collection candidate = %#v, ok=%v", candidate, ok)
	}
}

func TestSemanticMediaCandidateUsesStructuredRawCandidatesWhenSectionsWereTrimmed(t *testing.T) {
	state := map[string]any{
		"url": "https://music.example/",
		"semantic_page": perception.SemanticPageModel{
			Type: "media_catalog", URL: "https://music.example/", Signals: []string{"media_controls"},
		},
		"content_candidates": []map[string]any{
			{"title": "First discovered track", "url": "https://music.example/artist-one/first-track", "context": "collection", "structured": true, "score": 5, "position": 200, "order": 10},
			{"title": "Second discovered track", "url": "https://music.example/artist-two/second-track", "context": "collection", "structured": true, "score": 5, "position": 400, "order": 20},
			{"title": "Artist profile", "url": "https://music.example/artist-one", "context": "standalone", "score": 2, "position": 210, "order": 11},
		},
	}
	candidate, ok := semanticTaskCandidate(state, 2, true, "audio")
	if !ok || candidate.Title != "Second discovered track" {
		t.Fatalf("trimmed media page fallback = %#v, ok=%v", candidate, ok)
	}
}

func TestSemanticTaskCandidateSelectsMediaCollectionWithoutTreatingItAsPlayable(t *testing.T) {
	state := map[string]any{"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
		{Kind: "media_collection", Label: "Focus playlist", Ref: "@e8", URL: "https://music.example/playlist/focus", Position: 1},
		{Kind: "media_collection", Label: "Workout playlist", Ref: "@e9", URL: "https://music.example/playlist/workout", Position: 2},
	}}}
	candidate, ok := semanticTaskCandidate(state, 1, false, "media_collection")
	if !ok || candidate.Title != "Focus playlist" || candidate.Ref != "@e8" {
		t.Fatalf("media collection candidate = %#v, ok=%v", candidate, ok)
	}
	if _, playable := semanticTaskCandidate(state, 1, true, "audio"); playable {
		t.Fatal("a media collection was treated as a playable audio item")
	}
}

func TestTaskResultCandidateFallsBackToObservedMedia(t *testing.T) {
	state := map[string]any{"media_candidates": []map[string]any{
		{"id": "track-1", "title": "First track", "url": "https://music.example/watch?v=track-1", "kind": "video", "position": 1},
	}}
	task := inferredBrowserTask{PlayResult: true, PreferredKinds: []string{"audio"}}
	candidate, ok := taskResultCandidate(state, 1, task)
	if !ok || candidate.Title != "First track" {
		t.Fatalf("observed media fallback = %#v, ok=%v", candidate, ok)
	}
}

func TestDiscoveredTaskTargetCandidatePrefersMatchingOfficialHost(t *testing.T) {
	state := map[string]any{"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
		{Kind: "content", Label: "Vimeo profile on X", URL: "https://x.com/vimeo", Position: 1},
		{Kind: "content", Label: "Vimeo: high quality video hosting", URL: "https://vimeo.com/", Position: 2},
		{Kind: "content", Label: "Vimeo - Wikipedia", URL: "https://en.wikipedia.org/wiki/Vimeo", Position: 3},
	}}}
	candidate, ok := discoveredTaskTargetCandidate(state, "Vimeo")
	if !ok || candidate.URL != "https://vimeo.com/" {
		t.Fatalf("resolved website = %#v, ok=%v", candidate, ok)
	}
}

func TestDiscoveredTaskTargetCandidateRejectsLabelOnlyThirdParty(t *testing.T) {
	state := map[string]any{"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
		{Kind: "content", Label: "Lobsters discussion roundup", URL: "https://third-party.example/post/lobsters", Position: 1},
		{Kind: "content", Label: "Lobsters Online Seafood Delivery", URL: "https://www.lobsters-online.com/", Position: 2},
		{Kind: "content", Label: "Lobsters", URL: "https://lobste.rs/", Position: 3},
	}}}
	candidate, ok := discoveredTaskTargetCandidate(state, "Lobsters")
	if !ok || candidate.URL != "https://lobste.rs/" {
		t.Fatalf("resolved website = %#v, ok=%v", candidate, ok)
	}
}

func TestDiscoveredTaskTargetCandidateAcceptsShortBrandRootDomain(t *testing.T) {
	state := map[string]any{"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
		{Kind: "content", Label: "DEV Community", URL: "https://dev.to/", Position: 1},
		{Kind: "content", Label: "DEV Community guide", URL: "https://guides.example/dev-community", Position: 2},
	}}}
	candidate, ok := discoveredTaskTargetCandidate(state, "DEV Community")
	if !ok || candidate.URL != "https://dev.to/" {
		t.Fatalf("short brand website = %#v, ok=%v", candidate, ok)
	}
}

func TestDiscoveredTaskTargetCandidateUsesRawRootWhenSemanticModelOnlyHasSitelink(t *testing.T) {
	state := map[string]any{
		"semantic_page": perception.SemanticPageModel{Entities: []perception.SemanticEntity{
			{Kind: "content", Label: "Lobsters by mail", URL: "https://lobste.rs/s/jg3eet", Position: 1},
		}},
		"content_candidates": []map[string]any{
			{"title": "Lobsters", "url": "https://lobste.rs/"},
		},
	}
	candidate, ok := discoveredTaskTargetCandidate(state, "Lobsters")
	if !ok || candidate.URL != "https://lobste.rs/" {
		t.Fatalf("resolved root website = %#v, ok=%v", candidate, ok)
	}
}

func TestBrowserDiscoveryNormalizesSitelinkToOrigin(t *testing.T) {
	got := browserDiscoveryRootURL("https://lobste.rs/about?source=search#team")
	if got != "https://lobste.rs/" {
		t.Fatalf("discovery origin = %q", got)
	}
}

func TestDetectBrowserChallengeRecognizesGenericIntegrityPage(t *testing.T) {
	challenge := detectBrowserChallenge(map[string]any{
		"url": "https://example.com/post/one", "title": "Checking your browser...",
	})
	if challenge == nil || challenge["kind"] != "cloudflare_challenge" {
		t.Fatalf("generic integrity challenge = %#v", challenge)
	}
}

func TestDetectBrowserChallengeIgnoresPassiveRecaptchaDisclosure(t *testing.T) {
	challenge := detectBrowserChallenge(map[string]any{
		"url":     "https://stream.example/watch/42",
		"title":   "Playback error",
		"content": "This page is protected by Google reCAPTCHA. Pardon the interruption.",
	})
	if challenge != nil {
		t.Fatalf("passive reCAPTCHA disclosure was treated as an active challenge: %#v", challenge)
	}
}

func TestDetectBrowserChallengeRecognizesCaptchaURL(t *testing.T) {
	challenge := detectBrowserChallenge(map[string]any{
		"url": "https://example.com/captcha/check", "title": "Security check",
	})
	if challenge == nil || challenge["kind"] != "captcha" {
		t.Fatalf("captcha URL was not detected: %#v", challenge)
	}
}

func TestBrowserTargetRejectsObservedErrorPage(t *testing.T) {
	state := map[string]any{
		"url":   "https://forum.example/u/missing",
		"title": "Page Not Found - Example Forum",
		"page":  map[string]any{"type": "error_page"},
	}
	if !browserErrorPageDetected(state) {
		t.Fatal("error page was not detected")
	}
	if browserTargetObserved(state, "https://forum.example/u/missing") {
		t.Fatal("an error page satisfied the navigation postcondition")
	}
}

func TestBrowserTargetAcceptsStableContentRedirect(t *testing.T) {
	state := map[string]any{
		"url":   "https://www.netflix.com/title/80057281",
		"title": "Stranger Things - Netflix",
	}
	if !browserTargetObserved(state, "https://www.netflix.com/search?q=Stranger+Things&jbv=80057281") {
		t.Fatal("stable content redirect did not satisfy the navigation postcondition")
	}
	if browserTargetObserved(state, "https://www.netflix.com/search?q=Stranger+Things&jbv=70170287") {
		t.Fatal("a different content identity satisfied the navigation postcondition")
	}
}

func TestGitHubRepositoryTaskUsesTypedSearch(t *testing.T) {
	task := inferBrowserTask("Open GitHub, search for golang agent, and open the second repository", "", "")
	if len(task.PreferredKinds) != 1 || task.PreferredKinds[0] != "repository" || task.ResultOrdinal != 2 {
		t.Fatalf("repository intent = %+v", task)
	}
	if got := taskSearchURL(task); got != "https://github.com/search?q=golang+agent&type=repositories" {
		t.Fatalf("typed GitHub search URL = %q", got)
	}
}

func TestFinishTaskMarksChallengeAsBlocked(t *testing.T) {
	state, err := (&browserController{}).finishTask(
		map[string]any{"challenge_detected": true},
		browserTaskPlan{Completed: true, Message: "Browser task completed."},
		errors.New("captcha requires user takeover"),
	)
	if err == nil {
		t.Fatal("challenge error was swallowed")
	}
	plan, ok := state["browser_task"].(browserTaskPlan)
	if !ok || plan.Completed || plan.Message != "Human verification is required before this browser task can continue." {
		t.Fatalf("challenge plan = %#v", state["browser_task"])
	}
}

func TestCleanupBrowserQueryRemovesActionPunctuation(t *testing.T) {
	task := inferBrowserTask("Open Stack Overflow, search for Go context cancellation, and open the second result", "", "")
	if task.Query != "Go context cancellation" {
		t.Fatalf("query = %q", task.Query)
	}
}

func TestFindSecondBrowserResultRef(t *testing.T) {
	state := map[string]any{"key_elements": []map[string]string{
		{"ref": "@e1", "label": `link "Liked videos" [url=https://www.youtube.com/playlist?list=LL]`},
		{"ref": "@e2", "label": `link "First tutorial" [url=https://www.youtube.com/watch?v=first]`},
		{"ref": "@e3", "label": `link "First tutorial title" [url=https://www.youtube.com/watch?v=first]`},
		{"ref": "@e4", "label": `link "Second tutorial" [url=https://www.youtube.com/watch?v=second]`},
	}}
	ref := findNthBrowserResultRef(state, 2, "YouTube")
	if ref != "@e4" {
		t.Fatalf("second result ref=%q, want @e4", ref)
	}
}

func TestFindBrowserSearchBoxRef(t *testing.T) {
	state := map[string]any{"key_elements": []map[string]string{
		{"ref": "@e1", "label": "Home"},
		{"ref": "@e2", "label": `input "Search"`},
	}}
	if ref := findBrowserElementRef(state, isBrowserSearchBox); ref != "@e2" {
		t.Fatalf("search ref=%q, want @e2", ref)
	}
}

func TestLikelyYouTubeResultSkipsNavigation(t *testing.T) {
	nav := browserSemanticElement{Ref: "@e1", Label: "Home"}
	liked := browserSemanticElement{Ref: "@e2", Label: `link "Liked videos" [url=https://www.youtube.com/playlist?list=LL]`, URL: "https://www.youtube.com/playlist?list=LL"}
	video := browserSemanticElement{Ref: "@e3", Label: `link "AI Agent tutorial" [url=https://www.youtube.com/watch?v=abc]`, URL: "https://www.youtube.com/watch?v=abc"}
	if isLikelyResultElement(nav, "YouTube") {
		t.Fatal("navigation element was treated as a video result")
	}
	if isLikelyResultElement(liked, "YouTube") {
		t.Fatal("liked-videos playlist was treated as a video result")
	}
	if !isLikelyResultElement(video, "YouTube") {
		t.Fatal("video result was not detected")
	}
}

func TestYouTubeWatchPostcondition(t *testing.T) {
	if isYouTubeWatchPage(map[string]any{"url": "https://www.youtube.com/playlist?list=LL"}) {
		t.Fatal("playlist page satisfied video postcondition")
	}
	if !isYouTubeWatchPage(map[string]any{"url": "https://www.youtube.com/watch?v=abc"}) {
		t.Fatal("watch page did not satisfy video postcondition")
	}
}

func TestQQMusicPlaybackLinkIsRecognized(t *testing.T) {
	if !isBrowserPlayButton(browserSemanticElement{Ref: "@e42", Label: `link "播放"`}) {
		t.Fatal("QQ Music playback link was not recognized")
	}
	if isBrowserPlayButton(browserSemanticElement{Ref: "@e10", Label: `link "播放全部"`}) {
		t.Fatal("play-all control must not be mistaken for a single-track play action")
	}
}

func TestBrowserPlayControlsPreferAutoplayConfirmation(t *testing.T) {
	controls := browserPlayControls(map[string]any{"key_elements": []map[string]string{
		{"ref": "@e1", "label": `link "播放"`},
		{"ref": "@e2", "label": `button "开始播放"`},
	}})
	if len(controls) != 2 || controls[0].Ref != "@e2" {
		t.Fatalf("unexpected playback control order: %#v", controls)
	}
}

func TestWaitForBrowserPlayControlsHandlesDelayedPage(t *testing.T) {
	snapshots := 0
	run := func(_ time.Duration, args ...string) (string, error) {
		command := strings.Join(args, " ")
		switch {
		case strings.Contains(command, "get url"):
			return "https://y.qq.com/n/ryqq_v2/songDetail/001", nil
		case strings.Contains(command, "snapshot"):
			snapshots++
			if snapshots < 3 {
				return `- heading "- -" [ref=e1]`, nil
			}
			return `- link "播放" [ref=e42]`, nil
		case strings.Contains(command, "wait 1000"):
			return "", nil
		default:
			return "", nil
		}
	}
	_, controls, err := waitForBrowserPlayControls(run, nil, time.Second)
	if err != nil || len(controls) != 1 || controls[0].Ref != "@e42" || snapshots != 3 {
		t.Fatalf("delayed playback control was not discovered: controls=%#v snapshots=%d err=%v", controls, snapshots, err)
	}
}

func TestInspectActiveModalPlaybackControl(t *testing.T) {
	run := func(_ time.Duration, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "eval" {
			return `{"data":{"result":{"found":true,"clicked":false,"reason":"active_modal_play_control","label":"Play","selector":"[data-athena-modal-play-control=\"true\"]"}}}`, nil
		}
		return "", nil
	}
	selector, found, err := inspectActiveModalPlaybackControl(run, nil)
	if err != nil || !found || selector != `[data-athena-modal-play-control="true"]` {
		t.Fatalf("modal playback control = selector=%q found=%v err=%v", selector, found, err)
	}
}

func TestInspectProtectedPlaybackFailureReportsMissingDRM(t *testing.T) {
	run := func(_ time.Duration, args ...string) (string, error) {
		if len(args) > 0 && args[0] == "eval" {
			return `{"data":{"result":{"found":true,"clicked":false,"page_error":true,"page_message":"Pardon the interruption","drm_checked":true,"drm_supported":false,"drm_reason":"Unsupported keySystem"}}}`, nil
		}
		return "", nil
	}
	diagnosis, err := inspectProtectedPlaybackFailure(run, nil)
	if err == nil || !strings.Contains(err.Error(), "Widevine") || diagnosis["drm_supported"] != false {
		t.Fatalf("protected playback diagnosis = %#v, err=%v", diagnosis, err)
	}
}

func TestBrowserTaskActionTimeoutUsesBoundedBudgets(t *testing.T) {
	tests := []struct {
		action    string
		arguments map[string]any
		want      time.Duration
	}{
		{action: "play", want: 45 * time.Second},
		{action: "navigate", want: 40 * time.Second},
		{action: "click", want: 30 * time.Second},
		{action: "wait", arguments: map[string]any{"value": "1800"}, want: 11800 * time.Millisecond},
		{action: "wait", arguments: map[string]any{"value": ""}, want: 20 * time.Second},
	}
	for _, test := range tests {
		if got := browserTaskActionTimeout(test.action, test.arguments); got != test.want {
			t.Errorf("browserTaskActionTimeout(%q)=%s, want %s", test.action, got, test.want)
		}
	}
}
