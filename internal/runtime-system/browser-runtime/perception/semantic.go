package perception

import (
	"fmt"
	"sort"
	"strings"
)

func buildSemantic(request Request, raw map[string]any, budget Budget, classification Classification) semanticResult {
	elements := parseElements(raw["key_elements"], raw["element_boxes"])
	rankElements(elements, request)
	if len(elements) > budget.MaxElements {
		elements = elements[:budget.MaxElements]
	}
	content := compactText(stringValue(raw["content"]), budget.MaxContentChars)
	snapshot := focusedSnapshot(elements, budget.MaxSnapshotChars)

	metadata := map[string]any{
		"url":       stringValue(raw["url"]),
		"title":     stringValue(raw["title"]),
		"page_type": classification.Type,
	}
	accessibilityElements := make([]map[string]any, 0, len(elements))
	ariaElements := make([]map[string]any, 0, len(elements))
	for _, element := range elements {
		item := map[string]any{"ref": element.Ref, "role": element.Role, "name": element.Name}
		if element.Focused {
			item["focused"] = true
		}
		if element.Disabled {
			item["disabled"] = true
		}
		accessibilityElements = append(accessibilityElements, item)
		ariaElements = append(ariaElements, map[string]any{
			"ref": element.Ref, "role": element.Role, "accessible_name": element.Name,
		})
	}
	return semanticResult{
		Metadata: metadata,
		Accessibility: map[string]any{
			"source": "accessibility_snapshot", "elements": accessibilityElements,
		},
		ARIA: map[string]any{
			"normalized": true, "elements": ariaElements,
		},
		FocusedDOM: map[string]any{
			"content_excerpt": content, "interactive_snapshot": snapshot, "element_count": len(elements),
		},
		Elements: elements,
		Content:  content,
		Snapshot: snapshot,
	}
}

func parseElements(value any, rawBoxes any) []Element {
	var items []map[string]any
	switch typed := value.(type) {
	case []map[string]string:
		for _, item := range typed {
			converted := make(map[string]any, len(item))
			for key, current := range item {
				converted[key] = current
			}
			items = append(items, converted)
		}
	case []map[string]any:
		items = typed
	case []any:
		for _, item := range typed {
			if converted, ok := item.(map[string]any); ok {
				items = append(items, converted)
			}
		}
	}
	boxes := parseElementBoxes(rawBoxes)
	elements := make([]Element, 0, len(items))
	for order, item := range items {
		ref := strings.TrimSpace(stringValue(item["ref"]))
		label := strings.TrimSpace(stringValue(item["label"]))
		if ref == "" || label == "" {
			continue
		}
		role, name := splitRoleAndName(label)
		box := parseBoundingBox(label)
		if observed := boxes[ref]; observed != nil {
			box = observed
		}
		elements = append(elements, Element{
			Ref: ref, Role: role, Name: name, Label: label, Order: order,
			Focused:  containsAny(strings.ToLower(label), "focused", "selected"),
			Disabled: containsAny(strings.ToLower(label), "disabled", "unavailable"),
			Box:      box,
		})
	}
	return elements
}

func parseElementBoxes(value any) map[string]*BoundingBox {
	result := make(map[string]*BoundingBox)
	container, _ := value.(map[string]any)
	if items, ok := container["items"].(map[string]any); ok {
		container = items
	}
	for ref, raw := range container {
		if box := boundingBoxFromValue(raw); box != nil {
			result[ref] = box
		}
	}
	return result
}

func boundingBoxFromValue(value any) *BoundingBox {
	values := make(map[string]float64, 4)
	switch typed := value.(type) {
	case map[string]float64:
		values = typed
	case map[string]any:
		for _, key := range []string{"x", "y", "width", "height"} {
			switch number := typed[key].(type) {
			case float64:
				values[key] = number
			case int:
				values[key] = float64(number)
			}
		}
	default:
		return nil
	}
	if len(values) != 4 {
		return nil
	}
	return &BoundingBox{X: values["x"], Y: values["y"], Width: values["width"], Height: values["height"]}
}

func splitRoleAndName(label string) (string, string) {
	fields := strings.Fields(strings.TrimSpace(label))
	if len(fields) == 0 {
		return "", ""
	}
	known := map[string]bool{
		"button": true, "link": true, "textbox": true, "combobox": true, "checkbox": true,
		"radio": true, "menuitem": true, "tab": true, "option": true, "heading": true,
		"img": true, "image": true, "slider": true, "switch": true,
	}
	role := strings.ToLower(strings.Trim(fields[0], "-[]"))
	if !known[role] {
		return "element", label
	}
	return role, strings.TrimSpace(strings.TrimPrefix(label, fields[0]))
}

func rankElements(elements []Element, request Request) {
	query := strings.ToLower(strings.Join(argumentStrings(request.Arguments), " "))
	for index := range elements {
		element := &elements[index]
		label := strings.ToLower(element.Label)
		element.Score = 1000 - element.Order
		if element.Focused {
			element.Score += 400
		}
		switch request.Action {
		case "type", "select":
			if containsAny(element.Role, "textbox", "combobox") {
				element.Score += 300
			}
		case "click", "hover", "drag", "navigate":
			if containsAny(element.Role, "button", "link", "tab") {
				element.Score += 180
			}
		}
		for _, term := range strings.Fields(query) {
			if len(term) > 2 && strings.Contains(label, term) {
				element.Score += 250
			}
		}
	}
	sort.SliceStable(elements, func(i, j int) bool {
		return elements[i].Score > elements[j].Score
	})
}

func compactText(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	seen := make(map[string]bool)
	lines := make([]string, 0, 32)
	used := 0
	for _, line := range strings.Split(value, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" || seen[line] {
			continue
		}
		seen[line] = true
		separator := 0
		if len(lines) > 0 {
			separator = 1
		}
		remaining := limit - used - separator
		if remaining <= 0 {
			break
		}
		if len(line) > remaining {
			line = line[:remaining]
		}
		lines = append(lines, line)
		used += separator + len(line)
		if used >= limit {
			break
		}
	}
	return strings.Join(lines, "\n")
}

func focusedSnapshot(elements []Element, limit int) string {
	lines := make([]string, 0, len(elements))
	for _, element := range elements {
		line := fmt.Sprintf("%s %s", element.Ref, element.Label)
		candidate := strings.Join(append(lines, line), "\n")
		if len(candidate) > limit {
			break
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func compatibleElements(elements []Element) []map[string]string {
	result := make([]map[string]string, 0, len(elements))
	for _, element := range elements {
		result = append(result, map[string]string{"ref": element.Ref, "label": element.Label})
	}
	return result
}
