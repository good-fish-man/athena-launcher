package browser_runtime

import (
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"regexp"
	"strings"

	"athena-launcher/internal/runtime-system/browser-runtime/perception"
)

var (
	browserSemanticQueryWordPattern      = regexp.MustCompile(`[\pL\pN]+`)
	browserEnglishSpatialRelationPattern = regexp.MustCompile(`(?i)^(.*?)\s+(?:(immediately|directly|just)\s+)?(?:(?:to|on)\s+the\s+)?(right|left|above|below|under|over)\s+(?:of\s+)?(?:the\s+)?(.+)$`)
	browserChineseSpatialRelationPattern = regexp.MustCompile(`^(.+?)(?:的)?(右边|右邊|右侧|右側|左边|左邊|左侧|左側|上面|上方|下面|下方)(?:的)?(.+)$`)
)

type browserSpatialRelation struct {
	Direction   string                  `json:"direction"`
	TargetQuery string                  `json:"target_query,omitempty"`
	AnchorQuery string                  `json:"anchor_query"`
	AnchorRef   string                  `json:"anchor_ref,omitempty"`
	AnchorLabel string                  `json:"anchor_label,omitempty"`
	AnchorBox   *perception.BoundingBox `json:"anchor_box,omitempty"`
	Confidence  float64                 `json:"confidence"`
	Resolved    bool                    `json:"resolved"`
	Immediate   bool                    `json:"immediate,omitempty"`
}

func browserTaskSemanticQuery(task inferredBrowserTask) string {
	value := strings.ToLower(strings.TrimSpace(task.Query))
	if relation := parseBrowserSpatialRelation(task); relation != nil && relation.TargetQuery != "" {
		value = relation.TargetQuery
	}
	for _, phrase := range []string{
		"on the right", "on the left", "at the top", "at the bottom", "to the right", "to the left",
		"右边", "右邊", "右侧", "右側", "左边", "左邊", "左侧", "左側", "上面", "上方", "下面", "下方",
		"红色", "紅色", "蓝色", "藍色", "绿色", "綠色", "黄色", "黃色", "黑色", "白色",
	} {
		value = strings.ReplaceAll(value, phrase, " ")
	}
	stop := map[string]bool{
		"open": true, "click": true, "play": true, "select": true, "choose": true, "showing": true, "looks": true,
		"like": true, "the": true, "a": true, "an": true, "on": true, "at": true, "to": true, "of": true,
		"current": true, "page": true, "filter": true, "chip": true, "tab": true, "bar": true,
		"immediately": true, "directly": true, "just": true,
		"first": true, "second": true, "third": true, "1st": true, "2nd": true, "3rd": true,
		"video": true, "videos": true, "audio": true, "song": true, "track": true, "result": true, "item": true,
		"button": true, "control": true, "icon": true, "link": true, "按钮": true, "按鈕": true, "控件": true, "图标": true, "圖標": true,
		"red": true, "blue": true, "green": true, "yellow": true, "black": true, "white": true,
	}
	words := browserSemanticQueryWordPattern.FindAllString(value, -1)
	filtered := make([]string, 0, len(words))
	for _, word := range words {
		if !stop[word] {
			filtered = append(filtered, word)
		}
	}
	return strings.Join(filtered, " ")
}

func browserTaskRequiresSpatialGrounding(task inferredBrowserTask) bool {
	return browserTaskSpatialIntent(task) != ""
}

func browserTaskSpatialIntent(task inferredBrowserTask) string {
	if relation := parseBrowserSpatialRelation(task); relation != nil {
		return relation.Direction
	}
	value := strings.ToLower(strings.Join([]string{task.Query, task.Target}, " "))
	for _, candidate := range []struct {
		name  string
		terms []string
	}{
		{name: "right", terms: []string{"right", "右边", "右邊", "右侧", "右側"}},
		{name: "left", terms: []string{"left", "左边", "左邊", "左侧", "左側"}},
		{name: "bottom", terms: []string{"bottom", "below", "under", "下面", "下方", "底部"}},
		{name: "top", terms: []string{"top", "above", "上面", "上方", "顶部", "頂部"}},
	} {
		for _, term := range candidate.terms {
			if strings.Contains(value, term) {
				return candidate.name
			}
		}
	}
	return ""
}

func parseBrowserSpatialRelation(task inferredBrowserTask) *browserSpatialRelation {
	for _, raw := range []string{task.Query, task.Target} {
		value := strings.TrimSpace(raw)
		if value == "" {
			continue
		}
		if match := browserEnglishSpatialRelationPattern.FindStringSubmatch(value); len(match) == 5 {
			target := normalizeBrowserSpatialPhrase(match[1])
			anchor := normalizeBrowserSpatialPhrase(match[4])
			if anchor != "" {
				return &browserSpatialRelation{
					Direction: normalizeBrowserSpatialDirection(match[3]), TargetQuery: target, AnchorQuery: anchor,
					Immediate: strings.TrimSpace(match[2]) != "",
				}
			}
		}
		if match := browserChineseSpatialRelationPattern.FindStringSubmatch(value); len(match) == 4 {
			anchor := normalizeBrowserSpatialPhrase(match[1])
			target := normalizeBrowserSpatialPhrase(match[3])
			if anchor != "" && target != "" {
				return &browserSpatialRelation{
					Direction: normalizeBrowserSpatialDirection(match[2]), TargetQuery: target, AnchorQuery: anchor,
				}
			}
		}
	}
	return nil
}

func normalizeBrowserSpatialPhrase(value string) string {
	value = normalizeCurrentPageSelection(value)
	for {
		original := value
		for _, prefix := range []string{
			"click ", "open ", "choose ", "select ", "find ", "press ", "tap ", "the ",
			"点击", "點擊", "打开", "打開", "选择", "選擇", "找到", "查找", "按下",
		} {
			value = strings.TrimSpace(strings.TrimPrefix(value, prefix))
		}
		if value == original {
			break
		}
	}
	for _, marker := range []string{
		" in the top filter bar", " in top filter bar", " in the filter bar", " in filter bar",
		" on the top filter bar", " on top filter bar", " in the top bar", " on the top bar",
		" in the left sidebar", " in the right sidebar", " on the left sidebar", " on the right sidebar",
		" 位于顶部筛选栏", " 位於頂部篩選欄", " 在顶部筛选栏", " 在頂部篩選欄",
	} {
		if index := strings.Index(value, marker); index > 0 {
			value = strings.TrimSpace(value[:index])
			break
		}
	}
	for _, suffix := range []string{" immediately", " directly", " just", " 紧邻", " 緊鄰"} {
		value = strings.TrimSpace(strings.TrimSuffix(value, suffix))
	}
	return strings.TrimSpace(strings.TrimPrefix(value, "of "))
}

func normalizeBrowserSpatialDirection(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "right", "右边", "右邊", "右侧", "右側":
		return "right"
	case "left", "左边", "左邊", "左侧", "左側":
		return "left"
	case "above", "over", "上面", "上方":
		return "top"
	case "below", "under", "下面", "下方":
		return "bottom"
	default:
		return ""
	}
}

func resolveBrowserSpatialRelation(state map[string]any, task inferredBrowserTask) *browserSpatialRelation {
	relation := parseBrowserSpatialRelation(task)
	if relation == nil {
		return nil
	}
	bestScore := 0.0
	for _, element := range browserSemanticElements(state) {
		box := browserCandidateBox(state, element.Ref)
		if box == nil {
			continue
		}
		text := browserSpatialSemanticText(strings.Join([]string{browserElementTitle(element), element.Role, element.Kind}, " "))
		score := browserSemanticEvidence(browserSpatialSemanticText(relation.AnchorQuery), text)
		if score <= bestScore {
			continue
		}
		bestScore = score
		relation.AnchorRef = element.Ref
		relation.AnchorLabel = browserElementTitle(element)
		relation.AnchorBox = box
	}
	relation.Confidence = math.Round(bestScore*100) / 100
	relation.Resolved = relation.AnchorBox != nil && bestScore >= 0.55
	if !relation.Resolved {
		relation.AnchorRef, relation.AnchorLabel, relation.AnchorBox = "", "", nil
	}
	return relation
}

func browserSpatialSemanticText(value string) string {
	value = normalizeCurrentPageSelection(value)
	replacements := []struct{ old, replacement string }{
		{"搜索框", " search textbox input "}, {"搜尋框", " search textbox input "},
		{"输入框", " textbox input "}, {"輸入框", " textbox input "},
		{"按钮", " button "}, {"按鈕", " button "}, {"链接", " link "}, {"連結", " link "},
		{"菜单", " menu "}, {"菜單", " menu "}, {"视频", " video "}, {"影片", " video "},
	}
	for _, replacement := range replacements {
		value = strings.ReplaceAll(value, replacement.old, replacement.replacement)
	}
	if strings.Contains(value, "search box") {
		value += " textbox input"
	}
	return normalizeCurrentPageSelection(value)
}

func browserCandidateBox(state map[string]any, ref string) *perception.BoundingBox {
	ref = strings.TrimSpace(ref)
	if ref == "" || state == nil {
		return nil
	}
	if boxes, ok := state["element_boxes"].(map[string]any); ok {
		if box, found := browserBoundingBox(boxes[ref]); found {
			return box
		}
	}
	perceptionValue, _ := state["perception"].(map[string]any)
	spatial, _ := perceptionValue["spatial"].(map[string]any)
	for _, raw := range browserAnySlice(spatial["elements"]) {
		item, ok := raw.(map[string]any)
		if !ok || browserStringValue(item["ref"]) != ref {
			continue
		}
		if box, found := browserBoundingBox(item["bounding_box"]); found {
			return box
		}
	}
	return nil
}

func browserBoundingBox(value any) (*perception.BoundingBox, bool) {
	switch box := value.(type) {
	case *perception.BoundingBox:
		if box != nil && box.Width > 0 && box.Height > 0 {
			copy := *box
			return &copy, true
		}
	case perception.BoundingBox:
		if box.Width > 0 && box.Height > 0 {
			copy := box
			return &copy, true
		}
	case map[string]float64:
		result := &perception.BoundingBox{X: box["x"], Y: box["y"], Width: box["width"], Height: box["height"]}
		return result, result.Width > 0 && result.Height > 0
	case map[string]any:
		result := &perception.BoundingBox{
			X: browserFloatValue(box["x"]), Y: browserFloatValue(box["y"]),
			Width: browserFloatValue(box["width"]), Height: browserFloatValue(box["height"]),
		}
		return result, result.Width > 0 && result.Height > 0
	}
	return nil, false
}

func browserAnySlice(value any) []any {
	switch values := value.(type) {
	case []any:
		return values
	case []map[string]any:
		result := make([]any, len(values))
		for index := range values {
			result[index] = values[index]
		}
		return result
	default:
		return nil
	}
}

func browserSpatialEvidenceScore(candidate browserTargetCandidate, task inferredBrowserTask, relation *browserSpatialRelation) float64 {
	if !browserTaskRequiresSpatialGrounding(task) {
		return 0.75
	}
	if candidate.Box == nil {
		return 0
	}
	if relation != nil {
		if !relation.Resolved || relation.AnchorBox == nil || candidate.Ref == relation.AnchorRef {
			return 0
		}
		if relation.Immediate && !browserImmediateSpatialAlignment(candidate.Box, relation.AnchorBox, relation.Direction) {
			return 0
		}
		score, ok := browserRelativeSpatialScore(candidate.Box, relation.AnchorBox, relation.Direction)
		if !ok {
			return 0
		}
		return score
	}
	return 0.94
}

func browserImmediateSpatialAlignment(candidate, anchor *perception.BoundingBox, direction string) bool {
	if candidate == nil || anchor == nil {
		return false
	}
	candidateCenterX, candidateCenterY := candidate.X+candidate.Width/2, candidate.Y+candidate.Height/2
	anchorCenterX, anchorCenterY := anchor.X+anchor.Width/2, anchor.Y+anchor.Height/2
	switch direction {
	case "right", "left":
		tolerance := math.Max(24, math.Max(candidate.Height, anchor.Height)*0.8)
		return math.Abs(candidateCenterY-anchorCenterY) <= tolerance
	case "top", "bottom":
		tolerance := math.Max(24, math.Max(candidate.Width, anchor.Width)*0.8)
		return math.Abs(candidateCenterX-anchorCenterX) <= tolerance
	default:
		return false
	}
}

func browserSpatialOrderValue(candidate browserTargetCandidate, direction string, relation *browserSpatialRelation) (float64, bool) {
	if candidate.Box == nil {
		return 0, false
	}
	if relation != nil {
		if !relation.Resolved || relation.AnchorBox == nil || candidate.Ref == relation.AnchorRef {
			return 0, false
		}
		return browserRelativeSpatialDistance(candidate.Box, relation.AnchorBox, direction)
	}
	centerX := candidate.Box.X + candidate.Box.Width/2
	centerY := candidate.Box.Y + candidate.Box.Height/2
	switch direction {
	case "right":
		return -centerX, true
	case "left":
		return centerX, true
	case "bottom":
		return -centerY, true
	case "top":
		return centerY, true
	default:
		return 0, false
	}
}

func browserRelativeSpatialScore(candidate, anchor *perception.BoundingBox, direction string) (float64, bool) {
	distance, ok := browserRelativeSpatialDistance(candidate, anchor, direction)
	if !ok {
		return 0, false
	}
	proximity := 1 / (1 + distance/300)
	return clampBrowserScore(0.78 + 0.18*proximity), true
}

func browserRelativeSpatialDistance(candidate, anchor *perception.BoundingBox, direction string) (float64, bool) {
	if candidate == nil || anchor == nil {
		return 0, false
	}
	candidateCenterX, candidateCenterY := candidate.X+candidate.Width/2, candidate.Y+candidate.Height/2
	anchorCenterX, anchorCenterY := anchor.X+anchor.Width/2, anchor.Y+anchor.Height/2
	var axisGap, crossGap float64
	switch direction {
	case "right":
		if candidateCenterX <= anchorCenterX {
			return 0, false
		}
		axisGap, crossGap = math.Abs(candidate.X-(anchor.X+anchor.Width)), math.Abs(candidateCenterY-anchorCenterY)
	case "left":
		if candidateCenterX >= anchorCenterX {
			return 0, false
		}
		axisGap, crossGap = math.Abs(anchor.X-(candidate.X+candidate.Width)), math.Abs(candidateCenterY-anchorCenterY)
	case "bottom":
		if candidateCenterY <= anchorCenterY {
			return 0, false
		}
		axisGap, crossGap = math.Abs(candidate.Y-(anchor.Y+anchor.Height)), math.Abs(candidateCenterX-anchorCenterX)
	case "top":
		if candidateCenterY >= anchorCenterY {
			return 0, false
		}
		axisGap, crossGap = math.Abs(anchor.Y-(candidate.Y+candidate.Height)), math.Abs(candidateCenterX-anchorCenterX)
	default:
		return 0, false
	}
	return axisGap + crossGap*0.35, true
}

func browserVisualEvidenceScore(state map[string]any, candidate browserTargetCandidate, task inferredBrowserTask) float64 {
	if confidence, ok := browserExplicitVisualMatch(state, candidate); ok {
		return clampBrowserScore(confidence)
	}
	screenshot := browserScreenshotEvidence(state)
	path := browserStringValue(screenshot["path"])
	if path == "" {
		return 0
	}
	colorName := browserVisualColorIntent(task)
	box := candidate.Box
	if browserStringValue(screenshot["scope"]) == "element" && browserStringValue(screenshot["ref"]) == candidate.Ref {
		box = nil
	}
	if colorName == "" || (box == nil && browserStringValue(screenshot["scope"]) != "element") {
		// A screenshot is available for a later vision provider or HITL, but it
		// is not evidence that this particular candidate matches the goal.
		return 0.35
	}
	coverage, ok := browserCandidateColorCoverage(path, box, colorName)
	if !ok {
		return 0.35
	}
	return clampBrowserScore(0.35 + coverage*2.5)
}

func browserExplicitVisualMatch(state map[string]any, candidate browserTargetCandidate) (float64, bool) {
	for _, grounding := range browserVisualGroundingMaps(state) {
		if confidence, ok := browserCandidateGroundingConfidence(grounding, candidate); ok {
			return confidence, true
		}
	}
	return 0, false
}

func browserVisualGroundingMaps(state map[string]any) []map[string]any {
	result := make([]map[string]any, 0, 3)
	if grounding, ok := state["visual_grounding"].(map[string]any); ok {
		result = append(result, grounding)
	}
	perceptionValue, _ := state["perception"].(map[string]any)
	visual, _ := perceptionValue["visual"].(map[string]any)
	if grounding, ok := visual["target_grounding"].(map[string]any); ok {
		result = append(result, grounding)
	}
	if grounding, ok := visual["target_matches"].(map[string]any); ok {
		result = append(result, map[string]any{"matches": grounding})
	}
	return result
}

func browserCandidateGroundingConfidence(grounding map[string]any, candidate browserTargetCandidate) (float64, bool) {
	if grounding == nil {
		return 0, false
	}
	matches, _ := grounding["matches"].(map[string]any)
	if matches == nil {
		matches = grounding
	}
	for _, key := range browserCandidateGroundingKeys(candidate) {
		value, exists := matches[key]
		if !exists {
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			confidence := browserFloatValue(typed["confidence"])
			return confidence, confidence > 0
		default:
			confidence := browserFloatValue(typed)
			return confidence, confidence > 0
		}
	}
	return 0, false
}

func browserCandidateGroundingKeys(candidate browserTargetCandidate) []string {
	result := make([]string, 0, 5)
	for _, value := range []string{
		candidate.ID,
		candidate.Ref,
		normalizeBrowserPageURL(candidate.URL),
		normalizeCurrentPageSelection(candidate.Label),
	} {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func browserScreenshotPath(state map[string]any) string {
	return browserStringValue(browserScreenshotEvidence(state)["path"])
}

func browserScreenshotEvidence(state map[string]any) map[string]any {
	if state == nil {
		return nil
	}
	if path := browserStringValue(state["screenshot_path"]); path != "" {
		return map[string]any{"available": true, "path": path, "scope": "viewport"}
	}
	if screenshot, ok := state["screenshot"].(map[string]any); ok && browserScreenshotEvidenceAvailable(screenshot) {
		return screenshot
	}
	perceptionValue, _ := state["perception"].(map[string]any)
	visual, _ := perceptionValue["visual"].(map[string]any)
	for _, key := range []string{"element_screenshot", "viewport_screenshot", "full_page_screenshot"} {
		if evidence, ok := visual[key].(map[string]any); ok && browserScreenshotEvidenceAvailable(evidence) {
			return evidence
		}
	}
	return nil
}

func browserVisualColorIntent(task inferredBrowserTask) string {
	value := strings.ToLower(strings.Join([]string{task.Query, task.Target}, " "))
	for _, candidate := range []struct {
		name  string
		terms []string
	}{
		{name: "red", terms: []string{"red", "红色", "紅色"}},
		{name: "blue", terms: []string{"blue", "蓝色", "藍色"}},
		{name: "green", terms: []string{"green", "绿色", "綠色"}},
		{name: "yellow", terms: []string{"yellow", "黄色", "黃色"}},
		{name: "black", terms: []string{"black", "黑色"}},
		{name: "white", terms: []string{"white", "白色"}},
	} {
		for _, term := range candidate.terms {
			if strings.Contains(value, term) {
				return candidate.name
			}
		}
	}
	return ""
}

func browserCandidateColorCoverage(path string, box *perception.BoundingBox, colorName string) (float64, bool) {
	file, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer file.Close()
	decoded, _, err := image.Decode(file)
	if err != nil {
		return 0, false
	}
	bounds := decoded.Bounds()
	left, top, right, bottom := bounds.Min.X, bounds.Min.Y, bounds.Max.X, bounds.Max.Y
	if box != nil {
		left = max(bounds.Min.X, int(math.Floor(box.X)))
		top = max(bounds.Min.Y, int(math.Floor(box.Y)))
		right = min(bounds.Max.X, int(math.Ceil(box.X+box.Width)))
		bottom = min(bounds.Max.Y, int(math.Ceil(box.Y+box.Height)))
	}
	if right <= left || bottom <= top {
		return 0, false
	}
	step := max(1, int(math.Sqrt(float64((right-left)*(bottom-top))/4096)))
	matched, sampled := 0, 0
	for y := top; y < bottom; y += step {
		for x := left; x < right; x += step {
			rawR, rawG, rawB, _ := decoded.At(x, y).RGBA()
			r, g, b := float64(rawR>>8), float64(rawG>>8), float64(rawB>>8)
			if browserPixelMatchesColor(r, g, b, colorName) {
				matched++
			}
			sampled++
		}
	}
	if sampled == 0 {
		return 0, false
	}
	return float64(matched) / float64(sampled), true
}

func browserPixelMatchesColor(r, g, b float64, name string) bool {
	switch name {
	case "red":
		return r > 90 && r > g*1.35 && r > b*1.35
	case "blue":
		return b > 80 && b > r*1.25 && b > g*1.2
	case "green":
		return g > 75 && g > r*1.2 && g > b*1.15
	case "yellow":
		return r > 110 && g > 95 && b < math.Min(r, g)*0.7
	case "black":
		return r < 55 && g < 55 && b < 55
	case "white":
		return r > 205 && g > 205 && b > 205
	default:
		return false
	}
}
