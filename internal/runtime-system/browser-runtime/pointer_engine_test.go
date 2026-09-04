package browser_runtime

import (
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

const pointerTestSession = "athena-0123456789abcdef0123456789abcdef"

type pointerTestCDP struct {
	mu       sync.Mutex
	document string
	hit      browserPointerHit
	inputs   []map[string]any
	server   *httptest.Server
}

func newPointerTestCDP(t *testing.T) *pointerTestCDP {
	t.Helper()
	fixture := &pointerTestCDP{
		document: "document-1",
		hit:      browserPointerHit{Found: true, Tag: "canvas"},
	}
	fixture.server = httptest.NewServer(websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: websocket.Handler(func(connection *websocket.Conn) {
			defer connection.Close()
			for {
				var command map[string]any
				if err := websocket.JSON.Receive(connection, &command); err != nil {
					return
				}
				id := command["id"]
				method, _ := command["method"].(string)
				result := map[string]any{}
				fixture.mu.Lock()
				switch method {
				case "Target.getTargets":
					result["targetInfos"] = []any{map[string]any{
						"targetId": "target-page", "type": "page", "url": "https://example.test/canvas",
					}}
				case "Target.attachToTarget":
					result["sessionId"] = "cdp-session-page"
				case "Page.getLayoutMetrics":
					result["cssVisualViewport"] = map[string]any{
						"clientWidth": 800, "clientHeight": 600, "offsetX": 0, "offsetY": 0,
						"pageX": 0, "pageY": 0, "scale": 1,
					}
				case "Runtime.evaluate":
					params, _ := command["params"].(map[string]any)
					expression, _ := params["expression"].(string)
					if strings.Contains(expression, "elementFromPoint") {
						result["result"] = map[string]any{"value": map[string]any{
							"found": fixture.hit.Found, "tag": fixture.hit.Tag, "role": fixture.hit.Role,
							"label": fixture.hit.Label, "input_type": fixture.hit.InputType,
							"semantic_available": fixture.hit.SemanticAvailable, "editable": fixture.hit.Editable,
							"frame": fixture.hit.Frame, "challenge": fixture.hit.Challenge,
						}}
					} else {
						result["result"] = map[string]any{"value": map[string]any{
							"athena_pointer_document": true, "document_id": fixture.document,
							"url": "https://example.test/canvas", "device_pixel_ratio": 2,
							"scroll_x": 0, "scroll_y": 0,
						}}
					}
				case "Input.dispatchMouseEvent":
					params, _ := command["params"].(map[string]any)
					fixture.inputs = append(fixture.inputs, params)
				}
				fixture.mu.Unlock()
				if err := websocket.JSON.Send(connection, map[string]any{"id": id, "result": result}); err != nil {
					return
				}
			}
		}),
	})
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *pointerTestCDP) endpoint() string {
	return "ws" + strings.TrimPrefix(f.server.URL, "http") + "/devtools/browser/test"
}

func (f *pointerTestCDP) setDocument(value string) {
	f.mu.Lock()
	f.document = value
	f.mu.Unlock()
}

func (f *pointerTestCDP) setHit(hit browserPointerHit) {
	f.mu.Lock()
	f.hit = hit
	f.mu.Unlock()
}

func (f *pointerTestCDP) inputSnapshot() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]map[string]any, len(f.inputs))
	copy(result, f.inputs)
	return result
}

func pointerTestScreenshot(t *testing.T, width, height int) string {
	t.Helper()
	path := t.TempDir() + "/viewport.png"
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	canvas := image.NewRGBA(image.Rect(0, 0, width, height))
	if err := png.Encode(file, canvas); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func pointerTestState(t *testing.T) map[string]any {
	return map[string]any{
		"tab_id": "tab-main", "url": "https://example.test/canvas",
		"screenshot": map[string]any{
			"available": true, "scope": "viewport", "path": pointerTestScreenshot(t, 1600, 1200),
			"artifact": map[string]any{"id": "image-0123456789abcdef", "sha256": strings.Repeat("a", 64)},
		},
	}
}

func TestBrowserPointerRejectsScreenshotCDPPageMismatch(t *testing.T) {
	cdp := newPointerTestCDP(t)
	engine := newBrowserPointerEngine()
	state := pointerTestState(t)
	state["url"] = "https://other.example.test/canvas"
	engine.attachGrounding(pointerTestSession, state, pointerTestRun(cdp.endpoint()), nil)
	grounding, ok := state["pointer_grounding"].(map[string]any)
	if !ok || grounding["available"] != false || grounding["reason"] != "cdp_page_mismatch" {
		t.Fatalf("mismatched page grounding = %#v", state["pointer_grounding"])
	}
}

func TestBrowserPointerObservedPageUsesActiveTabFallback(t *testing.T) {
	address, title := browserPointerObservedPage(map[string]any{
		"tab_id": "t2",
		"tabs": map[string]any{"items": []map[string]any{
			{"id": "t1", "url": "https://example.test/one", "title": "One"},
			{"id": "t2", "url": "https://example.test/two", "title": "Two", "active": true},
		}},
	})
	if address != "https://example.test/two" || title != "Two" {
		t.Fatalf("active pointer page = %q %q", address, title)
	}
}

func pointerTestRun(endpoint string) browserCommandRunner {
	return func(_ time.Duration, arguments ...string) (string, error) {
		joined := strings.Join(arguments, " ")
		if strings.Contains(joined, "get cdp-url") {
			return fmt.Sprintf(`{"data":{"cdp_url":%q}}`, endpoint), nil
		}
		return "", fmt.Errorf("unexpected agent-browser command: %s", joined)
	}
}

func pointerRequestFromState(state map[string]any, operation string) browserExecuteRequest {
	grounding := state["pointer_grounding"].(map[string]any)
	arguments := map[string]any{
		"operation": operation, "grounding_id": grounding["grounding_id"],
		"screenshot_id": grounding["screenshot_id"], "page_revision": grounding["page_revision"],
		"coordinate_space": "normalized_1000", "x": 500.0, "y": 500.0,
		"purpose": "Activate the unlabeled canvas control",
	}
	if operation == "drag" {
		arguments["target_x"], arguments["target_y"] = 750.0, 250.0
	}
	return browserExecuteRequest{SessionID: pointerTestSession, Action: "pointer", Arguments: arguments}
}

func TestBrowserPointerGroundingCalibratesAndDispatchesClick(t *testing.T) {
	cdp := newPointerTestCDP(t)
	engine := newBrowserPointerEngine()
	state := pointerTestState(t)
	engine.attachGrounding(pointerTestSession, state, pointerTestRun(cdp.endpoint()), []string{"--session", pointerTestSession})
	public, ok := state["pointer_grounding"].(map[string]any)
	if !ok || public["available"] != true || public["preferred_coordinate_space"] != "normalized_1000" {
		t.Fatalf("pointer grounding = %#v", state["pointer_grounding"])
	}
	result, err := engine.execute(pointerRequestFromState(state, "click"), pointerTestRun(cdp.endpoint()), nil)
	if err != nil {
		t.Fatal(err)
	}
	source := result["source"].(browserPointerPoint)
	if source.X != 400 || source.Y != 300 {
		t.Fatalf("calibrated source = %#v", source)
	}
	inputs := cdp.inputSnapshot()
	if len(inputs) != 3 || inputs[0]["type"] != "mouseMoved" || inputs[1]["type"] != "mousePressed" || inputs[2]["type"] != "mouseReleased" {
		t.Fatalf("pointer CDP sequence = %#v", inputs)
	}
	if _, err := engine.execute(pointerRequestFromState(state, "click"), pointerTestRun(cdp.endpoint()), nil); err == nil || !strings.Contains(err.Error(), "missing or expired") {
		t.Fatalf("single-use grounding was reusable: %v", err)
	}
}

func TestBrowserPointerScreenshotPixelCalibrationAccountsForDPR(t *testing.T) {
	grounding := browserPointerGrounding{
		ScreenshotWidth: 1600, ScreenshotHeight: 1200,
		Page: browserPointerPageContext{ViewportWidth: 800, ViewportHeight: 600, DevicePixelRatio: 2},
	}
	point, err := grounding.calibrate(browserPointerPoint{X: 800, Y: 600}, "screenshot_pixels")
	if err != nil || point.X != 400 || point.Y != 300 {
		t.Fatalf("screenshot calibration = %#v, %v", point, err)
	}
	if _, err := grounding.calibrate(browserPointerPoint{X: 1600, Y: 20}, "screenshot_pixels"); err == nil {
		t.Fatal("out-of-bounds screenshot coordinate was accepted")
	}
}

func TestBrowserPointerDispatchesBoundedDragSequence(t *testing.T) {
	cdp := newPointerTestCDP(t)
	engine := newBrowserPointerEngine()
	state := pointerTestState(t)
	engine.attachGrounding(pointerTestSession, state, pointerTestRun(cdp.endpoint()), nil)
	if _, err := engine.execute(pointerRequestFromState(state, "drag"), pointerTestRun(cdp.endpoint()), nil); err != nil {
		t.Fatal(err)
	}
	inputs := cdp.inputSnapshot()
	if len(inputs) != 11 {
		t.Fatalf("drag event count = %d, want 11: %#v", len(inputs), inputs)
	}
	if inputs[0]["type"] != "mouseMoved" || inputs[1]["type"] != "mousePressed" || inputs[len(inputs)-1]["type"] != "mouseReleased" {
		t.Fatalf("drag event boundaries = %#v", inputs)
	}
	for index, input := range inputs[2 : len(inputs)-1] {
		if input["type"] != "mouseMoved" || input["buttons"] != float64(1) && input["buttons"] != 1 {
			t.Fatalf("drag move %d = %#v", index, input)
		}
	}
}

func TestBrowserPointerRejectsStalePageRevision(t *testing.T) {
	cdp := newPointerTestCDP(t)
	engine := newBrowserPointerEngine()
	state := pointerTestState(t)
	engine.attachGrounding(pointerTestSession, state, pointerTestRun(cdp.endpoint()), nil)
	cdp.setDocument("document-2")
	if _, err := engine.execute(pointerRequestFromState(state, "click"), pointerTestRun(cdp.endpoint()), nil); err == nil || !strings.Contains(err.Error(), "STALE_GROUNDING") {
		t.Fatalf("stale page was accepted: %v", err)
	}
	if len(cdp.inputSnapshot()) != 0 {
		t.Fatal("stale grounding dispatched pointer input")
	}
}

func TestBrowserPointerPageRevisionTracksZoomScrollAndViewport(t *testing.T) {
	base := browserPointerPageContext{
		URL: "https://example.test/canvas", DocumentID: "document-1", TabID: "tab-main",
		ViewportWidth: 800, ViewportHeight: 600, Scale: 1, DevicePixelRatio: 2,
	}
	baseRevision := browserPointerPageRevision(base)
	variants := []browserPointerPageContext{base, base, base, base}
	variants[0].Scale = 1.25
	variants[1].ScrollY = 120
	variants[2].PageY = 120
	variants[3].ViewportOffsetX = 8
	for index, variant := range variants {
		if revision := browserPointerPageRevision(variant); revision == baseRevision {
			t.Fatalf("page revision ignored viewport variant %d: %#v", index, variant)
		}
	}
}

func TestBrowserPointerRejectsSemanticAndSensitiveTargets(t *testing.T) {
	for _, hit := range []browserPointerHit{
		{Found: true, Tag: "button", Role: "button", Label: "Continue", SemanticAvailable: true},
		{Found: true, Tag: "canvas", Label: "Place order"},
		{Found: true, Tag: "iframe", Frame: true},
		{Found: true, Tag: "canvas", Challenge: true},
	} {
		if err := validateBrowserPointerHit(hit); err == nil {
			t.Fatalf("unsafe hit was accepted: %#v", hit)
		}
	}
}

func TestBrowserPointerGroundingExpiresAndRejectsCorrelationMismatch(t *testing.T) {
	engine := newBrowserPointerEngine()
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return now }
	grounding := browserPointerGrounding{
		ID: "pointer-grounding-test", ScreenshotID: "image-test", SessionID: pointerTestSession,
		PageRevision: "page-revision-test", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	engine.store(grounding)
	command := browserPointerCommand{
		GroundingID: grounding.ID, ScreenshotID: "different-image", PageRevision: grounding.PageRevision,
	}
	if _, err := engine.lookup(command, pointerTestSession); err == nil || !strings.Contains(err.Error(), "correlation mismatch") {
		t.Fatalf("correlation mismatch was accepted: %v", err)
	}
	now = now.Add(2 * time.Minute)
	command.ScreenshotID = grounding.ScreenshotID
	if _, err := engine.lookup(command, pointerTestSession); err == nil || !strings.Contains(err.Error(), "missing or expired") {
		t.Fatalf("expired grounding was accepted: %v", err)
	}
}

func TestBrowserPointerStoresOnlyLatestGroundingPerSession(t *testing.T) {
	engine := newBrowserPointerEngine()
	now := time.Date(2026, time.September, 4, 12, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return now }
	first := browserPointerGrounding{
		ID: "pointer-grounding-first", ScreenshotID: "image-first", SessionID: pointerTestSession,
		PageRevision: "page-revision-first", CreatedAt: now, ExpiresAt: now.Add(time.Minute),
	}
	second := browserPointerGrounding{
		ID: "pointer-grounding-second", ScreenshotID: "image-second", SessionID: pointerTestSession,
		PageRevision: "page-revision-second", CreatedAt: now.Add(time.Second), ExpiresAt: now.Add(time.Minute),
	}
	engine.store(first)
	engine.store(second)
	if _, err := engine.lookup(browserPointerCommand{
		GroundingID: first.ID, ScreenshotID: first.ScreenshotID, PageRevision: first.PageRevision,
	}, pointerTestSession); err == nil {
		t.Fatal("superseded session grounding remained usable")
	}
	if _, err := engine.lookup(browserPointerCommand{
		GroundingID: second.ID, ScreenshotID: second.ScreenshotID, PageRevision: second.PageRevision,
	}, pointerTestSession); err != nil {
		t.Fatalf("latest session grounding was unavailable: %v", err)
	}
}

func TestParseBrowserPointerCommandRejectsNonNumericCoordinates(t *testing.T) {
	_, err := parseBrowserPointerCommand(map[string]any{
		"operation": "click", "grounding_id": "pointer-grounding-test", "screenshot_id": "image-test",
		"page_revision": "page-revision-test", "coordinate_space": "normalized_1000",
		"x": "500", "y": 500.0, "purpose": "canvas control",
	})
	if err == nil {
		t.Fatal("string coordinate was accepted")
	}
}
