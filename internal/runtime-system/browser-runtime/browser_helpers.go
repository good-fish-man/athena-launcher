package browser_runtime

import (
	"net/url"
	"strings"
)

func cleanBrowserCommandOutput(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	clean := lines[:0]
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[agent-browser]") {
			continue
		}
		clean = append(clean, line)
	}
	return strings.TrimSpace(strings.Join(clean, "\n"))
}

func browserCommandURL(value string) string {
	value = cleanBrowserCommandOutput(value)
	lines := strings.Split(value, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		candidate := strings.TrimSpace(lines[index])
		parsed, err := url.Parse(candidate)
		if err != nil || parsed.Scheme == "" {
			continue
		}
		if parsed.Host != "" || parsed.Scheme == "about" || parsed.Scheme == "file" || parsed.Scheme == "data" {
			return candidate
		}
	}
	return ""
}

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
