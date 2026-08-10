package browser

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"regexp"
	"strings"
)

var browserSessionPattern = regexp.MustCompile(`^athena-[a-f0-9]{32}$`)

func stableBrowserSessionID(targetKey string) string {
	key := normalizeBrowserSessionKey(targetKey)
	if key == "" {
		key = "default"
	}
	sum := md5.Sum([]byte(key))
	return "athena-" + hex.EncodeToString(sum[:])
}

func normalizeBrowserWorkspaceKey(string) string { return "default" }

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

func browserSessionTargetKey(arguments map[string]any) string {
	for _, key := range []string{"target", "url", "query", "goal", "engine"} {
		if value, ok := arguments[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func newDeviceBrowserSessionID() string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "athena-" + strings.Repeat("0", 32)
	}
	return "athena-" + hex.EncodeToString(value)
}
