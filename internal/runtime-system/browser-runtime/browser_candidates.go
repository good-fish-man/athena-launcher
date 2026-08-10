package browser_runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"athena-launcher/internal/runtime-system/browser-runtime/siteknowledge"
)

var universalCandidateBundleScript = fmt.Sprintf(`(() => {
  const media = %s;
  const content = %s;
  return {
    media_candidates: media && Array.isArray(media.candidates) ? media.candidates : [],
    content_candidates: content && Array.isArray(content.candidates) ? content.candidates : []
  };
})()`, universalMediaCandidatesScript, universalContentCandidatesScript)

func enrichBrowserCandidates(run browserCommandRunner, sessionArgs []string, state map[string]any) map[string]any {
	if run == nil {
		return state
	}
	output, err := run(15*time.Second, append(sessionArgs, "eval", universalCandidateBundleScript, "--json")...)
	if err != nil {
		state = enrichBrowserMediaCandidates(run, sessionArgs, state)
		return enrichBrowserContentCandidates(run, sessionArgs, state)
	}
	mediaValues, contentValues, ok := parseBrowserCandidateBundle(output)
	if !ok {
		state = enrichBrowserMediaCandidates(run, sessionArgs, state)
		return enrichBrowserContentCandidates(run, sessionArgs, state)
	}
	mediaPayload, _ := json.Marshal(map[string]any{"candidates": mediaValues})
	contentPayload, _ := json.Marshal(map[string]any{"candidates": contentValues})
	mediaCandidates := parseBrowserMediaCandidates(string(mediaPayload), browserStringValue(state["url"]))
	if len(mediaCandidates) == 0 {
		mediaCandidates = knowledgeMediaCandidates(run, sessionArgs, state)
	}
	state = applyBrowserMediaCandidates(state, mediaCandidates)
	return applyBrowserContentCandidates(state, parseBrowserContentCandidates(string(contentPayload), browserStringValue(state["url"])))
}

func knowledgeMediaCandidates(run browserCommandRunner, sessionArgs []string, state map[string]any) []browserMediaCandidate {
	knowledge, matched := siteknowledge.MatchURL(browserStringValue(state["url"]))
	if !matched || knowledge.CandidateScript == "" {
		return nil
	}
	output, err := run(12*time.Second, append(sessionArgs, "eval", knowledge.CandidateScript, "--json")...)
	if err != nil {
		return nil
	}
	return parseBrowserMediaCandidates(output, browserStringValue(state["url"]))
}

func parseBrowserCandidateBundle(output string) ([]any, []any, bool) {
	var decoded any
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &decoded) != nil {
		return nil, nil, false
	}
	return findBrowserCandidateBundle(decoded)
}

func findBrowserCandidateBundle(value any) ([]any, []any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		media, hasMedia := typed["media_candidates"].([]any)
		content, hasContent := typed["content_candidates"].([]any)
		if hasMedia && hasContent {
			return media, content, true
		}
		for _, key := range []string{"data", "result", "value"} {
			if nested, exists := typed[key]; exists {
				if media, content, found := findBrowserCandidateBundle(nested); found {
					return media, content, true
				}
			}
		}
	case string:
		var nested any
		if json.Unmarshal([]byte(strings.TrimSpace(typed)), &nested) == nil {
			return findBrowserCandidateBundle(nested)
		}
	}
	return nil, nil, false
}
