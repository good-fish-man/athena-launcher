package browser_runtime

import (
	"errors"
	"strings"
	"testing"
	"time"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"
)

func TestSemanticBrowserSuggestionNavigatesURLWithoutSnapshotRef(t *testing.T) {
	currentURL := "https://news.ycombinator.com/"
	actions := browserSuggestedActions("athena-88888888888888888888888888888888", map[string]any{
		"url": currentURL,
		"semantic_page": perception.SemanticPageModel{Interactions: []perception.Interaction{{
			Kind: "open", Label: "A useful article", TargetURL: "https://example.com/article",
			Description: "Open this article.", Risk: "LOW", Confidence: 0.9,
			Postcondition: map[string]any{"page_changed": true},
		}}},
	})
	if len(actions) != 1 {
		t.Fatalf("actions=%#v, want one URL navigation", actions)
	}
	if actions[0].Capability != "browser.navigate" || actions[0].Arguments["url"] != "https://example.com/article" {
		t.Fatalf("URL-only interaction must navigate: %#v", actions[0])
	}
	if actions[0].Arguments["expected_page_url"] != currentURL || actions[0].Arguments["open_mode"] != "current" {
		t.Fatalf("navigation precondition or mode missing: %#v", actions[0].Arguments)
	}
}

func TestSemanticBrowserSuggestionClicksStableSnapshotRef(t *testing.T) {
	actions := browserSuggestedActions("athena-99999999999999999999999999999999", map[string]any{
		"url": "https://example.com/feed",
		"semantic_page": perception.SemanticPageModel{Interactions: []perception.Interaction{{
			Kind: "open", Label: "Second item", Ref: "@e12", TargetURL: "https://example.com/item/2",
			Risk: "LOW", Confidence: 0.9, Postcondition: map[string]any{"page_changed": true},
		}}},
	})
	if len(actions) != 1 || actions[0].Capability != "browser.click" || actions[0].Arguments["ref"] != "@e12" {
		t.Fatalf("snapshot-backed interaction must click: %#v", actions)
	}
}

func TestProfileSelectionSuggestionWaitsForDocumentTransition(t *testing.T) {
	actions := browserSuggestedActions("athena-77777777777777777777777777777777", map[string]any{
		"url": "https://stream.example/browse",
		"semantic_page": perception.SemanticPageModel{
			Type: "profile_selection",
			Interactions: []perception.Interaction{{
				Kind: "open", Label: "Personal", Ref: "@e4", Risk: "LOW", Confidence: 0.99,
			}},
		},
	})
	if len(actions) != 1 || actions[0].Capability != "browser.click" {
		t.Fatalf("profile actions = %#v", actions)
	}
	if actions[0].Arguments["wait_for_document_transition"] != true {
		t.Fatalf("profile action must wait for the destination document: %#v", actions[0].Arguments)
	}
}

func TestBrowserDocumentTransitionDetectsTitleOrURLChange(t *testing.T) {
	if !browserDocumentTransitioned("https://stream.example/browse", "Stream", "https://stream.example/browse", "Home - Stream") {
		t.Fatal("title change was not detected")
	}
	if !browserDocumentTransitioned("https://stream.example/browse", "Stream", "https://stream.example/watch/1", "Stream") {
		t.Fatal("URL change was not detected")
	}
	if browserDocumentTransitioned("https://stream.example/browse", "Stream", "https://stream.example/browse", "Stream") {
		t.Fatal("unchanged document reported as transitioned")
	}
}

func TestBrowserSuggestedActionsOnlyOfferChallengeResume(t *testing.T) {
	actions := browserSuggestedActions("athena-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", map[string]any{
		"url": "https://example.com/verify", "challenge_detected": true,
		"semantic_page": perception.SemanticPageModel{Interactions: []perception.Interaction{{
			Kind: "open", Label: "Cookie Policy", Ref: "@e1", TargetURL: "https://example.com/cookies",
		}}},
	})
	if len(actions) != 1 || actions[0].Capability != "browser.observe" || actions[0].Kind != "resume" {
		t.Fatalf("challenge suggestions = %#v", actions)
	}
	if actions[0].Postcondition["challenge_cleared"] != true {
		t.Fatalf("challenge postcondition missing: %#v", actions[0])
	}
}

func TestBrowserSuggestedActionsOnlyOfferAuthenticationResume(t *testing.T) {
	actions := browserSuggestedActions("athena-abababababababababababababababab", map[string]any{
		"url":                        "https://music.example/player",
		"user_intervention_required": true,
		"semantic_page": perception.SemanticPageModel{Interactions: []perception.Interaction{{
			Kind: "play", Label: "First song", Ref: "@e1", TargetURL: "https://music.example/song/1",
		}}},
	})
	if len(actions) != 1 || actions[0].Capability != "browser.observe" || actions[0].Label != "I've completed sign-in" {
		t.Fatalf("authentication suggestions = %#v", actions)
	}
	if actions[0].Postcondition["intervention_cleared"] != true {
		t.Fatalf("authentication postcondition missing: %#v", actions[0])
	}
}

func TestBrowserSuggestedActionsOfferResolvedTargetConfirmation(t *testing.T) {
	sessionID := "athena-abababababababababababababababab"
	resolution := browserTargetResolution{
		Schema: browserTargetResolutionSchema, Decision: browserResolutionAskUser, Confidence: 0.61,
		Candidates: []browserTargetCandidate{
			{ID: "one", Label: "First tutorial", URL: "https://video.example/watch/one", Kind: "video", Playable: true, Confidence: 0.61},
			{ID: "two", Label: "Second tutorial", URL: "https://video.example/watch/two", Kind: "video", Playable: true, Confidence: 0.58},
		},
	}
	actions := browserSuggestedActions(sessionID, map[string]any{
		"url": "https://video.example/home", "user_intervention_required": true,
		"intervention": map[string]any{"kind": "target_confirmation"}, "target_resolution": resolution,
	})
	if len(actions) != 2 {
		t.Fatalf("actions = %#v", actions)
	}
	if actions[0].Capability != "browser.play" || actions[0].Arguments["target_url"] != "https://video.example/watch/one" {
		t.Fatalf("first action = %#v", actions[0])
	}
	if actions[0].Label == "I've completed sign-in" {
		t.Fatalf("target confirmation was rendered as sign-in takeover: %#v", actions)
	}
}

func TestSemanticBrowserSuggestionsExcludeLegalFooterLinks(t *testing.T) {
	actions := browserSuggestedActions("athena-cdcdcdcdcdcdcdcdcdcdcdcdcdcdcdcd", map[string]any{
		"url": "https://forum.example/post/1",
		"semantic_page": perception.SemanticPageModel{Interactions: []perception.Interaction{
			{Kind: "open", Label: "Example, Inc. © 2026. All rights reserved.", Ref: "@e1", TargetURL: "https://forum.example/legal", Confidence: 0.9},
			{Kind: "open", Label: "Related discussion", Ref: "@e2", TargetURL: "https://forum.example/post/2", Confidence: 0.8},
		}},
	})
	if len(actions) != 1 || actions[0].Label != "Related discussion" {
		t.Fatalf("footer link leaked into suggestions: %#v", actions)
	}
}

func TestBrowserSuggestedActionsUseUniqueYouTubeVideos(t *testing.T) {
	state := map[string]any{
		"url": "https://www.youtube.com/",
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": `link "Home" [url=https://www.youtube.com/]`},
			{"ref": "@e2", "label": `link "Liked videos" [url=https://www.youtube.com/playlist?list=LL]`},
			{"ref": "@e3", "label": `link "First video" [url=https://www.youtube.com/watch?v=first]`},
			{"ref": "@e4", "label": `link "First video duplicate" [url=https://www.youtube.com/watch?v=first]`},
			{"ref": "@e5", "label": `link "Second video" [url=https://www.youtube.com/watch?v=second]`},
		},
	}
	actions := browserSuggestedActions("athena-11111111111111111111111111111111", state)
	if len(actions) != 2 {
		t.Fatalf("actions=%#v, want two unique videos", actions)
	}
	if actions[0].Capability != "browser.play" || actions[0].Label != "First video" {
		t.Fatalf("unexpected first action: %#v", actions[0])
	}
	if actions[1].Arguments["target_url"] != "https://www.youtube.com/watch?v=second" {
		t.Fatalf("unexpected second target: %#v", actions[1].Arguments)
	}
	for _, action := range actions {
		if strings.Contains(strings.ToLower(action.Label), "liked") {
			t.Fatalf("navigation item leaked into media options: %#v", action)
		}
		if action.Arguments["expected_page_url"] != state["url"] {
			t.Fatalf("missing page precondition: %#v", action.Arguments)
		}
	}
}

func TestParseQQMusicMediaCandidates(t *testing.T) {
	output := `{"success":true,"data":{"result":{"candidates":[{"id":"001","title":"First song","url":"https://y.qq.com/n/ryqq_v2/songDetail/001","position":100},{"id":"001","title":"A much longer lyric label","url":"https://y.qq.com/n/ryqq_v2/songDetail/001","position":120},{"id":"singer","title":"Artist","url":"https://y.qq.com/n/ryqq_v2/singer/singer","position":130}]}}}`
	candidates := parseBrowserMediaCandidates(output, "https://y.qq.com/")
	if len(candidates) != 1 || candidates[0].Kind != "audio" || candidates[0].Title != "First song" {
		t.Fatalf("unexpected QQ Music candidates: %#v", candidates)
	}
	state := map[string]any{"url": "https://y.qq.com/", "media_candidates": []map[string]any{{
		"id": "001", "kind": "audio", "title": "First song", "url": "https://y.qq.com/n/ryqq_v2/songDetail/001",
	}}}
	actions := browserSuggestedActions("athena-11111111111111111111111111111111", state)
	if len(actions) != 1 || actions[0].Capability != "browser.play" || actions[0].Postcondition["url_kind"] != "music_track" {
		t.Fatalf("unexpected QQ Music suggestion: %#v", actions)
	}
}

func TestBrowserSuggestedActionsPreferDiscoveredMediaCandidates(t *testing.T) {
	state := map[string]any{
		"url":  "https://www.youtube.com/results?search_query=agents",
		"page": map[string]any{"type": "media_catalog"},
		"media_candidates": []map[string]any{
			{"id": "one", "title": "First tutorial", "url": "https://www.youtube.com/watch?v=one", "position": 100},
			{"id": "two", "title": "Second tutorial", "url": "https://www.youtube.com/watch?v=two", "position": 200},
		},
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": `button "Clear search query"`},
			{"ref": "@e2", "label": `button "Guide"`},
		},
	}
	actions := browserSuggestedActions("athena-44444444444444444444444444444444", state)
	if len(actions) != 2 {
		t.Fatalf("actions=%#v, want two media options", actions)
	}
	for index, action := range actions {
		if action.Capability != "browser.play" || action.Kind != "media" {
			t.Fatalf("action %d is not verified playback: %#v", index, action)
		}
	}
	if actions[1].Label != "Second tutorial" || actions[1].Arguments["target_url"] != "https://www.youtube.com/watch?v=two" {
		t.Fatalf("unexpected second option: %#v", actions[1])
	}
}

func TestParseBrowserMediaCandidatesFromAgentBrowserEnvelope(t *testing.T) {
	output := `{"success":true,"data":{"result":{"candidates":[{"id":"ad","title":"Ad: Sponsored tutorial","url":"https://www.youtube.com/watch?v=ad","position":10},{"id":"one","title":" First tutorial ","url":"https://www.youtube.com/watch?v=one","position":120},{"id":"one","title":"duplicate","url":"https://www.youtube.com/watch?v=one","position":130},{"id":"bad","title":"external","url":"https://example.com/watch?v=bad","position":140}]}}}`
	candidates := parseBrowserMediaCandidates(output, "https://www.youtube.com/results?search_query=agents")
	if len(candidates) != 1 || candidates[0].ID != "one" || candidates[0].Title != "First tutorial" {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
}

func TestBrowserMediaCandidatesPreserveDOMOrderWithinSameRow(t *testing.T) {
	output := `{"success":true,"data":{"result":{"candidates":[{"id":"two","title":"Second video","url":"https://www.youtube.com/watch?v=two","position":100,"order":2},{"id":"one","title":"First video","url":"https://www.youtube.com/watch?v=one","position":100,"order":1}]}}}`
	candidates := parseBrowserMediaCandidates(output, "https://www.youtube.com/")
	state := applyBrowserMediaCandidates(map[string]any{"url": "https://www.youtube.com/"}, candidates)
	first, firstOK := findNthBrowserMediaCandidate(state, 1)
	second, secondOK := findNthBrowserMediaCandidate(state, 2)
	if !firstOK || !secondOK || first.ID != "one" || second.ID != "two" {
		t.Fatalf("same-row DOM order was not preserved: first=%#v second=%#v", first, second)
	}
}

func TestBrowserMediaCandidateRefMatchesObservedLink(t *testing.T) {
	state := map[string]any{
		"url": "https://music.example/",
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": `link "Home" [url=/]`},
			{"ref": "@e42", "label": `link "First song" [url=/song/one]`},
		},
	}
	if ref := browserMediaCandidateRef(state, "https://music.example/song/one"); ref != "@e42" {
		t.Fatalf("media ref = %q, want @e42", ref)
	}
}

func TestActivateBrowserMediaCandidateUsesCardPlayControl(t *testing.T) {
	var command string
	run := func(_ time.Duration, args ...string) (string, error) {
		command = strings.Join(args, " ")
		return `{"success":true,"data":{"result":{"found":true,"clicked":true,"reason":"card_play_control","score":105}}}`, nil
	}
	result, err := activateBrowserMediaCandidate(run, []string{"--session", "athena-test"}, "https://music.example/song/one", "First song")
	if err != nil || result["clicked"] != true {
		t.Fatalf("unexpected media activation: result=%#v err=%v", result, err)
	}
	if !strings.Contains(command, "data-athena-media-activation") || !strings.Contains(command, "card_play_control") {
		t.Fatalf("generic media-card activation script missing: %s", command)
	}
}

func TestDetectBrowserInterventionForAuthenticationDialog(t *testing.T) {
	state := map[string]any{
		"url":      "https://music.example/player",
		"title":    "Music Player",
		"snapshot": `- dialog "Log in to continue" [ref=e10]`,
	}
	annotateBrowserIntervention(state)
	if state["user_intervention_required"] != true {
		t.Fatalf("authentication dialog was not marked for takeover: %#v", state)
	}
	intervention, _ := state["intervention"].(map[string]any)
	if intervention["kind"] != "authentication_required" {
		t.Fatalf("unexpected intervention: %#v", intervention)
	}
}

func TestBrowserSuggestedActionsOfferCurrentWatchPlayback(t *testing.T) {
	actions := browserSuggestedActions("athena-22222222222222222222222222222222", map[string]any{
		"url":          "https://www.youtube.com/watch?v=current",
		"key_elements": []map[string]string{{"ref": "@e1", "label": `button "Play"`}},
	})
	if len(actions) != 1 || actions[0].Capability != "browser.play" {
		t.Fatalf("current playback action missing: %#v", actions)
	}
	if actions[0].Postcondition["media_playing"] != true {
		t.Fatalf("playback postcondition missing: %#v", actions[0])
	}
}

func TestBrowserSuggestedActionsOfferMusicPlayButton(t *testing.T) {
	actions := browserSuggestedActions("athena-33333333333333333333333333333333", map[string]any{
		"url":          "https://y.qq.com/n/ryqq_v2/songDetail/blue-hour",
		"key_elements": []map[string]string{{"ref": "@e8", "label": `button "Play Blue Hour"`}},
	})
	if len(actions) != 1 || actions[0].Capability != "browser.play" || actions[0].Arguments["ref"] != "@e8" {
		t.Fatalf("music playback option missing: %#v", actions)
	}
}

func TestBrowserSuggestedActionsReplaceActivePlayWithTransportControls(t *testing.T) {
	actions := browserSuggestedActions("athena-55555555555555555555555555555555", map[string]any{
		"url":      "https://y.qq.com/n/ryqq_v2/player",
		"playback": map[string]any{"playing": true, "verified": true},
		"key_elements": []map[string]string{
			{"ref": "@e5", "label": `link "上一首"`},
			{"ref": "@e6", "label": `link "播放"`},
			{"ref": "@e7", "label": `link "下一首"`},
			{"ref": "@e8", "label": `link "播放"`},
		},
	})
	if len(actions) != 2 || actions[0].Label != "Previous track" || actions[1].Label != "Next track" {
		t.Fatalf("unexpected active playback actions: %#v", actions)
	}
	for _, action := range actions {
		if action.Capability != "browser.play" {
			t.Fatalf("transport control does not verify playback: %#v", action)
		}
	}
}

func TestBrowserSuggestedActionsHideUnavailableTransportControls(t *testing.T) {
	actions := browserSuggestedActions("athena-66666666666666666666666666666666", map[string]any{
		"url": "https://y.qq.com/n/ryqq_v2/player",
		"playback": map[string]any{
			"playing": true, "verified": true, "can_previous": false, "can_next": false, "media_id": "current",
		},
		"media_candidates": []map[string]any{{
			"id": "current", "kind": "audio", "title": "Current song", "url": "https://y.qq.com/n/ryqq_v2/songDetail/current",
		}},
		"key_elements": []map[string]string{
			{"ref": "@e5", "label": `link "上一首"`},
			{"ref": "@e7", "label": `link "下一首"`},
		},
	})
	if len(actions) != 0 {
		t.Fatalf("unavailable transport controls were suggested: %#v", actions)
	}
}

func TestBrowserSuggestedActionsUseDeterministicQQResume(t *testing.T) {
	actions := browserSuggestedActions("athena-77777777777777777777777777777777", map[string]any{
		"url":      "https://y.qq.com/n/ryqq_v2/player",
		"playback": map[string]any{"playing": false, "verified": false},
		"key_elements": []map[string]string{
			{"ref": "@e40", "label": `link "播放"`},
			{"ref": "@e6", "label": `link "播放"`},
		},
	})
	if len(actions) != 1 || actions[0].Label != "Resume playback" {
		t.Fatalf("unexpected QQ resume actions: %#v", actions)
	}
	if _, hasRef := actions[0].Arguments["ref"]; hasRef {
		t.Fatalf("QQ resume must use the stable primary control: %#v", actions[0].Arguments)
	}
}

func TestInspectQQMusicPlaybackControlUsesStableSelectors(t *testing.T) {
	var command string
	run := func(_ time.Duration, args ...string) (string, error) {
		command = strings.Join(args, " ")
		return `{"success":true,"data":{"result":{"found":true,"playing":false,"control":"autoplay_prompt","selector":".yqq-dialog-wrap .upload_btns__item"}}}`, nil
	}
	control, selector, playing, err := inspectQQMusicPlaybackControl(run, []string{"--session", "athena-test"})
	if err != nil || control != "autoplay_prompt" || selector != ".yqq-dialog-wrap .upload_btns__item" || playing {
		t.Fatalf("unexpected QQ Music control: control=%q selector=%q playing=%v err=%v", control, selector, playing, err)
	}
	if !strings.Contains(command, "eval") || !strings.Contains(command, "data-athena-play-control") {
		t.Fatalf("unexpected QQ Music inspection command: %s", command)
	}
}

func TestBrowserEvaluationNavigationError(t *testing.T) {
	if !isBrowserEvaluationNavigationError(errors.New("CDP error (Runtime.evaluate): Inspected target navigated or closed")) {
		t.Fatal("navigation after a playback click should be recoverable")
	}
}

func TestValidateBrowserPagePreconditionRejectsStaleOption(t *testing.T) {
	run := func(_ time.Duration, args ...string) (string, error) {
		if strings.Join(args, " ") == "get url" {
			return "https://www.youtube.com/watch?v=new", nil
		}
		return "", errors.New("unexpected command")
	}
	err := validateBrowserPagePrecondition(run, nil, "https://www.youtube.com/watch?v=old")
	if err == nil || !strings.Contains(err.Error(), "page changed") {
		t.Fatalf("expected stale page error, got %v", err)
	}
}

func TestValidateBrowserPagePreconditionAcceptsCanonicalRedirect(t *testing.T) {
	run := func(_ time.Duration, args ...string) (string, error) {
		if strings.Join(args, " ") == "get url" {
			return "https://www.youtube.com/", nil
		}
		return "", errors.New("unexpected command")
	}
	if err := validateBrowserPagePrecondition(run, nil, "https://www.youtube.com/?reload=9"); err != nil {
		t.Fatalf("canonical YouTube redirect was rejected: %v", err)
	}
	if normalizeBrowserPageURL("https://www.youtube.com/watch?v=one") == normalizeBrowserPageURL("https://www.youtube.com/watch?v=two") {
		t.Fatal("video identity query parameters must remain significant")
	}
}

func TestParseBrowserPlaybackStateFromAgentBrowserEnvelope(t *testing.T) {
	state, ok := parseBrowserPlaybackState(`{"success":true,"data":{"result":{"found":true,"playing":true,"paused":false,"kind":"dom_player","current_time":12.5,"media_id":"song-1","media_title":"First song","media_url":"https://y.qq.com/n/ryqq_v2/songDetail/song-1"}}}`)
	if !ok || !browserPlaybackVerified(state) {
		t.Fatalf("playback result was not parsed: ok=%v state=%#v", ok, state)
	}
	if state["verified"] != true || state["kind"] != "dom_player" || state["media_id"] != "song-1" {
		t.Fatalf("unexpected sanitized state: %#v", state)
	}
}

func TestBrowserPlaybackRequiresReadyOrProgressingMedia(t *testing.T) {
	buffering := map[string]any{
		"found": true, "playing": true, "paused": false,
		"kind": "video", "ready_state": float64(0), "current_time": float64(0), "progressed": false,
	}
	if browserPlaybackVerified(buffering) {
		t.Fatal("a stalled, unready media element was reported as playing")
	}
	buffering["ready_state"] = float64(3)
	if !browserPlaybackVerified(buffering) {
		t.Fatal("ready media was not reported as playing")
	}
	if !strings.Contains(browserPlaybackScript, "media.play() did not settle within 3000ms") {
		t.Fatal("media.play promise is not protected by an in-page timeout")
	}
}

func TestPauseBrowserPlaybackRequiresVerifiedPausedState(t *testing.T) {
	run := func(_ time.Duration, args ...string) (string, error) {
		if !strings.Contains(strings.Join(args, " "), "eval") {
			return "", errors.New("unexpected browser command")
		}
		return `{"success":true,"data":{"result":{"found":true,"playing":false,"paused":true,"ended":false,"kind":"video","current_time":12.5}}}`, nil
	}
	state, err := pauseBrowserPlayback(run, []string{"--session", "test"})
	if err != nil {
		t.Fatal(err)
	}
	if verified, _ := state["verified"].(bool); !verified {
		t.Fatalf("pause was not verified: %#v", state)
	}
}

func TestDismissSafeBrowserOverlayPrefersRejectAll(t *testing.T) {
	clicked := ""
	run := func(_ time.Duration, args ...string) (string, error) {
		command := strings.Join(args, " ")
		switch {
		case strings.Contains(command, "snapshot -i -c"):
			return "- button \"I accept\" [ref=e1]\n- button \"Reject all\" [ref=e2]\n- button \"Manage preferences\" [ref=e3]", nil
		case strings.Contains(command, "click @e2"):
			clicked = "@e2"
			return "done", nil
		case strings.Contains(command, "wait 400"):
			return "done", nil
		default:
			return "", errors.New("unexpected command: " + command)
		}
	}
	dismissed, err := dismissSafeBrowserOverlay(run, nil)
	if err != nil || !dismissed || clicked != "@e2" {
		t.Fatalf("safe overlay dismissal: dismissed=%v clicked=%q err=%v", dismissed, clicked, err)
	}
	if safeOverlayDismissPriority("I accept") < 100 {
		t.Fatal("optional cookie acceptance must never be selected automatically")
	}
}

func TestDismissSafeBrowserOverlayScansFullPageBeforeTruncatedSnapshot(t *testing.T) {
	commands := make([]string, 0, 2)
	run := func(_ time.Duration, args ...string) (string, error) {
		command := strings.Join(args, " ")
		commands = append(commands, command)
		switch {
		case strings.Contains(command, "eval"):
			return `{"data":{"result":{"clicked":true,"label":"reject all","priority":0}}}`, nil
		case strings.Contains(command, "wait 400"):
			return "done", nil
		default:
			return "", errors.New("unexpected command: " + command)
		}
	}
	dismissed, err := dismissSafeBrowserOverlay(run, nil)
	if err != nil || !dismissed {
		t.Fatalf("full-page safe overlay dismissal: dismissed=%v err=%v", dismissed, err)
	}
	for _, command := range commands {
		if strings.Contains(command, "snapshot") {
			t.Fatalf("truncated snapshot fallback was used after DOM dismissal: %q", command)
		}
	}
}

func TestValidateBrowserPlaybackTargetRejectsWrongQQSong(t *testing.T) {
	err := validateBrowserPlaybackTarget(map[string]any{
		"media_id": "actual", "media_title": "Actual song",
	}, "audio", "expected", "Expected song")
	if err == nil || !strings.Contains(err.Error(), "Actual song") || !strings.Contains(err.Error(), "Expected song") {
		t.Fatalf("wrong QQ Music track was not rejected: %v", err)
	}
	if err := validateBrowserPlaybackTarget(map[string]any{"media_id": "expected"}, "audio", "expected", "Expected song"); err != nil {
		t.Fatalf("matching QQ Music track was rejected: %v", err)
	}
	if err := validateBrowserPlaybackTarget(map[string]any{"playing": true}, "video", "expected", "Expected video"); err == nil {
		t.Fatal("targeted playback without observable media identity was accepted")
	}
}

func TestInferBrowserTaskKeepsCurrentPagePlaybackLocal(t *testing.T) {
	task := inferBrowserTask("播放当前视频", "", "")
	if !task.PlayResult || task.Target != "" || task.Query != "" || task.ResultOrdinal != 0 {
		t.Fatalf("current playback should not become a web search: %#v", task)
	}
}
