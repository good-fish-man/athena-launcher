package browser_runtime

import (
	"context"
	"testing"
)

func TestStabilizeBrowserObservationWaitsForMatchingState(t *testing.T) {
	request := browserExecuteRequest{Action: "click", Arguments: map[string]any{"ref": "@e1"}}
	states := []map[string]any{
		{"url": "https://example.test/loading", "title": "Loading"},
		{"url": "https://example.test/done", "title": "Done"},
		{"url": "https://example.test/done", "title": "Done"},
	}
	index := 1
	result := stabilizeBrowserObservation(context.Background(), request, states[0], func() (map[string]any, error) {
		value := states[index]
		if index < len(states)-1 {
			index++
		}
		return value, nil
	})
	stabilization, _ := result["stabilization"].(map[string]any)
	if stable, _ := stabilization["stable"].(bool); !stable {
		t.Fatalf("stabilization = %#v", stabilization)
	}
	if result["title"] != "Done" {
		t.Fatalf("result = %#v", result)
	}
}

func TestStabilizeBrowserObservationCanBeDisabled(t *testing.T) {
	initial := map[string]any{"url": "https://example.test"}
	request := browserExecuteRequest{Action: "click", Arguments: map[string]any{"stabilize": false}}
	called := false
	result := stabilizeBrowserObservation(context.Background(), request, initial, func() (map[string]any, error) {
		called = true
		return nil, nil
	})
	if called || result["stabilization"] != nil {
		t.Fatalf("disabled stabilization changed result: %#v", result)
	}
}
