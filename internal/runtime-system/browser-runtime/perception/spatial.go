package perception

import (
	"regexp"
	"strconv"
	"strings"
)

var boundingBoxPattern = regexp.MustCompile(`(?i)(?:box|bbox)\s*[=:]\s*[\[(]?\s*(-?\d+(?:\.\d+)?)\s*[, ]\s*(-?\d+(?:\.\d+)?)\s*[, ]\s*(\d+(?:\.\d+)?)\s*[, ]\s*(\d+(?:\.\d+)?)`)

func parseBoundingBox(value string) *BoundingBox {
	match := boundingBoxPattern.FindStringSubmatch(value)
	if len(match) != 5 {
		return nil
	}
	values := make([]float64, 4)
	for index := range values {
		parsed, err := strconv.ParseFloat(match[index+1], 64)
		if err != nil {
			return nil
		}
		values[index] = parsed
	}
	return &BoundingBox{X: values[0], Y: values[1], Width: values[2], Height: values[3]}
}

func buildSpatial(elements []Element) map[string]any {
	items := make([]map[string]any, 0, len(elements))
	located := 0
	for _, element := range elements {
		item := map[string]any{
			"ref": element.Ref, "position": element.Order + 1,
		}
		if element.Box != nil {
			item["bounding_box"] = element.Box
			item["center"] = map[string]float64{
				"x": element.Box.X + element.Box.Width/2,
				"y": element.Box.Y + element.Box.Height/2,
			}
			located++
		}
		items = append(items, item)
	}
	return map[string]any{
		"coordinate_space":      "viewport_css_pixels",
		"mouse_mapping":         "semantic_ref_preferred",
		"coordinate_fallback":   "bounding_box_center",
		"located_element_count": located,
		"elements":              items,
		"note":                  strings.TrimSpace("Coordinates are emitted only when the browser snapshot provides a bounding box."),
	}
}
