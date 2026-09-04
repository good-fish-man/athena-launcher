package browser_runtime

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"math"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	browserPointerGroundingSchema = "athena.browser.pointer-grounding.v1"
	browserPointerResultSchema    = "athena.browser.pointer-result.v1"
	browserPointerGroundingTTL    = 2 * time.Minute
	browserPointerGroundingLimit  = 128
)

const browserPointerDocumentScript = `(() => {
  const key = "__athenaPointerDocumentV1";
  if (!window[key]) {
    const random = globalThis.crypto && typeof globalThis.crypto.randomUUID === "function"
      ? globalThis.crypto.randomUUID()
      : Math.random().toString(36).slice(2) + Date.now().toString(36);
    Object.defineProperty(window, key, {value: random, configurable: false, enumerable: false});
  }
  return {
    athena_pointer_document: true,
    document_id: String(window[key]),
    url: location.href,
    title: document.title,
    device_pixel_ratio: Number(window.devicePixelRatio || 1),
    scroll_x: Number(window.scrollX || 0),
    scroll_y: Number(window.scrollY || 0)
  };
})()`

type browserPointerPoint struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type browserPointerPageContext struct {
	URL              string  `json:"url"`
	Title            string  `json:"title"`
	DocumentID       string  `json:"-"`
	TabID            string  `json:"tab_id,omitempty"`
	ViewportWidth    float64 `json:"viewport_width"`
	ViewportHeight   float64 `json:"viewport_height"`
	ViewportOffsetX  float64 `json:"viewport_offset_x"`
	ViewportOffsetY  float64 `json:"viewport_offset_y"`
	PageX            float64 `json:"page_x"`
	PageY            float64 `json:"page_y"`
	Scale            float64 `json:"scale"`
	DevicePixelRatio float64 `json:"device_pixel_ratio"`
	ScrollX          float64 `json:"scroll_x"`
	ScrollY          float64 `json:"scroll_y"`
}

type browserPointerGrounding struct {
	ID               string
	ScreenshotID     string
	ScreenshotSHA256 string
	SessionID        string
	TabID            string
	PageRevision     string
	ScreenshotWidth  int
	ScreenshotHeight int
	Page             browserPointerPageContext
	CreatedAt        time.Time
	ExpiresAt        time.Time
	Used             bool
}

type browserPointerCommand struct {
	Operation       string
	GroundingID     string
	ScreenshotID    string
	PageRevision    string
	CoordinateSpace string
	Source          browserPointerPoint
	Target          browserPointerPoint
	HasTarget       bool
	Purpose         string
}

type browserPointerHit struct {
	Found             bool
	Tag               string
	Role              string
	Label             string
	InputType         string
	SemanticAvailable bool
	Editable          bool
	Frame             bool
	Challenge         bool
}

type browserPointerEngine struct {
	mu         sync.Mutex
	groundings map[string]browserPointerGrounding
	now        func() time.Time
}

func newBrowserPointerEngine() *browserPointerEngine {
	return &browserPointerEngine{groundings: make(map[string]browserPointerGrounding), now: time.Now}
}

func (e *browserPointerEngine) attachGrounding(
	sessionID string,
	state map[string]any,
	run browserCommandRunner,
	sessionArgs []string,
) {
	if e == nil || state == nil || run == nil {
		return
	}
	screenshot, _ := state["screenshot"].(map[string]any)
	if screenshot == nil || browserStringValue(screenshot["scope"]) != "viewport" {
		return
	}
	available, _ := screenshot["available"].(bool)
	artifact, _ := screenshot["artifact"].(map[string]any)
	path := browserStringValue(screenshot["path"])
	if !available || path == "" || artifact == nil {
		return
	}
	screenshotID := browserStringValue(artifact["id"])
	screenshotSHA256 := browserStringValue(artifact["sha256"])
	width, height, err := browserPointerPNGDimensions(path)
	if err != nil || screenshotID == "" || screenshotSHA256 == "" {
		state["pointer_grounding"] = browserPointerUnavailable("screenshot_calibration_failed", err)
		return
	}
	endpoint, err := browserPointerCDPEndpoint(run, sessionArgs)
	if err != nil {
		state["pointer_grounding"] = browserPointerUnavailable("cdp_unavailable", err)
		return
	}
	client, err := openBrowserPointerCDPClient(endpoint)
	if err != nil {
		state["pointer_grounding"] = browserPointerUnavailable("cdp_connection_failed", err)
		return
	}
	defer client.Close()
	tabID := browserStringValue(state["tab_id"])
	observedURL, observedTitle := browserPointerObservedPage(state)
	observedURL = normalizeBrowserPageURL(observedURL)
	if err := client.bindPage(observedURL, observedTitle); err != nil {
		state["pointer_grounding"] = browserPointerUnavailable("cdp_page_mismatch", err)
		return
	}
	page, err := client.pageContext(tabID)
	if err != nil || page.ViewportWidth <= 0 || page.ViewportHeight <= 0 {
		state["pointer_grounding"] = browserPointerUnavailable("page_calibration_failed", err)
		return
	}
	if observedURL != "" && observedURL != normalizeBrowserPageURL(page.URL) {
		state["pointer_grounding"] = browserPointerUnavailable("cdp_page_mismatch", fmt.Errorf("CDP page does not match the screenshot observation"))
		return
	}
	id, err := newBrowserPointerGroundingID()
	if err != nil {
		state["pointer_grounding"] = browserPointerUnavailable("grounding_id_failed", err)
		return
	}
	now := e.now().UTC()
	grounding := browserPointerGrounding{
		ID: id, ScreenshotID: screenshotID, ScreenshotSHA256: screenshotSHA256,
		SessionID: sessionID, TabID: tabID, PageRevision: browserPointerPageRevision(page),
		ScreenshotWidth: width, ScreenshotHeight: height, Page: page,
		CreatedAt: now, ExpiresAt: now.Add(browserPointerGroundingTTL),
	}
	e.store(grounding)
	public := grounding.public()
	screenshot["width"], screenshot["height"] = width, height
	screenshot["pointer_grounding"] = public
	state["pointer_grounding"] = public
}

func browserPointerObservedPage(state map[string]any) (string, string) {
	if state == nil {
		return "", ""
	}
	if address := browserStringValue(state["url"]); address != "" {
		return address, browserStringValue(state["title"])
	}
	tabID := browserStringValue(state["tab_id"])
	if tabs, _ := state["tabs"].(map[string]any); tabs != nil {
		if tabID == "" {
			tabID = browserStringValue(tabs["active_tab_id"])
		}
		var items []map[string]any
		switch raw := tabs["items"].(type) {
		case []map[string]any:
			items = raw
		case []any:
			for _, value := range raw {
				if item, ok := value.(map[string]any); ok {
					items = append(items, item)
				}
			}
		}
		for _, item := range items {
			if browserStringValue(item["id"]) == tabID || boolMapValue(item, "active") {
				if address := browserStringValue(item["url"]); address != "" {
					return address, browserStringValue(item["title"])
				}
			}
		}
	}
	if runtimeState, _ := state["browser_runtime"].(map[string]any); runtimeState != nil {
		if tab, _ := runtimeState["tab"].(map[string]any); tab != nil {
			return browserStringValue(tab["url"]), browserStringValue(tab["title"])
		}
	}
	return "", browserStringValue(state["title"])
}

func browserPointerUnavailable(reason string, err error) map[string]any {
	result := map[string]any{"schema": browserPointerGroundingSchema, "available": false, "reason": reason}
	if err != nil {
		result["error"] = err.Error()
	}
	return result
}

func (g browserPointerGrounding) public() map[string]any {
	return map[string]any{
		"schema": browserPointerGroundingSchema, "available": true,
		"grounding_id": g.ID, "screenshot_id": g.ScreenshotID, "page_revision": g.PageRevision,
		"session_id": g.SessionID, "tab_id": g.TabID,
		"coordinate_spaces":          []string{"normalized_1000", "screenshot_pixels"},
		"preferred_coordinate_space": "normalized_1000",
		"screenshot":                 map[string]any{"width": g.ScreenshotWidth, "height": g.ScreenshotHeight},
		"viewport": map[string]any{
			"width": g.Page.ViewportWidth, "height": g.Page.ViewportHeight,
			"offset_x": g.Page.ViewportOffsetX, "offset_y": g.Page.ViewportOffsetY,
			"page_x": g.Page.PageX, "page_y": g.Page.PageY, "scale": g.Page.Scale,
			"device_pixel_ratio": g.Page.DevicePixelRatio, "scroll_x": g.Page.ScrollX, "scroll_y": g.Page.ScrollY,
		},
		"operations": []string{"move", "click", "drag"},
		"single_use": true, "created_at": g.CreatedAt.Format(time.RFC3339Nano), "expires_at": g.ExpiresAt.Format(time.RFC3339Nano),
	}
}

func (e *browserPointerEngine) store(grounding browserPointerGrounding) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pruneLocked(e.now().UTC())
	for id, existing := range e.groundings {
		if existing.SessionID == grounding.SessionID {
			delete(e.groundings, id)
		}
	}
	if len(e.groundings) >= browserPointerGroundingLimit {
		var oldestID string
		var oldest time.Time
		for id, candidate := range e.groundings {
			if oldestID == "" || candidate.CreatedAt.Before(oldest) {
				oldestID, oldest = id, candidate.CreatedAt
			}
		}
		delete(e.groundings, oldestID)
	}
	e.groundings[grounding.ID] = grounding
}

func (e *browserPointerEngine) clearSession(sessionID string) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, grounding := range e.groundings {
		if grounding.SessionID == sessionID {
			delete(e.groundings, id)
		}
	}
}

func (e *browserPointerEngine) execute(
	request browserExecuteRequest,
	run browserCommandRunner,
	sessionArgs []string,
) (map[string]any, error) {
	if e == nil || run == nil {
		return nil, fmt.Errorf("browser pointer is unavailable")
	}
	command, err := parseBrowserPointerCommand(request.Arguments)
	if err != nil {
		return nil, err
	}
	grounding, err := e.lookup(command, request.SessionID)
	if err != nil {
		return nil, err
	}
	endpoint, err := browserPointerCDPEndpoint(run, sessionArgs)
	if err != nil {
		return nil, err
	}
	client, err := openBrowserPointerCDPClient(endpoint)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if err := client.bindPage(normalizeBrowserPageURL(grounding.Page.URL), grounding.Page.Title); err != nil {
		return nil, err
	}
	current, err := client.pageContext(grounding.TabID)
	if err != nil {
		return nil, err
	}
	if currentRevision := browserPointerPageRevision(current); currentRevision != grounding.PageRevision {
		return nil, fmt.Errorf("STALE_GROUNDING: browser page, viewport, zoom, or scroll state changed; capture a new viewport screenshot")
	}
	source, err := grounding.calibrate(command.Source, command.CoordinateSpace)
	if err != nil {
		return nil, err
	}
	target := browserPointerPoint{}
	if command.HasTarget {
		target, err = grounding.calibrate(command.Target, command.CoordinateSpace)
		if err != nil {
			return nil, err
		}
	}
	if browserPointerSensitiveLabel(command.Purpose) {
		return nil, fmt.Errorf("browser pointer rejected a sensitive or consequential purpose")
	}
	for _, point := range pointerCommandPoints(command, source, target) {
		hit, hitErr := client.hitTest(point)
		if hitErr != nil {
			return nil, hitErr
		}
		if err := validateBrowserPointerHit(hit); err != nil {
			return nil, err
		}
	}
	current, err = client.pageContext(grounding.TabID)
	if err != nil {
		return nil, err
	}
	if currentRevision := browserPointerPageRevision(current); currentRevision != grounding.PageRevision {
		return nil, fmt.Errorf("STALE_GROUNDING: browser page changed during pointer validation; capture a new viewport screenshot")
	}
	if err := e.consume(grounding.ID); err != nil {
		return nil, err
	}
	if err := client.dispatch(command.Operation, source, target); err != nil {
		return nil, err
	}
	return map[string]any{
		"schema": browserPointerResultSchema, "executed": true, "operation": command.Operation,
		"grounding_id": grounding.ID, "screenshot_id": grounding.ScreenshotID,
		"page_revision": grounding.PageRevision, "coordinate_space": "viewport_css_pixels",
		"source": source, "target": pointerTargetValue(command, target),
		"before_screenshot_sha256": grounding.ScreenshotSHA256,
	}, nil
}

func pointerCommandPoints(command browserPointerCommand, source, target browserPointerPoint) []browserPointerPoint {
	if command.HasTarget {
		return []browserPointerPoint{source, target}
	}
	return []browserPointerPoint{source}
}

func pointerTargetValue(command browserPointerCommand, target browserPointerPoint) any {
	if !command.HasTarget {
		return nil
	}
	return target
}

func (e *browserPointerEngine) lookup(command browserPointerCommand, sessionID string) (browserPointerGrounding, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now().UTC()
	e.pruneLocked(now)
	grounding, ok := e.groundings[command.GroundingID]
	if !ok {
		return browserPointerGrounding{}, fmt.Errorf("pointer grounding is missing or expired; capture a new viewport screenshot")
	}
	if grounding.Used {
		return browserPointerGrounding{}, fmt.Errorf("pointer grounding has already been used; capture a new viewport screenshot")
	}
	if grounding.SessionID != strings.TrimSpace(sessionID) || grounding.ScreenshotID != command.ScreenshotID || grounding.PageRevision != command.PageRevision {
		return browserPointerGrounding{}, fmt.Errorf("pointer grounding correlation mismatch")
	}
	return grounding, nil
}

func (e *browserPointerEngine) consume(id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	grounding, ok := e.groundings[id]
	if !ok || grounding.Used {
		return fmt.Errorf("pointer grounding is no longer available")
	}
	grounding.Used = true
	e.groundings[id] = grounding
	return nil
}

func (e *browserPointerEngine) pruneLocked(now time.Time) {
	for id, grounding := range e.groundings {
		if grounding.Used || !grounding.ExpiresAt.After(now) {
			delete(e.groundings, id)
		}
	}
}

func parseBrowserPointerCommand(arguments map[string]any) (browserPointerCommand, error) {
	command := browserPointerCommand{
		Operation: strings.ToLower(browserStringValue(arguments["operation"])), GroundingID: browserStringValue(arguments["grounding_id"]),
		ScreenshotID: browserStringValue(arguments["screenshot_id"]), PageRevision: browserStringValue(arguments["page_revision"]),
		CoordinateSpace: strings.ToLower(browserStringValue(arguments["coordinate_space"])), Purpose: browserStringValue(arguments["purpose"]),
	}
	if command.Operation != "move" && command.Operation != "click" && command.Operation != "drag" {
		return command, fmt.Errorf("browser pointer operation must be move, click, or drag")
	}
	if !strings.HasPrefix(command.GroundingID, "pointer-grounding-") || command.ScreenshotID == "" || command.PageRevision == "" {
		return command, fmt.Errorf("browser pointer requires correlated grounding_id, screenshot_id, and page_revision")
	}
	if command.CoordinateSpace != "normalized_1000" && command.CoordinateSpace != "screenshot_pixels" {
		return command, fmt.Errorf("browser pointer coordinate_space must be normalized_1000 or screenshot_pixels")
	}
	if command.Purpose == "" || len([]rune(command.Purpose)) > 300 {
		return command, fmt.Errorf("browser pointer purpose is required and must not exceed 300 characters")
	}
	var ok bool
	if command.Source.X, ok = browserPointerArgument(arguments, "x"); !ok {
		return command, fmt.Errorf("browser pointer requires a finite x coordinate")
	}
	if command.Source.Y, ok = browserPointerArgument(arguments, "y"); !ok {
		return command, fmt.Errorf("browser pointer requires a finite y coordinate")
	}
	if command.Operation == "drag" {
		if command.Target.X, ok = browserPointerArgument(arguments, "target_x"); !ok {
			return command, fmt.Errorf("browser pointer drag requires a finite target_x coordinate")
		}
		if command.Target.Y, ok = browserPointerArgument(arguments, "target_y"); !ok {
			return command, fmt.Errorf("browser pointer drag requires a finite target_y coordinate")
		}
		command.HasTarget = true
		if command.Source == command.Target {
			return command, fmt.Errorf("browser pointer drag requires different source and target coordinates")
		}
	}
	return command, nil
}

func browserPointerArgument(arguments map[string]any, key string) (float64, bool) {
	value, exists := arguments[key]
	if !exists {
		return 0, false
	}
	switch value.(type) {
	case float64, float32, int, int64, json.Number:
	default:
		return 0, false
	}
	number := browserFloatValue(value)
	return number, number >= 0 && !math.IsNaN(number) && !math.IsInf(number, 0)
}

func (g browserPointerGrounding) calibrate(point browserPointerPoint, coordinateSpace string) (browserPointerPoint, error) {
	var x, y float64
	switch coordinateSpace {
	case "normalized_1000":
		if point.X > 1000 || point.Y > 1000 {
			return browserPointerPoint{}, fmt.Errorf("normalized pointer coordinates must be between 0 and 1000")
		}
		x = point.X * g.Page.ViewportWidth / 1000
		y = point.Y * g.Page.ViewportHeight / 1000
	case "screenshot_pixels":
		if g.ScreenshotWidth <= 0 || g.ScreenshotHeight <= 0 || point.X >= float64(g.ScreenshotWidth) || point.Y >= float64(g.ScreenshotHeight) {
			return browserPointerPoint{}, fmt.Errorf("pointer coordinate is outside the grounded screenshot")
		}
		x = point.X * g.Page.ViewportWidth / float64(g.ScreenshotWidth)
		y = point.Y * g.Page.ViewportHeight / float64(g.ScreenshotHeight)
	default:
		return browserPointerPoint{}, fmt.Errorf("unsupported pointer coordinate space")
	}
	if x < 0 || y < 0 || x >= g.Page.ViewportWidth || y >= g.Page.ViewportHeight {
		return browserPointerPoint{}, fmt.Errorf("calibrated pointer coordinate is outside the current CSS viewport")
	}
	return browserPointerPoint{X: x, Y: y}, nil
}

func validateBrowserPointerHit(hit browserPointerHit) error {
	if !hit.Found {
		return fmt.Errorf("browser pointer target is not visible at the grounded coordinate")
	}
	if hit.Challenge {
		return fmt.Errorf("browser pointer cannot operate on a challenge or verification page")
	}
	if hit.Frame {
		return fmt.Errorf("browser pointer cannot inspect a framed target safely; use a semantic action or user takeover")
	}
	if hit.Editable || strings.EqualFold(hit.InputType, "password") {
		return fmt.Errorf("browser pointer cannot operate on editable or credential controls")
	}
	if browserPointerSensitiveLabel(strings.Join([]string{hit.Label, hit.Role, hit.InputType}, " ")) {
		return fmt.Errorf("browser pointer rejected a sensitive or consequential target")
	}
	if hit.SemanticAvailable {
		return fmt.Errorf("semantic_target_available: use browser.action with the observed element ref instead of browser.pointer")
	}
	return nil
}

func browserPointerSensitiveLabel(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if browserSensitiveActionLabel(value) || browserSensitiveInputLabel(value) {
		return true
	}
	for _, marker := range []string{
		"consent", "accept all", "agree", "authorize", "allow access", "download", "upload", "sign in", "log in", "create account",
		"同意", "全部接受", "允许", "授權", "授权", "登录", "登入", "下载", "下載", "上传", "上傳",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func browserPointerPNGDimensions(path string) (int, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, 0, err
	}
	defer file.Close()
	config, err := png.DecodeConfig(file)
	if err != nil {
		return 0, 0, err
	}
	if config.Width <= 0 || config.Height <= 0 {
		return 0, 0, fmt.Errorf("screenshot dimensions are invalid")
	}
	return config.Width, config.Height, nil
}

func browserPointerCDPEndpoint(run browserCommandRunner, sessionArgs []string) (string, error) {
	output, err := run(8*time.Second, append(sessionArgs, "get", "cdp-url", "--json")...)
	if err != nil {
		return "", fmt.Errorf("read browser pointer CDP endpoint: %w", err)
	}
	endpoint, ok := parseBrowserCDPEndpoint(output)
	if !ok {
		return "", fmt.Errorf("agent-browser did not return a valid local CDP websocket endpoint for pointer control")
	}
	return endpoint, nil
}

type browserPointerCDPClient struct {
	connection   *websocket.Conn
	nextID       int
	browserLevel bool
	sessionID    string
}

type browserPointerCDPResponse struct {
	ID     int            `json:"id,omitempty"`
	Result map[string]any `json:"result,omitempty"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func openBrowserPointerCDPClient(endpoint string) (*browserPointerCDPClient, error) {
	validated, ok := validateBrowserCDPEndpoint(endpoint)
	if !ok {
		return nil, fmt.Errorf("browser pointer CDP endpoint is not a trusted loopback websocket")
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, Proxy: nil}
	connection, response, err := dialer.Dial(validated, nil)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, fmt.Errorf("connect browser pointer CDP session: %w", err)
	}
	parsed, _ := url.Parse(validated)
	return &browserPointerCDPClient{
		connection: connection, browserLevel: strings.HasPrefix(parsed.Path, "/devtools/browser/"),
	}, nil
}

func (c *browserPointerCDPClient) Close() error {
	if c == nil || c.connection == nil {
		return nil
	}
	return c.connection.Close()
}

func (c *browserPointerCDPClient) call(method string, params map[string]any) (map[string]any, error) {
	if c == nil || c.connection == nil {
		return nil, fmt.Errorf("browser pointer CDP connection is unavailable")
	}
	c.nextID++
	id := c.nextID
	command := map[string]any{"id": id, "method": method}
	if c.sessionID != "" && !strings.HasPrefix(method, "Target.") {
		command["sessionId"] = c.sessionID
	}
	if len(params) > 0 {
		command["params"] = params
	}
	deadline := time.Now().Add(10 * time.Second)
	_ = c.connection.SetWriteDeadline(deadline)
	if err := c.connection.WriteJSON(command); err != nil {
		return nil, fmt.Errorf("send browser pointer CDP %s: %w", method, err)
	}
	for {
		var response browserPointerCDPResponse
		_ = c.connection.SetReadDeadline(deadline)
		if err := c.connection.ReadJSON(&response); err != nil {
			return nil, fmt.Errorf("receive browser pointer CDP %s: %w", method, err)
		}
		if response.ID != id {
			continue
		}
		if response.Error != nil {
			return nil, fmt.Errorf("browser pointer CDP %s failed (%d): %s", method, response.Error.Code, response.Error.Message)
		}
		return response.Result, nil
	}
}

func (c *browserPointerCDPClient) bindPage(expectedURL, expectedTitle string) error {
	if c == nil || !c.browserLevel {
		return nil
	}
	result, err := c.call("Target.getTargets", nil)
	if err != nil {
		return fmt.Errorf("list browser pointer CDP targets: %w", err)
	}
	targets, _ := result["targetInfos"].([]any)
	type pageTarget struct {
		ID       string
		Title    string
		Attached bool
	}
	matches := make([]pageTarget, 0, 1)
	for _, raw := range targets {
		target, _ := raw.(map[string]any)
		if !strings.EqualFold(browserStringValue(target["type"]), "page") {
			continue
		}
		if expectedURL != "" && normalizeBrowserPageURL(browserStringValue(target["url"])) != expectedURL {
			continue
		}
		if targetID := browserStringValue(target["targetId"]); targetID != "" {
			matches = append(matches, pageTarget{
				ID: targetID, Title: browserStringValue(target["title"]), Attached: boolMapValue(target, "attached"),
			})
		}
	}
	if len(matches) == 0 {
		return fmt.Errorf("no CDP page matches the screenshot URL")
	}
	selected := matches[0]
	if len(matches) > 1 {
		bestScore, tied := -1, false
		for _, candidate := range matches {
			sessionID, attachErr := c.attachPageTarget(candidate.ID)
			if attachErr != nil {
				continue
			}
			c.sessionID = sessionID
			inspected, inspectErr := c.call("Runtime.evaluate", map[string]any{
				"expression":    `({url:location.href,title:document.title,visibility:document.visibilityState,focused:document.hasFocus()})`,
				"returnByValue": true,
			})
			c.sessionID = ""
			_, _ = c.call("Target.detachFromTarget", map[string]any{"sessionId": sessionID})
			page, ok := browserPointerRuntimeValue(inspected)
			if inspectErr != nil || !ok || normalizeBrowserPageURL(browserStringValue(page["url"])) != expectedURL {
				continue
			}
			score := 0
			if expectedTitle != "" && candidate.Title == expectedTitle {
				score += 2
			}
			if expectedTitle != "" && browserStringValue(page["title"]) == expectedTitle {
				score += 3
			}
			if strings.EqualFold(browserStringValue(page["visibility"]), "visible") {
				score += 4
			}
			if boolMapValue(page, "focused") {
				score += 8
			}
			if candidate.Attached {
				score++
			}
			if score > bestScore {
				selected, bestScore, tied = candidate, score, false
			} else if score == bestScore {
				tied = true
			}
		}
		if bestScore < 0 || tied {
			return fmt.Errorf("multiple CDP pages match the screenshot URL and no unique active page could be verified")
		}
	}
	sessionID, err := c.attachPageTarget(selected.ID)
	if err != nil {
		return fmt.Errorf("attach browser pointer CDP target: %w", err)
	}
	c.sessionID = sessionID
	return nil
}

func (c *browserPointerCDPClient) attachPageTarget(targetID string) (string, error) {
	attached, err := c.call("Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true})
	if err != nil {
		return "", err
	}
	sessionID := browserStringValue(attached["sessionId"])
	if sessionID == "" {
		return "", fmt.Errorf("browser pointer CDP target returned no session id")
	}
	return sessionID, nil
}

func (c *browserPointerCDPClient) pageContext(tabID string) (browserPointerPageContext, error) {
	layout, err := c.call("Page.getLayoutMetrics", nil)
	if err != nil {
		return browserPointerPageContext{}, err
	}
	visual, _ := layout["cssVisualViewport"].(map[string]any)
	if visual == nil {
		visual, _ = layout["cssLayoutViewport"].(map[string]any)
	}
	evaluated, err := c.call("Runtime.evaluate", map[string]any{
		"expression": browserPointerDocumentScript, "returnByValue": true, "awaitPromise": false,
	})
	if err != nil {
		return browserPointerPageContext{}, err
	}
	document, ok := browserPointerRuntimeValue(evaluated)
	if !ok || browserStringValue(document["document_id"]) == "" {
		return browserPointerPageContext{}, fmt.Errorf("browser pointer could not establish a document identity")
	}
	width := firstBrowserFloat(visual, "clientWidth", "width")
	height := firstBrowserFloat(visual, "clientHeight", "height")
	if width <= 0 || height <= 0 {
		return browserPointerPageContext{}, fmt.Errorf("browser pointer received invalid CSS viewport metrics")
	}
	scale := firstBrowserFloat(visual, "scale")
	if scale <= 0 {
		scale = 1
	}
	dpr := browserFloatValue(document["device_pixel_ratio"])
	if dpr <= 0 {
		dpr = 1
	}
	return browserPointerPageContext{
		URL: browserStringValue(document["url"]), Title: browserStringValue(document["title"]),
		DocumentID: browserStringValue(document["document_id"]), TabID: tabID,
		ViewportWidth: width, ViewportHeight: height,
		ViewportOffsetX: firstBrowserFloat(visual, "offsetX"), ViewportOffsetY: firstBrowserFloat(visual, "offsetY"),
		PageX: firstBrowserFloat(visual, "pageX"), PageY: firstBrowserFloat(visual, "pageY"), Scale: scale,
		DevicePixelRatio: dpr, ScrollX: browserFloatValue(document["scroll_x"]), ScrollY: browserFloatValue(document["scroll_y"]),
	}, nil
}

func firstBrowserFloat(values map[string]any, keys ...string) float64 {
	for _, key := range keys {
		if value, exists := values[key]; exists {
			return browserFloatValue(value)
		}
	}
	return 0
}

func browserPointerRuntimeValue(result map[string]any) (map[string]any, bool) {
	remote, _ := result["result"].(map[string]any)
	value, ok := remote["value"].(map[string]any)
	return value, ok
}

func browserPointerPageRevision(page browserPointerPageContext) string {
	payload, _ := json.Marshal(struct {
		URL, Title, DocumentID, TabID                                                 string
		ViewportWidth, ViewportHeight, ViewportOffsetX, ViewportOffsetY, PageX, PageY float64
		Scale, DevicePixelRatio, ScrollX, ScrollY                                     float64
	}{
		page.URL, page.Title, page.DocumentID, page.TabID,
		page.ViewportWidth, page.ViewportHeight, page.ViewportOffsetX, page.ViewportOffsetY, page.PageX, page.PageY,
		page.Scale, page.DevicePixelRatio, page.ScrollX, page.ScrollY,
	})
	digest := sha256.Sum256(payload)
	return "page-revision-" + hex.EncodeToString(digest[:12])
}

func (c *browserPointerCDPClient) hitTest(point browserPointerPoint) (browserPointerHit, error) {
	coordinates, _ := json.Marshal(map[string]float64{"x": point.X, "y": point.Y})
	expression := fmt.Sprintf(`(() => {
  const point = %s;
  const element = document.elementFromPoint(point.x, point.y);
  if (!element) return {athena_pointer_hit:true, found:false};
  const semantic = element.closest('button,a[href],input,textarea,select,[contenteditable="true"],[role="button"],[role="link"],[role="textbox"],[role="checkbox"],[role="radio"],[role="switch"],[role="menuitem"],[role="option"],[role="slider"]');
  const target = semantic || element;
  const clean = value => String(value || '').replace(/\s+/g, ' ').trim().slice(0, 300);
  const body = clean(document.body ? document.body.innerText : '').toLowerCase().slice(0, 2400);
  return {
    athena_pointer_hit: true,
    found: true,
    tag: clean(target.tagName).toLowerCase(),
    role: clean(target.getAttribute && target.getAttribute('role')).toLowerCase(),
    label: clean((target.getAttribute && (target.getAttribute('aria-label') || target.getAttribute('title') || target.getAttribute('placeholder'))) || target.innerText || target.textContent),
    input_type: clean(target.getAttribute && target.getAttribute('type')).toLowerCase(),
    semantic_available: Boolean(semantic),
    editable: Boolean(target.isContentEditable || /^(input|textarea|select)$/i.test(target.tagName || '')),
    frame: /^(iframe|frame)$/i.test(element.tagName || ''),
    challenge: /captcha|verify you are human|unusual traffic|sign in to continue|log in to continue|二维码|验证码|驗證碼/.test(body)
  };
})()`, string(coordinates))
	result, err := c.call("Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": false})
	if err != nil {
		return browserPointerHit{}, err
	}
	value, ok := browserPointerRuntimeValue(result)
	if !ok {
		return browserPointerHit{}, fmt.Errorf("browser pointer hit test returned no value")
	}
	return browserPointerHit{
		Found: boolMapValue(value, "found"), Tag: browserStringValue(value["tag"]), Role: browserStringValue(value["role"]),
		Label: browserStringValue(value["label"]), InputType: browserStringValue(value["input_type"]),
		SemanticAvailable: boolMapValue(value, "semantic_available"), Editable: boolMapValue(value, "editable"),
		Frame: boolMapValue(value, "frame"), Challenge: boolMapValue(value, "challenge"),
	}, nil
}

func boolMapValue(values map[string]any, key string) bool {
	value, _ := values[key].(bool)
	return value
}

func (c *browserPointerCDPClient) dispatch(operation string, source, target browserPointerPoint) error {
	move := func(point browserPointerPoint, buttons int) error {
		_, err := c.call("Input.dispatchMouseEvent", map[string]any{
			"type": "mouseMoved", "x": point.X, "y": point.Y, "button": "none", "buttons": buttons, "pointerType": "mouse",
		})
		return err
	}
	press := func(point browserPointerPoint) error {
		_, err := c.call("Input.dispatchMouseEvent", map[string]any{
			"type": "mousePressed", "x": point.X, "y": point.Y, "button": "left", "buttons": 1, "clickCount": 1, "pointerType": "mouse",
		})
		return err
	}
	release := func(point browserPointerPoint) error {
		_, err := c.call("Input.dispatchMouseEvent", map[string]any{
			"type": "mouseReleased", "x": point.X, "y": point.Y, "button": "left", "buttons": 0, "clickCount": 1, "pointerType": "mouse",
		})
		return err
	}
	if err := move(source, 0); err != nil || operation == "move" {
		return err
	}
	if err := press(source); err != nil {
		return err
	}
	if operation == "drag" {
		const steps = 8
		for step := 1; step <= steps; step++ {
			ratio := float64(step) / steps
			point := browserPointerPoint{X: source.X + (target.X-source.X)*ratio, Y: source.Y + (target.Y-source.Y)*ratio}
			if err := move(point, 1); err != nil {
				_, _ = c.call("Input.dispatchMouseEvent", map[string]any{
					"type": "mouseReleased", "x": point.X, "y": point.Y, "button": "left", "buttons": 0, "clickCount": 0, "pointerType": "mouse",
				})
				return err
			}
			time.Sleep(16 * time.Millisecond)
		}
		return release(target)
	}
	return release(source)
}

func newBrowserPointerGroundingID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create browser pointer grounding id: %w", err)
	}
	return "pointer-grounding-" + hex.EncodeToString(value), nil
}
