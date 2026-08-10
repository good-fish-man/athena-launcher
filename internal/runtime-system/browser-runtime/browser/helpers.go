package browser

import (
	"encoding/json"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var browserElementRefPattern = regexp.MustCompile(`^@e[0-9]+$`)

func truncateBrowserOutput(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return value[:limit]
}

func shouldCaptureBrowserScreenshot(request Request) bool {
	if request.Arguments != nil {
		if disabled, ok := request.Arguments["screenshot"].(bool); ok && !disabled {
			return false
		}
		if requested, ok := request.Arguments["screenshot"].(bool); ok && requested {
			return true
		}
		if internal, _ := request.Arguments["perception_capture"].(bool); internal {
			return true
		}
	}
	return request.Action == "screenshot"
}

func browserScreenshotScope(arguments map[string]any) string {
	value, _ := arguments["screenshot_scope"].(string)
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "element":
		return "element"
	case "full", "full_page":
		return "full_page"
	default:
		return "viewport"
	}
}

func browserScreenshotRef(arguments map[string]any) string {
	value, _ := arguments["ref"].(string)
	value = strings.TrimSpace(value)
	if !browserElementRefPattern.MatchString(value) {
		return ""
	}
	return value
}

func browserBoolArgument(arguments map[string]any, key string) bool {
	value, _ := arguments[key].(bool)
	return value
}

func redactBrowserScreenshotOutput(output string) any {
	output = strings.TrimSpace(output)
	var parsed any
	if json.Unmarshal([]byte(output), &parsed) == nil {
		return redactBrowserJSON(parsed)
	}
	return truncateBrowserOutput(output, 4000)
}

func safeBrowserDownloadFilename(value string, sessionID string) string {
	value = strings.TrimSpace(filepath.Base(value))
	if value == "" || value == "." || value == string(filepath.Separator) {
		value = "download-" + sessionID + "-" + time.Now().UTC().Format("20060102-150405")
	}
	value = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' || r == 0 {
			return '-'
		}
		return r
	}, value)
	return value
}

func redactBrowserCDPURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" {
		if value == "" {
			return ""
		}
		return "[redacted]"
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func parseBrowserTabs(output string) ([]map[string]any, string, string) {
	var parsed any
	if json.Unmarshal([]byte(output), &parsed) != nil {
		return nil, "", strings.TrimSpace(output)
	}
	candidates := findBrowserTabObjects(parsed)
	tabs := make([]map[string]any, 0, len(candidates))
	activeID := ""
	for _, candidate := range candidates {
		item := map[string]any{}
		for _, key := range []string{"label", "url", "title"} {
			if value, ok := candidate[key].(string); ok && strings.TrimSpace(value) != "" {
				item[key] = value
			}
		}
		for _, key := range []string{"id", "tabId"} {
			if value, ok := candidate[key].(string); ok && strings.TrimSpace(value) != "" {
				item["id"] = value
				break
			}
		}
		if active, ok := candidate["active"].(bool); ok {
			item["active"] = active
			if active {
				if id, _ := item["id"].(string); id != "" {
					activeID = id
				}
			}
		}
		if len(item) > 0 {
			tabs = append(tabs, item)
		}
	}
	return tabs, activeID, ""
}

func findBrowserTabObjects(value any) []map[string]any {
	switch typed := value.(type) {
	case []any:
		var result []map[string]any
		for _, item := range typed {
			result = append(result, findBrowserTabObjects(item)...)
		}
		return result
	case map[string]any:
		if looksLikeBrowserTab(typed) {
			return []map[string]any{typed}
		}
		for _, key := range []string{"data", "tabs", "items", "pages"} {
			if nested, ok := typed[key]; ok {
				if result := findBrowserTabObjects(nested); len(result) > 0 {
					return result
				}
			}
		}
	}
	return nil
}

func looksLikeBrowserTab(value map[string]any) bool {
	_, hasID := value["id"].(string)
	_, hasTabID := value["tabId"].(string)
	if hasID || hasTabID {
		if _, hasURL := value["url"].(string); hasURL {
			return true
		}
		if _, hasTitle := value["title"].(string); hasTitle {
			return true
		}
	}
	return false
}

func summarizeBrowserCookies(output string) map[string]any {
	summary := map[string]any{"available": true, "raw_cookies_exposed": false}
	var parsed any
	if json.Unmarshal([]byte(output), &parsed) != nil {
		summary["parse_error"] = true
		return summary
	}
	cookies := findBrowserCookieObjects(parsed)
	domains := make(map[string]bool)
	sessionCookies := 0
	for _, cookie := range cookies {
		if domain, _ := cookie["domain"].(string); strings.TrimSpace(domain) != "" {
			domains[domain] = true
		}
		if expires, ok := cookie["expires"]; !ok || expires == nil || expires == float64(-1) {
			sessionCookies++
		}
	}
	domainList := make([]string, 0, len(domains))
	for domain := range domains {
		domainList = append(domainList, domain)
	}
	summary["count"] = len(cookies)
	summary["has_cookies"] = len(cookies) > 0
	summary["session_cookie_count"] = sessionCookies
	summary["domains"] = domainList
	return summary
}

func findBrowserCookieObjects(value any) []map[string]any {
	switch typed := value.(type) {
	case []any:
		var result []map[string]any
		for _, item := range typed {
			result = append(result, findBrowserCookieObjects(item)...)
		}
		return result
	case map[string]any:
		if _, hasName := typed["name"].(string); hasName {
			if _, hasValue := typed["value"].(string); hasValue {
				return []map[string]any{typed}
			}
		}
		for _, key := range []string{"cookies", "items"} {
			if nested, ok := typed[key]; ok {
				if result := findBrowserCookieObjects(nested); len(result) > 0 {
					return result
				}
			}
		}
	}
	return nil
}

func redactBrowserJSON(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed)+1)
		result["available"] = true
		for key, item := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "cookie") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "secret") {
				result[key] = "[redacted]"
				continue
			}
			result[key] = redactBrowserJSON(item)
		}
		return result
	case []any:
		result := make([]any, 0, len(typed))
		for _, item := range typed {
			result = append(result, redactBrowserJSON(item))
		}
		return result
	default:
		return typed
	}
}
