package browser_runtime

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestParseBrowserElementBoxFromProviderJSON(t *testing.T) {
	box, ok := parseBrowserElementBox(`{"success":true,"data":{"x":10,"y":20,"width":300,"height":40}}`)
	if !ok || box["x"] != 10 || box["y"] != 20 || box["width"] != 300 || box["height"] != 40 {
		t.Fatalf("provider box not parsed: ok=%v box=%#v", ok, box)
	}
}

func TestProbeBrowserElementBoxesHonorsRefPriorityAndBudget(t *testing.T) {
	var probed []string
	runner := func(_ time.Duration, args ...string) (string, error) {
		joined := strings.Join(args, " ")
		for _, ref := range []string{"@e1", "@e2", "@e3"} {
			if strings.Contains(joined, "get box "+ref) {
				probed = append(probed, ref)
				return fmt.Sprintf(`{"data":{"x":1,"y":2,"width":3,"height":4}}`), nil
			}
		}
		return "", fmt.Errorf("unexpected command: %s", joined)
	}
	result := probeBrowserElementBoxes(map[string]any{
		"key_elements": []map[string]string{
			{"ref": "@e1", "label": "link One"},
			{"ref": "@e2", "label": "button Two"},
			{"ref": "@e3", "label": "button Three"},
		},
	}, browserExecuteRequest{Arguments: map[string]any{"ref": "@e3"}}, runner, nil, 2)
	if strings.Join(probed, ",") != "@e3,@e1" || result["count"] != 2 {
		t.Fatalf("spatial probe did not honor priority/budget: probed=%#v result=%#v", probed, result)
	}
}
