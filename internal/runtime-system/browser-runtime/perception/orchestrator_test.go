package perception

import (
	"strings"
	"testing"
)

func TestUnderstandMediaCatalogFromStructure(t *testing.T) {
	result := NewOrchestrator(DefaultBudget()).Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://media.example.test/", "title": "Example Media",
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": `link "First clip" [url=/watch?v=one]`},
			{"ref": "@e2", "label": `link "Second clip" [url=/watch?v=two]`},
		},
	}, Providers{})
	model, ok := SemanticPage(result)
	if !ok || model.Type != "media_catalog" || len(model.Entities) != 2 || !model.Entities[1].Playable {
		t.Fatalf("unexpected semantic page: %#v", result["semantic_page"])
	}
}

func TestOrchestratorAppliesObservationBudget(t *testing.T) {
	budget := Budget{MaxElements: 2, MaxContentChars: 32, MaxSnapshotChars: 80, MaxCaptures: 1}
	orchestrator := NewOrchestrator(budget)
	result := orchestrator.Observe(Request{Action: "observe"}, map[string]any{
		"url":     "https://example.com",
		"title":   "Example",
		"content": strings.Repeat("page content ", 30),
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": "link First"},
			{"ref": "@e2", "label": "button Second"},
			{"ref": "@e3", "label": "textbox Third"},
		},
	}, Providers{})

	if len(result["content"].(string)) > budget.MaxContentChars {
		t.Fatalf("content exceeded budget: %q", result["content"])
	}
	if elements := result["key_elements"].([]map[string]string); len(elements) != budget.MaxElements {
		t.Fatalf("element budget not applied: %#v", elements)
	}
	perception := result["perception"].(map[string]any)
	privacy := perception["privacy"].(map[string]any)
	if privacy["raw_page_forwarded"] != false || privacy["raw_page_persisted"] != false {
		t.Fatalf("raw page privacy contract missing: %#v", privacy)
	}
}

func TestOrchestratorCapturesVisualIntentWithoutLLMDecision(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	captures := 0
	result := orchestrator.Observe(Request{
		Action: "observe",
		Arguments: map[string]any{
			"goal": "\u8bf7\u770b\u622a\u56fe\u91cc\u53f3\u8fb9\u7684\u89c6\u9891\u5c01\u9762",
		},
	}, map[string]any{
		"url": "https://www.youtube.com", "title": "YouTube",
		"key_elements": []map[string]string{{"ref": "@e1", "label": "link Video"}},
	}, Providers{Capture: func(request CaptureRequest) map[string]any {
		captures++
		if request.Reason != "visual_or_spatial_intent" || request.Scope != "viewport" {
			t.Fatalf("unexpected capture request: %#v", request)
		}
		return map[string]any{"available": true, "path": "/tmp/page.png"}
	}})

	if captures != 1 || result["screenshot"] == nil {
		t.Fatalf("visual intent did not trigger capture: captures=%d result=%#v", captures, result)
	}
}

func TestOrchestratorSkipsCaptureWhenSemanticEvidenceIsEnough(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	captures := 0
	orchestrator.Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://example.com", "title": "Example", "content": "A useful page",
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": "link One"},
			{"ref": "@e2", "label": "link Two"},
			{"ref": "@e3", "label": "button Three"},
			{"ref": "@e4", "label": "textbox Four"},
		},
	}, Providers{Capture: func(CaptureRequest) map[string]any {
		captures++
		return nil
	}})
	if captures != 0 {
		t.Fatalf("semantic-only observation unexpectedly captured %d screenshot(s)", captures)
	}
}

func TestSpatialPerceptionParsesBoundingBox(t *testing.T) {
	box := parseBoundingBox("button Play [box=10,20,120,40]")
	if box == nil || box.X != 10 || box.Y != 20 || box.Width != 120 || box.Height != 40 {
		t.Fatalf("bounding box not parsed: %#v", box)
	}
	spatial := buildSpatial([]Element{{Ref: "@e1", Label: "button Play", Box: box}})
	if spatial["located_element_count"] != 1 || spatial["mouse_mapping"] != "semantic_ref_preferred" {
		t.Fatalf("unexpected spatial observation: %#v", spatial)
	}
}

func TestScreenshotFalseOverridesAutomaticCapture(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	captures := 0
	result := orchestrator.Observe(Request{
		Action: "click", Arguments: map[string]any{"screenshot": false, "goal": "inspect image"},
	}, map[string]any{"challenge_detected": true}, Providers{Capture: func(CaptureRequest) map[string]any {
		captures++
		return nil
	}})
	if captures != 0 {
		t.Fatalf("explicit screenshot=false was ignored")
	}
	policy := result["perception"].(map[string]any)["orchestrator"].(map[string]any)["capture_policy"].(CaptureDecision)
	if policy.Reason != "disabled_by_request" {
		t.Fatalf("unexpected capture policy: %#v", policy)
	}
}

func TestVisualPerceptionRunsOCRFromCapturedEvidence(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	result := orchestrator.Observe(Request{
		Action: "observe", Arguments: map[string]any{"goal": "read text in image", "ocr_language": "eng"},
	}, map[string]any{"url": "https://example.com/image", "title": "Image"}, Providers{
		Capture: func(request CaptureRequest) map[string]any {
			return map[string]any{"available": true, "path": "/tmp/image.png", "scope": request.Scope}
		},
		OCR: func(request OCRRequest) map[string]any {
			if request.Path != "/tmp/image.png" || request.Language != "eng" || request.MaxChars != DefaultBudget().MaxOCRChars {
				t.Fatalf("unexpected OCR request: %#v", request)
			}
			return map[string]any{"available": true, "provider": "test", "text": "visible text"}
		},
	})
	visual := result["perception"].(map[string]any)["visual"].(map[string]any)
	ocr := visual["ocr"].(map[string]any)
	if ocr["available"] != true || ocr["text"] != "visible text" || ocr["requested"] != true {
		t.Fatalf("OCR evidence missing: %#v", visual)
	}
}

func TestSemanticPerceptionUsesObservedBoundingBoxes(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	result := orchestrator.Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://example.com", "title": "Example",
		"key_elements": []map[string]string{{"ref": "@e1", "label": "button Play"}},
		"element_boxes": map[string]any{
			"items": map[string]any{"@e1": map[string]float64{"x": 10, "y": 20, "width": 100, "height": 40}},
		},
	}, Providers{})
	spatial := result["perception"].(map[string]any)["spatial"].(map[string]any)
	if spatial["located_element_count"] != 1 {
		t.Fatalf("observed bounding box was not fused: %#v", spatial)
	}
}

func TestDocumentWithLittleSemanticTextAutomaticallyRunsOCR(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	ocrCalls := 0
	result := orchestrator.Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://example.com/report.pdf", "title": "Report PDF Viewer",
	}, Providers{
		Capture: func(request CaptureRequest) map[string]any {
			if request.Annotate {
				t.Fatalf("OCR screenshot must not be annotated: %#v", request)
			}
			return map[string]any{"available": true, "path": "/tmp/report.png", "scope": request.Scope}
		},
		OCR: func(OCRRequest) map[string]any {
			ocrCalls++
			return map[string]any{"available": true, "text": "Report text"}
		},
	})
	visual := result["perception"].(map[string]any)["visual"].(map[string]any)
	if ocrCalls != 1 || visual["ocr"].(map[string]any)["available"] != true {
		t.Fatalf("document OCR policy did not run: calls=%d visual=%#v", ocrCalls, visual)
	}
}

func TestAdaptivePolicyEscalatesUnstablePage(t *testing.T) {
	orchestrator := NewOrchestrator(DefaultBudget())
	result := orchestrator.Observe(Request{Action: "observe"}, map[string]any{
		"url": "https://example.com", "title": "Loading",
		"stabilization": map[string]any{"stable": false, "attempts": 4},
	}, Providers{})
	metadata := result["perception"].(map[string]any)
	orchestratorState := metadata["orchestrator"].(map[string]any)
	policy, ok := orchestratorState["adaptive_policy"].(AdaptivePolicy)
	if !ok || policy.Level != 3 || policy.Profile != "unstable_page" {
		t.Fatalf("adaptive policy = %#v", orchestratorState["adaptive_policy"])
	}
}
