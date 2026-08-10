package browser_runtime

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const browserSpatialProbeTimeout = 5 * time.Second

var browserBoxNumberPattern = regexp.MustCompile(`-?\d+(?:\.\d+)?`)

func probeBrowserElementBoxes(
	observation map[string]any,
	request browserExecuteRequest,
	run browserCommandRunner,
	sessionArgs []string,
	limit int,
) map[string]any {
	result := map[string]any{"available": run != nil, "items": map[string]any{}, "count": 0}
	if run == nil {
		result["reason"] = "browser_runner_not_available"
		return result
	}
	refs := browserSpatialRefs(observation, request.Arguments, limit)
	items := result["items"].(map[string]any)
	for _, ref := range refs {
		output, err := run(browserSpatialProbeTimeout, append(sessionArgs, "get", "box", ref, "--json")...)
		if err != nil {
			continue
		}
		if box, ok := parseBrowserElementBox(output); ok {
			items[ref] = box
		}
	}
	result["count"] = len(items)
	if len(items) == 0 {
		result["reason"] = "no_bounding_boxes_observed"
	}
	return result
}

func browserSpatialRefs(observation map[string]any, arguments map[string]any, limit int) []string {
	if limit <= 0 {
		limit = 8
	}
	refs := make([]string, 0, limit)
	seen := make(map[string]bool)
	appendRef := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] || !browserRefPattern.MatchString(ref) || len(refs) >= limit {
			return
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	if arguments != nil {
		appendRef(browserStringValue(arguments["ref"]))
	}
	switch elements := observation["key_elements"].(type) {
	case []map[string]string:
		for _, element := range elements {
			appendRef(element["ref"])
		}
	case []map[string]any:
		for _, element := range elements {
			appendRef(browserStringValue(element["ref"]))
		}
	case []any:
		for _, raw := range elements {
			if element, ok := raw.(map[string]any); ok {
				appendRef(browserStringValue(element["ref"]))
			}
		}
	}
	return refs
}

func browserStringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case fmt.Stringer:
		return strings.TrimSpace(typed.String())
	case nil:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func parseBrowserElementBox(output string) (map[string]float64, bool) {
	var parsed any
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &parsed) == nil {
		if box, ok := findBrowserElementBox(parsed); ok {
			return box, true
		}
	}
	numbers := browserBoxNumberPattern.FindAllString(output, -1)
	if len(numbers) < 4 {
		return nil, false
	}
	values := make([]float64, 4)
	for index := range values {
		value, err := strconv.ParseFloat(numbers[index], 64)
		if err != nil {
			return nil, false
		}
		values[index] = value
	}
	return map[string]float64{"x": values[0], "y": values[1], "width": values[2], "height": values[3]}, true
}

func findBrowserElementBox(value any) (map[string]float64, bool) {
	switch typed := value.(type) {
	case map[string]any:
		box := make(map[string]float64, 4)
		for _, key := range []string{"x", "y", "width", "height"} {
			if number, ok := numberValue(typed[key]); ok {
				box[key] = number
			}
		}
		if len(box) == 4 {
			return box, true
		}
		for _, key := range []string{"data", "box", "boundingBox", "result"} {
			if nested, ok := typed[key]; ok {
				if box, found := findBrowserElementBox(nested); found {
					return box, true
				}
			}
		}
	case []any:
		for _, item := range typed {
			if box, ok := findBrowserElementBox(item); ok {
				return box, true
			}
		}
	}
	return nil, false
}

func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}
