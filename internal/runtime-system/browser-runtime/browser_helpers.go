package browser_runtime

import (
	"net/url"
	"strings"
)

func normalizeBrowserSessionKey(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		host := strings.TrimPrefix(strings.TrimPrefix(parsed.Hostname(), "www."), "m.")
		if parts := strings.Split(host, "."); len(parts) == 2 && isCommonBrowserSessionTLD(parts[1]) {
			host = parts[0]
		}
		if host != "" {
			return host
		}
	}
	return strings.Join(strings.Fields(value), " ")
}

func isCommonBrowserSessionTLD(value string) bool {
	switch value {
	case "com", "net", "org", "cn", "jp", "io", "ai", "dev":
		return true
	default:
		return false
	}
}

func browserChallengeDetected(state map[string]any) bool {
	detected, _ := state["challenge_detected"].(bool)
	return detected
}
