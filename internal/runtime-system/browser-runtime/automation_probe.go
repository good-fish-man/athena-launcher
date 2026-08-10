package browser_runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const browserAutomationProbeScript = `(() => {
  const visible = (element) => {
    if (!element) return false;
    const rect = element.getBoundingClientRect();
    const style = getComputedStyle(element);
    return rect.width > 1 && rect.height > 1 && style.display !== "none" && style.visibility !== "hidden";
  };
  const clean = (value) => String(value || "").replace(/\s+/g, " ").trim().slice(0, 240);
  const nodes = [...document.querySelectorAll('button,a[href],input,textarea,select,[role],video,audio')]
    .filter(visible)
    .slice(0, 120)
    .map((element) => {
      const role = clean(element.getAttribute('role') || element.tagName).toLowerCase();
      const name = clean(element.getAttribute('aria-label') || element.getAttribute('title') || element.getAttribute('placeholder') || element.innerText || element.textContent);
      const href = element.href ? clean(element.href) : '';
      let kind = '';
      if (element.tagName === 'VIDEO') kind = 'video';
      else if (element.tagName === 'AUDIO') kind = 'audio';
      return { role, name, kind, url: href };
    })
    .filter((item) => item.name || item.url || item.kind);
  const media = [...document.querySelectorAll('video,audio')].find(visible) || document.querySelector('video,audio');
  const body = clean(document.body ? document.body.innerText : '').toLowerCase().slice(0, 2000);
  const challenge = /captcha|verify you are human|unusual traffic|sign in to continue|log in to continue|二维码|验证码|驗證碼/.test(body);
  return {
    athena_probe: true,
    url: location.href,
    title: document.title || '',
    elements: nodes,
    media: media ? {
      playing: !media.paused && !media.ended,
      paused: Boolean(media.paused),
      ended: Boolean(media.ended),
      kind: String(media.tagName || '').toLowerCase()
    } : { playing: navigator.mediaSession && navigator.mediaSession.playbackState === 'playing', paused: false, ended: false, kind: 'media' },
    blocked: challenge
  };
})()`

func (b *browserController) probeAutomationState(ctx context.Context, sessionID string) (browserAutomationSnapshot, error) {
	lock := b.browserSessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()
	executable, err := b.executable()
	if err != nil {
		return browserAutomationSnapshot{}, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	args := append(b.sessionArgs(sessionID), "eval", browserAutomationProbeScript, "--json")
	output, err := b.browserCommand(commandCtx, executable, args...).CombinedOutput()
	if commandCtx.Err() != nil {
		return browserAutomationSnapshot{}, fmt.Errorf("browser automation probe timed out: %w", commandCtx.Err())
	}
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if len(detail) > 2000 {
			detail = detail[len(detail)-2000:]
		}
		return browserAutomationSnapshot{}, fmt.Errorf("browser automation probe: %w: %s", err, detail)
	}
	probe, ok := parseBrowserAutomationProbe(string(output))
	if !ok {
		return browserAutomationSnapshot{}, fmt.Errorf("browser automation probe returned an invalid payload")
	}
	return probe, nil
}

func parseBrowserAutomationProbe(output string) (browserAutomationSnapshot, bool) {
	var decoded any
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &decoded) != nil {
		return browserAutomationSnapshot{}, false
	}
	value, ok := findBrowserAutomationProbe(decoded)
	if !ok {
		return browserAutomationSnapshot{}, false
	}
	result := browserAutomationSnapshot{
		URL: browserStringValue(value["url"]), Title: browserStringValue(value["title"]),
		Elements: make(map[string]BrowserAutomationSelector),
	}
	if blocked, ok := value["blocked"].(bool); ok {
		result.Blocked = blocked
	}
	media, _ := value["media"].(map[string]any)
	result.Playing, _ = media["playing"].(bool)
	result.Paused, _ = media["paused"].(bool)
	result.Ended, _ = media["ended"].(bool)
	for _, raw := range browserAutomationMapSlice(value["elements"]) {
		selector := BrowserAutomationSelector{
			Role: browserStringValue(raw["role"]), Name: browserStringValue(raw["name"]),
			Kind: browserStringValue(raw["kind"]), URLContains: browserStringValue(raw["url"]),
		}
		key := browserAutomationSelectorKey(selector)
		if key != "" {
			result.Elements[key] = selector
		}
	}
	return result, true
}

func findBrowserAutomationProbe(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if probe, _ := typed["athena_probe"].(bool); probe {
			return typed, true
		}
		for _, key := range []string{"data", "result", "value"} {
			if nested, exists := typed[key]; exists {
				if result, found := findBrowserAutomationProbe(nested); found {
					return result, true
				}
			}
		}
	case string:
		var nested any
		if json.Unmarshal([]byte(strings.TrimSpace(typed)), &nested) == nil {
			return findBrowserAutomationProbe(nested)
		}
	}
	return nil, false
}

func browserAutomationMapSlice(value any) []map[string]any {
	values, _ := value.([]any)
	result := make([]map[string]any, 0, len(values))
	for _, item := range values {
		if current, ok := item.(map[string]any); ok {
			result = append(result, current)
		}
	}
	return result
}

func browserAutomationSelectorKey(value BrowserAutomationSelector) string {
	name := normalizeCurrentPageSelection(value.Name)
	url := strings.ToLower(strings.TrimSpace(value.URLContains))
	kind := strings.ToLower(strings.TrimSpace(value.Kind))
	role := strings.ToLower(strings.TrimSpace(value.Role))
	if name == "" && url == "" && kind == "" {
		return ""
	}
	return strings.Join([]string{role, name, kind, url}, "|")
}
