package perception

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUIUnderstandingFindsSearchWithoutSiteKnowledge(t *testing.T) {
	result := NewOrchestrator(DefaultBudget()).Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://unknown.example/catalog", "title": "Catalog",
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": `textbox "Search topics"`},
			{"ref": "@e2", "label": `button "Search"`},
			{"ref": "@e3", "label": `link "First topic" [url=/thread/1001]`},
			{"ref": "@e4", "label": `link "Second topic" [url=/thread/1002]`},
		},
	}, Providers{})

	model, ok := SemanticPage(result)
	if !ok || model.Type != "discussion" {
		t.Fatalf("page model = %#v", result["semantic_page"])
	}
	if len(model.Entities) != 2 || model.Entities[0].Kind != "discussion" {
		t.Fatalf("entities = %#v", model.Entities)
	}
	searchFound := false
	for _, interaction := range model.Interactions {
		if interaction.Kind == "search" && interaction.InputRef == "@e1" {
			searchFound = true
		}
	}
	if !searchFound {
		t.Fatalf("search interaction missing: %#v", model.Interactions)
	}
}

func TestUIUnderstandingBuildsSafeProfileSelectionModel(t *testing.T) {
	result := NewOrchestrator(DefaultBudget()).Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://stream.example/browse", "title": "Streaming",
		"content": "Who's watching?",
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": `link "Streaming" [url=https://stream.example/]`},
			{"ref": "@e2", "label": `link "Alice" [url=https://stream.example/SwitchProfile?tkn=secret-a]`},
			{"ref": "@e3", "label": `link "Kids" [url=https://stream.example/SwitchProfile?tkn=secret-b]`},
			{"ref": "@e4", "label": `link "Manage Profiles" [url=https://stream.example/ManageProfiles]`},
		},
	}, Providers{})

	model, ok := SemanticPage(result)
	if !ok || model.Type != "profile_selection" || len(model.Entities) != 2 {
		t.Fatalf("profile selection model = %#v", model)
	}
	for _, entity := range model.Entities {
		if entity.Kind != "profile" || entity.Ref == "" || entity.URL != "" {
			t.Fatalf("profile identity leaked its target URL: %#v", entity)
		}
	}
	interactions := InteractionCandidates(result)
	if len(interactions) != 2 || interactions[0].Label != "Alice" || interactions[0].Ref != "@e2" || interactions[0].TargetURL != "" {
		t.Fatalf("profile interactions = %#v", interactions)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "SwitchProfile") || strings.Contains(string(encoded), "secret-a") {
		t.Fatalf("profile target leaked through observation: %s", encoded)
	}
}

func TestNavigationLinksAreNotContentEntities(t *testing.T) {
	result := NewOrchestrator(DefaultBudget()).Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://unknown.example/", "title": "Videos",
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": `link "Home" [url=/]`},
			{"ref": "@e2", "label": `link "History" [url=/history]`},
			{"ref": "@e3", "label": `link "First useful video" [url=/video/one]`},
			{"ref": "@e4", "label": `link "Second useful video" [url=/video/two]`},
		},
	}, Providers{})

	model, _ := SemanticPage(result)
	if len(model.Entities) != 2 || model.Entities[0].Label != "First useful video" || model.Entities[1].Label != "Second useful video" {
		t.Fatalf("navigation polluted content entities: %#v", model.Entities)
	}
	second, ok := EntityAt(result, 2, "video")
	if !ok || second.URL != "/video/two" {
		t.Fatalf("second video = %#v, %v", second, ok)
	}
}

func TestObservedMediaCandidatesBecomeOrderedPlayableEntities(t *testing.T) {
	result := NewOrchestrator(DefaultBudget()).Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://player.example/", "title": "Media",
		"media_candidates": []map[string]any{
			{"title": "Song A", "url": "https://player.example/track/a", "kind": "audio"},
			{"title": "Song B", "url": "https://player.example/track/b", "kind": "audio"},
		},
	}, Providers{})

	model, _ := SemanticPage(result)
	if model.Type != "media_catalog" || len(model.Entities) != 2 || !model.Entities[0].Playable {
		t.Fatalf("media model = %#v", model)
	}
	second, ok := EntityAt(result, 2, "audio")
	if !ok || second.Label != "Song B" {
		t.Fatalf("second audio = %#v, %v", second, ok)
	}
}

func TestMusicLabelsDoNotTurnNavigationIntoPlayableAudio(t *testing.T) {
	result := NewOrchestrator(DefaultBudget()).Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://music.example/", "title": "Music",
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": `link "音乐馆" [url=/]`},
			{"ref": "@e2", "label": `link "我的音乐" [url=/profile]`},
			{"ref": "@e3", "label": `link "Focus playlist" [url=/playlist/42]`},
			{"ref": "@e4", "label": `link "First real song" [url=/songDetail/track-a]`},
			{"ref": "@e5", "label": `link "Second real song" [url=/track/track-b]`},
		},
	}, Providers{})

	model, _ := SemanticPage(result)
	first, ok := EntityAt(result, 1, "audio")
	if !ok || first.Label != "First real song" || first.URL != "/songDetail/track-a" {
		t.Fatalf("first playable audio = %#v, model=%#v", first, model)
	}
	for _, entity := range model.Entities {
		if (entity.Label == "音乐馆" || entity.Label == "我的音乐" || entity.Label == "Focus playlist") && entity.Playable {
			t.Fatalf("navigation or collection became playable: %#v", entity)
		}
	}
}

func TestFocusedDOMContentCandidatesRecoverLinksMissingFromAccessibility(t *testing.T) {
	result := NewOrchestrator(DefaultBudget()).Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://forum.example/", "title": "Forum",
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": `cell "First story title"`},
			{"ref": "@e2", "label": `cell "Second story title"`},
		},
		"content_candidates": []map[string]any{
			{"title": "First story title", "url": "https://source.example/first", "position": 100},
			{"title": "Second story title", "url": "https://source.example/second", "position": 200},
		},
	}, Providers{})

	second, ok := EntityAt(result, 2, "content")
	if !ok || second.Label != "Second story title" || second.URL != "https://source.example/second" {
		t.Fatalf("second focused DOM candidate = %#v, ok=%v", second, ok)
	}
}

func TestRepositoryLinksBecomeTypedEntities(t *testing.T) {
	result := NewOrchestrator(DefaultBudget()).Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://github.com/search?q=golang+agent&type=repositories", "title": "Repository search",
		"content_candidates": []map[string]any{
			{"title": "microsoft/retina", "url": "https://github.com/microsoft/retina", "position": 100},
			{"title": "google/adk-go", "url": "https://github.com/google/adk-go", "position": 200},
		},
	}, Providers{})

	second, ok := EntityAt(result, 2, "repository")
	if !ok || second.Label != "google/adk-go" || second.URL != "https://github.com/google/adk-go" {
		t.Fatalf("second repository = %#v, ok=%v", second, ok)
	}
}

func TestPageClassificationDoesNotDependOnKnownDomain(t *testing.T) {
	known := NewOrchestrator(DefaultBudget()).Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://www.youtube.com/", "title": "YouTube",
	}, Providers{})
	unknown := NewOrchestrator(DefaultBudget()).Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://unknown.example/", "title": "Unknown",
	}, Providers{})
	knownModel, _ := SemanticPage(known)
	unknownModel, _ := SemanticPage(unknown)
	if knownModel.Type != "web_page" || unknownModel.Type != "web_page" {
		t.Fatalf("domain influenced classification: known=%s unknown=%s", knownModel.Type, unknownModel.Type)
	}
}

func TestRepeatedArticleHeadingsDominateSingleEmbeddedPlayer(t *testing.T) {
	result := NewOrchestrator(DefaultBudget()).Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://community.example/", "title": "Developer Community",
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": `button "Play video"`},
		},
		"content_candidates": []map[string]any{
			{"title": "First engineering article", "url": "https://community.example/team/first", "context": "heading"},
			{"title": "Second engineering article", "url": "https://community.example/team/second", "context": "heading"},
			{"title": "Third engineering article", "url": "https://community.example/team/third", "context": "heading"},
		},
	}, Providers{})
	model, _ := SemanticPage(result)
	if model.Type != "content_feed" {
		t.Fatalf("single embedded player overrode article feed: %#v", model)
	}
}

func TestRepeatedStoryRowsDominateOccasionalVideoLink(t *testing.T) {
	result := NewOrchestrator(DefaultBudget()).Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://news.example/", "title": "Community News",
		"media_candidates": []map[string]any{
			{"title": "One external video story", "url": "https://video.example/watch?v=one", "kind": "video"},
		},
		"content_candidates": []map[string]any{
			{"title": "First community story", "url": "https://source.example/first", "context": "collection"},
			{"title": "Second community story", "url": "https://source.example/second", "context": "collection"},
			{"title": "Third community story", "url": "https://source.example/third", "context": "collection"},
			{"title": "Fourth community story", "url": "https://source.example/fourth", "context": "collection"},
			{"title": "Fifth community story", "url": "https://source.example/fifth", "context": "collection"},
		},
	}, Providers{})
	model, _ := SemanticPage(result)
	if model.Type != "content_feed" {
		t.Fatalf("single video link overrode story feed: %#v", model)
	}
}

func TestPrizeTextWithoutPurchaseActionIsNotCommerce(t *testing.T) {
	result := NewOrchestrator(DefaultBudget()).Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://community.example/challenge", "title": "Challenge winners",
		"content": "Announcing $5,000 in prizes for this year's developer challenge.",
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": `link "Read the winning project" [url=/projects/winner]`},
		},
	}, Providers{})
	model, _ := SemanticPage(result)
	if model.Type == "commerce" {
		t.Fatalf("prize announcement became commerce: %#v", model)
	}
}
