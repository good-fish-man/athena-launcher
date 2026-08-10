package browser_runtime

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"
)

func TestTargetResolverSelectsRequestedFeedOrdinal(t *testing.T) {
	state := map[string]any{
		"url": "https://video.example/home",
		"semantic_page": perception.SemanticPageModel{
			Type: "media_catalog", Confidence: 0.92,
			Entities: []perception.SemanticEntity{
				{ID: "one", Kind: "video", Label: "First tutorial", URL: "https://video.example/watch/one", Position: 1, Playable: true, Confidence: 0.92},
				{ID: "two", Kind: "video", Label: "Second tutorial", URL: "https://video.example/watch/two", Position: 2, Playable: true, Confidence: 0.92},
			},
		},
	}
	task := inferredBrowserTask{ResultOrdinal: 2, PlayResult: true, PreferredKinds: []string{"video"}}
	resolution := newBrowserTargetResolver().Resolve(state, task, 2, "LOW")
	if resolution.Decision != browserResolutionExecute {
		t.Fatalf("decision = %q, reason=%q confidence=%.2f", resolution.Decision, resolution.Reason, resolution.Confidence)
	}
	if resolution.Selected == nil || resolution.Selected.ID != "two" {
		t.Fatalf("selected = %#v", resolution.Selected)
	}
	if resolution.Selected.Evidence.Ordinal < 0.9 {
		t.Fatalf("ordinal evidence = %.2f", resolution.Selected.Evidence.Ordinal)
	}
}

func TestTargetResolverFiltersCandidatesByQueryAndKind(t *testing.T) {
	state := map[string]any{
		"url": "https://media.example/search",
		"semantic_page": perception.SemanticPageModel{
			Type: "search_results", Confidence: 0.9,
			Entities: []perception.SemanticEntity{
				{ID: "article", Kind: "article", Label: "Athena architecture article", URL: "https://media.example/article", Position: 1, Confidence: 0.9},
				{ID: "video", Kind: "video", Label: "Athena Browser architecture", URL: "https://media.example/video/athena", Position: 2, Playable: true, Confidence: 0.94},
			},
		},
	}
	task := inferredBrowserTask{Query: "Athena Browser architecture", ResultOrdinal: 1, PlayResult: true, PreferredKinds: []string{"video"}}
	resolution := newBrowserTargetResolver().Resolve(state, task, 1, "LOW")
	if resolution.Decision != browserResolutionExecute || resolution.Selected == nil || resolution.Selected.ID != "video" {
		t.Fatalf("resolution = %#v", resolution)
	}
}

func TestTargetResolverRequestsVisualEvidenceBeforeVisualSelection(t *testing.T) {
	state := map[string]any{
		"url": "https://video.example/home",
		"semantic_page": perception.SemanticPageModel{
			Type: "media_catalog", Confidence: 0.9,
			Entities: []perception.SemanticEntity{
				{ID: "red", Kind: "video", Label: "Car review", URL: "https://video.example/watch/red", Position: 1, Playable: true, Confidence: 0.9},
			},
		},
	}
	task := inferredBrowserTask{Query: "video showing a red car", ResultOrdinal: 1, PlayResult: true, PreferredKinds: []string{"video"}}
	resolution := newBrowserTargetResolver().Resolve(state, task, 1, "LOW")
	if !resolution.RequiresVisual || resolution.Decision != browserResolutionReobserve || resolution.Reason != "visual_evidence_required" {
		t.Fatalf("resolution = %#v", resolution)
	}
}

func TestTargetResolverDoesNotTreatScreenshotAsCandidateMatch(t *testing.T) {
	state := map[string]any{
		"url": "https://video.example/home",
		"semantic_page": perception.SemanticPageModel{
			Type: "media_catalog", Confidence: 0.9,
			Entities: []perception.SemanticEntity{
				{ID: "red", Kind: "video", Label: "Red car review", URL: "https://video.example/watch/red", Position: 1, Playable: true, Confidence: 0.9},
			},
		},
		"perception": map[string]any{"visual": map[string]any{
			"viewport_screenshot": map[string]any{"available": true, "path": "/tmp/page.png"},
		}},
	}
	task := inferredBrowserTask{Query: "video showing a red car", ResultOrdinal: 1, PlayResult: true, PreferredKinds: []string{"video"}}
	resolution := newBrowserTargetResolver().Resolve(state, task, 1, "LOW")
	if resolution.Decision != browserResolutionReobserve || resolution.Selected == nil || resolution.Selected.Evidence.Visual >= 0.55 {
		t.Fatalf("resolution = %#v", resolution)
	}
}

func TestTargetResolverUsesCandidateVisualGrounding(t *testing.T) {
	state := map[string]any{
		"url": "https://video.example/home",
		"semantic_page": perception.SemanticPageModel{
			Type: "media_catalog", Confidence: 0.9,
			Entities: []perception.SemanticEntity{
				{ID: "red", Kind: "video", Label: "Red car review", URL: "https://video.example/watch/red", Position: 1, Playable: true, Confidence: 0.9},
			},
		},
		"visual_grounding": map[string]any{"matches": map[string]any{"red": 0.94}},
	}
	task := inferredBrowserTask{Query: "video showing a red car", ResultOrdinal: 1, PlayResult: true, PreferredKinds: []string{"video"}}
	resolution := newBrowserTargetResolver().Resolve(state, task, 1, "LOW")
	if resolution.Decision != browserResolutionExecute || resolution.Selected == nil || resolution.Selected.Evidence.Visual < 0.9 {
		t.Fatalf("resolution = %#v", resolution)
	}
}

func TestTargetResolverUsesFocusedScreenshotColorEvidence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidates.png")
	canvas := image.NewRGBA(image.Rect(0, 0, 200, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 200; x++ {
			pixel := color.RGBA{R: 35, G: 95, B: 220, A: 255}
			if x < 100 {
				pixel = color.RGBA{R: 220, G: 35, B: 35, A: 255}
			}
			canvas.Set(x, y, pixel)
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, canvas); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	state := map[string]any{
		"url": "https://example.com/actions",
		"key_elements": []any{
			map[string]any{"ref": "@e1", "label": "Primary action", "role": "button"},
			map[string]any{"ref": "@e2", "label": "Secondary action", "role": "button"},
		},
		"element_boxes": map[string]any{
			"@e1": map[string]any{"x": 0.0, "y": 0.0, "width": 100.0, "height": 80.0},
			"@e2": map[string]any{"x": 100.0, "y": 0.0, "width": 100.0, "height": 80.0},
		},
		"screenshot": map[string]any{"available": true, "path": path, "scope": "viewport"},
	}
	task := inferredBrowserTask{Query: "red button", PreferredRoles: []string{"button"}}
	resolution := newBrowserTargetResolver().Resolve(state, task, 1, "LOW")
	if resolution.Decision != browserResolutionExecute || resolution.Selected == nil || resolution.Selected.Ref != "@e1" {
		t.Fatalf("resolution = %#v", resolution)
	}
	if resolution.Selected.Evidence.Visual < 0.9 {
		t.Fatalf("visual evidence = %.2f", resolution.Selected.Evidence.Visual)
	}
}

func TestTargetResolverUsesSpatialBoxesForRightmostCandidate(t *testing.T) {
	state := map[string]any{
		"url": "https://video.example/home",
		"semantic_page": perception.SemanticPageModel{
			Type: "media_catalog", Confidence: 0.9,
			Entities: []perception.SemanticEntity{
				{ID: "left", Ref: "@e1", Kind: "video", Label: "Left tutorial", URL: "https://video.example/watch/left", Position: 1, Playable: true},
				{ID: "right", Ref: "@e2", Kind: "video", Label: "Right tutorial", URL: "https://video.example/watch/right", Position: 2, Playable: true},
			},
		},
		"element_boxes": map[string]any{
			"@e1": map[string]any{"x": 20.0, "y": 100.0, "width": 200.0, "height": 100.0},
			"@e2": map[string]any{"x": 600.0, "y": 100.0, "width": 200.0, "height": 100.0},
		},
	}
	task := inferredBrowserTask{Query: "video on the right", ResultOrdinal: 1, PlayResult: true, PreferredKinds: []string{"video"}}
	resolution := newBrowserTargetResolver().Resolve(state, task, 1, "LOW")
	if resolution.Decision != browserResolutionExecute || resolution.Selected == nil || resolution.Selected.ID != "right" {
		t.Fatalf("resolution = %#v", resolution)
	}
	if resolution.Selected.Evidence.Spatial < 0.9 {
		t.Fatalf("spatial evidence = %.2f", resolution.Selected.Evidence.Spatial)
	}
}

func TestTargetResolverDeduplicatesObservedSources(t *testing.T) {
	state := map[string]any{
		"url": "https://video.example/home",
		"semantic_page": perception.SemanticPageModel{
			Type: "media_catalog", Confidence: 0.9,
			Entities: []perception.SemanticEntity{
				{ID: "one", Kind: "video", Label: "One", URL: "https://video.example/watch/one", Position: 1, Playable: true, Confidence: 0.9},
			},
		},
		"media_candidates": []map[string]any{
			{"id": "one", "kind": "video", "title": "One video", "url": "https://video.example/watch/one", "position": 1},
		},
	}
	candidates := (browserCandidateResolver{}).Resolve(state, inferredBrowserTask{PlayResult: true, PreferredKinds: []string{"video"}})
	if len(candidates) != 1 {
		t.Fatalf("candidates = %#v", candidates)
	}
	if candidates[0].Source != "observed_media" {
		t.Fatalf("source = %q", candidates[0].Source)
	}
}

func TestTargetResolverUsesAnchorRelativeSpatialRelation(t *testing.T) {
	state := map[string]any{
		"url": "https://example.com",
		"key_elements": []any{
			map[string]any{"ref": "@e1", "label": "Search", "role": "textbox"},
			map[string]any{"ref": "@e2", "label": "Previous", "role": "button"},
			map[string]any{"ref": "@e3", "label": "Submit", "role": "button"},
		},
		"element_boxes": map[string]any{
			"@e1": map[string]any{"x": 200.0, "y": 20.0, "width": 300.0, "height": 40.0},
			"@e2": map[string]any{"x": 130.0, "y": 20.0, "width": 50.0, "height": 40.0},
			"@e3": map[string]any{"x": 520.0, "y": 20.0, "width": 80.0, "height": 40.0},
		},
	}
	task := inferredBrowserTask{Query: "button to the right of the search box", ResultOrdinal: 1}
	resolution := newBrowserTargetResolver().Resolve(state, task, 1, "LOW")
	if resolution.Decision != browserResolutionExecute || resolution.Selected == nil || resolution.Selected.Ref != "@e3" {
		t.Fatalf("resolution = %#v", resolution)
	}
	if resolution.SpatialRelation == nil || !resolution.SpatialRelation.Resolved || resolution.SpatialRelation.AnchorRef != "@e1" {
		t.Fatalf("spatial relation = %#v", resolution.SpatialRelation)
	}
}

func TestTargetResolverSelectsShortsImmediatelyRightOfAll(t *testing.T) {
	state := map[string]any{
		"url": "https://www.youtube.com/results?search_query=spider-man",
		"key_elements": []any{
			map[string]any{"ref": "@e1", "label": "All", "role": "button"},
			map[string]any{"ref": "@e2", "label": "Shorts", "role": "button"},
			map[string]any{"ref": "@e3", "label": "Shorts", "role": "link"},
		},
		"element_boxes": map[string]any{
			"@e1": map[string]any{"x": 500.0, "y": 80.0, "width": 60.0, "height": 40.0},
			"@e2": map[string]any{"x": 575.0, "y": 80.0, "width": 90.0, "height": 40.0},
			// A duplicate label elsewhere on the page must not satisfy "immediately right".
			"@e3": map[string]any{"x": 575.0, "y": 420.0, "width": 90.0, "height": 40.0},
		},
	}
	task := inferBrowserTask(
		"On the current YouTube page, click the Shorts filter in the top filter bar, immediately to the right of All.",
		"", "",
	)
	controller := &browserController{}
	plan := browserTaskPlan{}
	selected, ok := controller.resolveCurrentPageSelection(state, task, &plan)
	if !ok || selected.Ref != "@e2" {
		t.Fatalf("selected = %#v, ok=%v, resolution=%#v", selected, ok, plan.Resolution)
	}
	if plan.Resolution == nil || plan.Resolution.SpatialRelation == nil || !plan.Resolution.SpatialRelation.Immediate {
		t.Fatalf("immediate spatial relation = %#v", plan.Resolution)
	}
}

func TestTargetResolverReobservesWhenSpatialAnchorIsMissing(t *testing.T) {
	state := map[string]any{
		"key_elements":  []any{map[string]any{"ref": "@e1", "label": "Submit", "role": "button"}},
		"element_boxes": map[string]any{"@e1": map[string]any{"x": 520.0, "y": 20.0, "width": 80.0, "height": 40.0}},
	}
	resolution := newBrowserTargetResolver().Resolve(
		state, inferredBrowserTask{Query: "button to the right of the search box"}, 1, "LOW",
	)
	if resolution.Decision != browserResolutionReobserve || resolution.Reason != "spatial_anchor_not_observed" {
		t.Fatalf("resolution = %#v", resolution)
	}
}
