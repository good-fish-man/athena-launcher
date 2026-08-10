package browser

import (
	"os"
	"path/filepath"
	"time"
)

func (m *browserDownloadManager) directory() string {
	if m.runtime.home == "" {
		return ""
	}
	return filepath.Join(m.runtime.home, "browser", "downloads")
}

func (m *browserDownloadManager) enrichLocked(request Request, observation map[string]any) {
	if request.Action != "download" {
		return
	}
	observation["download"] = m.observationLocked(request, observation)
}

func (m *browserDownloadManager) probe(request Request, observation map[string]any) {
	if request.Action != "download" {
		return
	}
	path, _ := request.Arguments["path"].(string)
	info := map[string]any{"requested": true, "path": path, "directory": m.directory(), "completed": false}
	if stat, err := os.Stat(path); err == nil {
		info["completed"] = true
		info["bytes"] = stat.Size()
		info["modified_at"] = stat.ModTime().UTC().Format(time.RFC3339Nano)
		info["filename"] = filepath.Base(path)
	} else if path != "" {
		info["error"] = err.Error()
	}
	observation["download"] = info
}

func (m *browserDownloadManager) observationLocked(request Request, observation map[string]any) map[string]any {
	if existing, ok := observation["download"].(map[string]any); ok {
		if existing["directory"] == nil {
			existing["directory"] = m.directory()
		}
		existing["requires_user_approval"] = request.Action == "download"
		return existing
	}
	return map[string]any{
		"requested":              request.Action == "download",
		"directory":              m.directory(),
		"requires_user_approval": request.Action == "download",
	}
}

func (m *browserCookieManager) probe(observation map[string]any, run CommandRunner, sessionArgs []string) {
	output, err := run(10*time.Second, append(sessionArgs, "cookies", "get", "--json")...)
	if err != nil {
		observation["cookie_status"] = map[string]any{"available": false, "raw_cookies_exposed": false, "error": err.Error()}
		return
	}
	summary := summarizeBrowserCookies(output)
	observation["cookie_status"] = summary
}

func (m *browserCookieManager) observationLocked(session *browserSessionInfo) map[string]any {
	if session == nil {
		return map[string]any{"raw_cookies_exposed": false}
	}
	return map[string]any{
		"mode":                session.Profile.Mode,
		"raw_cookies_exposed": false,
		"summary":             "cookies stay inside the selected browser profile",
	}
}
