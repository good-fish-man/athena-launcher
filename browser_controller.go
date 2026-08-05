package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

var (
	browserSessionPattern = regexp.MustCompile(`^athena-[a-f0-9]{32}$`)
	browserRefPattern     = regexp.MustCompile(`^@e[0-9]+$`)
	browserElementLineRef = regexp.MustCompile(`(@e[0-9]+|\[ref=([eE][0-9]+)\])`)
)

const (
	browserAuthModeIsolated    = "isolated"
	browserAuthModeProfile     = "profile"
	browserAuthModeAutoConnect = "auto_connect"
)

type browserController struct {
	home       string
	runtime    *browserRuntime
	perception *perceptionLayer
}

type browserExecuteRequest struct {
	RequestID    string                      `json:"request_id"`
	SessionID    string                      `json:"session_id"`
	Action       string                      `json:"action"`
	Arguments    map[string]any              `json:"arguments"`
	RiskLevel    string                      `json:"risk_level"`
	Decision     string                      `json:"decision"`
	UserTakeover bool                        `json:"user_takeover"`
	Approved     bool                        `json:"approved"`
	Progress     func(browserActionProgress) `json:"-"`
}

type browserActionProgress struct {
	Stage    string         `json:"stage,omitempty"`
	Message  string         `json:"message,omitempty"`
	Progress int            `json:"progress,omitempty"`
	Bytes    int64          `json:"bytes,omitempty"`
	Total    int64          `json:"total,omitempty"`
	State    map[string]any `json:"state,omitempty"`
}

func newBrowserController(home string) *browserController {
	runtime := newBrowserRuntime(home)
	return &browserController{home: home, runtime: runtime, perception: newPerceptionLayer(home, runtime)}
}

func (b *browserController) runAction(ctx context.Context, request browserExecuteRequest) (map[string]any, error) {
	executable, err := b.executable()
	if err != nil {
		if result, handled, fallbackErr := b.runSystemBrowserFallback(ctx, request); handled {
			if fallbackErr != nil {
				return nil, fallbackErr
			}
			return result, nil
		}
		return nil, err
	}
	value := func(key string) string {
		text, _ := request.Arguments[key].(string)
		return strings.TrimSpace(text)
	}
	run := func(timeout time.Duration, args ...string) (string, error) {
		commandCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		command := exec.CommandContext(commandCtx, executable, args...)
		output, commandErr := command.CombinedOutput()
		if commandCtx.Err() != nil {
			return "", fmt.Errorf("browser action timed out: %w", commandCtx.Err())
		}
		if commandErr != nil {
			detail := strings.TrimSpace(string(output))
			if len(detail) > 4000 {
				detail = detail[len(detail)-4000:]
			}
			return "", fmt.Errorf("agent-browser: %w: %s", commandErr, detail)
		}
		return strings.TrimSpace(string(output)), nil
	}
	sessionArgs := b.sessionArgs(request.SessionID)
	switch request.Action {
	case "navigate":
		target, err := validateBrowserTarget(value("url"))
		if err != nil {
			return nil, err
		}
		if browserOpenMode(request.Arguments) == "tab" && b.runtime != nil && b.runtime.hasSessionContent(request.SessionID) {
			label := browserTabLabel(request.Arguments, target)
			if label == "" {
				label = "page"
			}
			if _, err := run(30*time.Second, append(sessionArgs, "tab", label)...); err != nil {
				args := append(append([]string{}, sessionArgs...), "tab", "new", "--label", label, target)
				if _, err := run(30*time.Second, args...); err != nil {
					return nil, err
				}
			} else if _, err := run(30*time.Second, append(sessionArgs, "open", target)...); err != nil {
				return nil, err
			}
			request.Arguments["tab_label"] = label
		} else {
			args := append(append([]string{}, sessionArgs...), "open", target)
			headed, _ := request.Arguments["headed"].(bool)
			if request.UserTakeover || headed {
				args = append(args, "--headed")
			}
			if _, err := run(30*time.Second, args...); err != nil {
				return nil, err
			}
		}
	case "click":
		ref := value("ref")
		if !browserRefPattern.MatchString(ref) {
			return nil, fmt.Errorf("%s requires a snapshot ref such as @e12", request.Action)
		}
		if _, err := run(20*time.Second, append(sessionArgs, "click", ref)...); err != nil {
			return nil, err
		}
	case "download":
		ref := value("ref")
		if !browserRefPattern.MatchString(ref) {
			return nil, fmt.Errorf("download requires a snapshot ref such as @e12")
		}
		var path string
		if b.runtime != nil {
			var err error
			request, path, err = b.runtime.prepareDownload(request)
			if err != nil {
				return nil, err
			}
		}
		if path == "" {
			path = value("path")
		}
		if err := b.runDownload(ctx, executable, sessionArgs, ref, path, request); err != nil {
			return nil, err
		}
	case "type":
		ref := value("ref")
		if !browserRefPattern.MatchString(ref) || value("value") == "" {
			return nil, fmt.Errorf("type requires a snapshot ref and non-empty value")
		}
		if _, err := run(20*time.Second, append(sessionArgs, "fill", ref, value("value"))...); err != nil {
			return nil, err
		}
	case "press":
		if _, err := run(20*time.Second, append(sessionArgs, "press", value("value"))...); err != nil {
			return nil, err
		}
	case "scroll":
		direction := value("value")
		if direction != "up" && direction != "down" {
			return nil, fmt.Errorf("scroll direction must be up or down")
		}
		if _, err := run(20*time.Second, append(sessionArgs, "scroll", direction)...); err != nil {
			return nil, err
		}
	case "wait":
		milliseconds, _ := strconv.Atoi(value("value"))
		if milliseconds < 0 || milliseconds > 30000 {
			return nil, fmt.Errorf("wait must be between 0 and 30000 milliseconds")
		}
		if _, err := run(35*time.Second, append(sessionArgs, "wait", strconv.Itoa(milliseconds))...); err != nil {
			return nil, err
		}
	case "extract":
		if target := value("url"); target != "" {
			validated, err := validateBrowserTarget(target)
			if err != nil {
				return nil, err
			}
			if _, err := run(30*time.Second, append(sessionArgs, "open", validated)...); err != nil {
				return nil, err
			}
		}
	case "screenshot":
		directory := filepath.Join(b.home, "browser", "screenshots")
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
		path := filepath.Join(directory, request.SessionID+"-"+time.Now().Format("20060102-150405")+".png")
		if _, err := run(30*time.Second, append(sessionArgs, "screenshot", path)...); err != nil {
			return nil, err
		}
		result := map[string]any{"screenshot_path": path, "screenshot": map[string]any{"available": true, "path": path}}
		result = b.observeBrowser(request, result, run, sessionArgs)
		return result, nil
	case "upload":
		return nil, fmt.Errorf("upload requires the native file picker and user takeover")
	case "close":
		_, err := run(10*time.Second, append(sessionArgs, "close")...)
		return map[string]any{"closed": err == nil}, err
	default:
		return nil, fmt.Errorf("unsupported browser action %q", request.Action)
	}
	observation, err := browserObservation(run, sessionArgs)
	if err != nil {
		return nil, err
	}
	if challenge := detectBrowserChallenge(observation); challenge != nil {
		observation["challenge_detected"] = true
		observation["challenge"] = challenge
	}
	if screenshot := b.captureBrowserScreenshot(request, run, sessionArgs); screenshot != nil {
		observation["screenshot"] = screenshot
	}
	observation = b.observeBrowser(request, observation, run, sessionArgs)
	return observation, nil
}

func (b *browserController) observeBrowser(request browserExecuteRequest, observation map[string]any, run browserCommandRunner, sessionArgs []string) map[string]any {
	if b == nil || b.runtime == nil {
		return observation
	}
	perception := b.perception
	if perception == nil {
		perception = newPerceptionLayer(b.home, b.runtime)
	}
	return perception.ObserveBrowser(request, observation, run, sessionArgs)
}

func (b *browserController) captureBrowserScreenshot(request browserExecuteRequest, run browserCommandRunner, sessionArgs []string) map[string]any {
	if b == nil || b.runtime == nil {
		return nil
	}
	perception := b.perception
	if perception == nil {
		perception = newPerceptionLayer(b.home, b.runtime)
	}
	return perception.CaptureBrowserScreenshot(request, run, sessionArgs)
}

func (b *browserController) sessionArgs(sessionID string) []string {
	if b.runtime != nil {
		return b.runtime.sessionArgs(sessionID)
	}
	return newBrowserRuntime(b.home).sessionArgs(sessionID)
}

func (b *browserController) runDownload(ctx context.Context, executable string, sessionArgs []string, ref, path string, request browserExecuteRequest) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("download path is required")
	}
	started := time.Now()
	request.emitProgress(browserActionProgress{
		Stage: "starting", Progress: 3, Message: "Preparing browser download",
		State: map[string]any{"ref": ref, "path": path},
	})
	commandCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	done := make(chan struct{})
	var lastBytes int64 = -1
	go func() {
		ticker := time.NewTicker(750 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-commandCtx.Done():
				return
			case <-ticker.C:
				snapshot := browserDownloadFileSnapshot(path, started)
				bytes, _ := snapshot["bytes"].(int64)
				if bytes <= 0 || bytes == lastBytes {
					continue
				}
				lastBytes = bytes
				request.emitProgress(browserActionProgress{
					Stage: "downloading", Progress: browserDownloadProgress(bytes, int64Argument(request.Arguments["expected_bytes"]), 15, 85),
					Message: fmt.Sprintf("Downloading file: %s", humanBytes(bytes)),
					Bytes:   bytes, Total: int64Argument(request.Arguments["expected_bytes"]), State: snapshot,
				})
			}
		}
	}()
	args := append(append([]string{}, sessionArgs...), "download", ref, path)
	command := exec.CommandContext(commandCtx, executable, args...)
	output, commandErr := command.CombinedOutput()
	close(done)
	if commandCtx.Err() != nil {
		request.emitProgress(browserActionProgress{Stage: "failed", Progress: 100, Message: "Download timed out", State: map[string]any{"path": path}})
		return fmt.Errorf("browser download timed out: %w", commandCtx.Err())
	}
	if commandErr != nil {
		detail := strings.TrimSpace(string(output))
		if len(detail) > 4000 {
			detail = detail[len(detail)-4000:]
		}
		request.emitProgress(browserActionProgress{Stage: "failed", Progress: 100, Message: "Download failed", State: map[string]any{"path": path, "error": detail}})
		return fmt.Errorf("agent-browser download: %w: %s", commandErr, detail)
	}
	snapshot := browserDownloadFileSnapshot(path, started)
	bytes, _ := snapshot["bytes"].(int64)
	request.emitProgress(browserActionProgress{
		Stage: "complete", Progress: 100, Message: "Download completed",
		Bytes: bytes, Total: int64Argument(request.Arguments["expected_bytes"]), State: snapshot,
	})
	return nil
}

func browserOpenMode(arguments map[string]any) string {
	value, _ := arguments["open_mode"].(string)
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "tab", "new_tab":
		return "tab"
	default:
		return ""
	}
}

func browserTabLabel(arguments map[string]any, fallback string) string {
	for _, key := range []string{"tab_label", "target", "url"} {
		if value, _ := arguments[key].(string); strings.TrimSpace(value) != "" {
			if label := safeBrowserTabLabel(value); label != "" {
				return label
			}
		}
	}
	return safeBrowserTabLabel(fallback)
}

func safeBrowserTabLabel(value string) string {
	value = normalizeBrowserSessionKey(value)
	if value == "" {
		return ""
	}
	var builder strings.Builder
	lastDash := false
	for _, char := range value {
		switch {
		case char >= 'a' && char <= 'z':
			builder.WriteRune(char)
			lastDash = false
		case char >= '0' && char <= '9':
			builder.WriteRune(char)
			lastDash = false
		default:
			if !lastDash && builder.Len() > 0 {
				builder.WriteByte('-')
				lastDash = true
			}
		}
		if builder.Len() >= 40 {
			break
		}
	}
	return strings.Trim(builder.String(), "-")
}

func (request browserExecuteRequest) emitProgress(progress browserActionProgress) {
	if request.Progress == nil {
		return
	}
	if progress.Progress < 0 {
		progress.Progress = 0
	}
	if progress.Progress > 100 {
		progress.Progress = 100
	}
	request.Progress(progress)
}

func browserDownloadFileSnapshot(path string, started time.Time) map[string]any {
	path = strings.TrimSpace(path)
	snapshot := map[string]any{"path": path, "completed": false}
	if path == "" {
		return snapshot
	}
	candidates := []string{path, path + ".crdownload", path + ".part", path + ".download"}
	if entries, err := os.ReadDir(filepath.Dir(path)); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasPrefix(name, filepath.Base(path)) && (strings.HasSuffix(name, ".crdownload") || strings.HasSuffix(name, ".part") || strings.HasSuffix(name, ".download")) {
				candidates = append(candidates, filepath.Join(filepath.Dir(path), name))
			}
		}
	}
	var newest os.FileInfo
	var newestPath string
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if newest == nil || info.ModTime().After(newest.ModTime()) || info.Size() > newest.Size() {
			newest = info
			newestPath = candidate
		}
	}
	if newest == nil {
		return snapshot
	}
	snapshot["path"] = newestPath
	snapshot["filename"] = filepath.Base(newestPath)
	snapshot["bytes"] = newest.Size()
	snapshot["modified_at"] = newest.ModTime().UTC().Format(time.RFC3339Nano)
	snapshot["elapsed_ms"] = time.Since(started).Milliseconds()
	snapshot["completed"] = newestPath == path
	return snapshot
}

func browserDownloadProgress(bytes, total int64, minPercent, maxPercent int) int {
	if total <= 0 || bytes <= 0 {
		return minPercent
	}
	span := maxPercent - minPercent
	if span <= 0 {
		return minPercent
	}
	progress := minPercent + int(bytes*int64(span)/total)
	if progress < minPercent {
		return minPercent
	}
	if progress > maxPercent {
		return maxPercent
	}
	return progress
}

func humanBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%d B", value)
	}
	size := float64(value)
	for _, suffix := range []string{"KB", "MB", "GB", "TB"} {
		size /= unit
		if size < unit {
			return fmt.Sprintf("%.1f %s", size, suffix)
		}
	}
	return fmt.Sprintf("%.1f PB", size/unit)
}

func int64Argument(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		result, _ := typed.Int64()
		return result
	case string:
		result, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return result
	default:
		return 0
	}
}

func browserSessionArgs(sessionID string) []string {
	return (&browserController{}).sessionArgs(sessionID)
}

func browserAuthMode(home string) string {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("ATHENA_BROWSER_AUTH_MODE")))
	if mode == "" && home != "" {
		if state, err := loadState(home); err == nil && state != nil {
			mode = strings.ToLower(strings.TrimSpace(state.BrowserAuthMode))
		}
	}
	return normalizeBrowserAuthMode(mode)
}

func normalizeBrowserAuthMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case browserAuthModeProfile:
		return browserAuthModeProfile
	case browserAuthModeAutoConnect:
		return browserAuthModeAutoConnect
	default:
		return browserAuthModeIsolated
	}
}

func browserProfile(home string) string {
	for _, key := range []string{"ATHENA_BROWSER_PROFILE", "AGENT_BROWSER_PROFILE"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	if home != "" {
		if state, err := loadState(home); err == nil && state != nil && strings.TrimSpace(state.BrowserProfile) != "" {
			if normalizeBrowserAuthMode(state.BrowserAuthMode) == browserAuthModeProfile {
				return strings.TrimSpace(state.BrowserProfile)
			}
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "browser", "profiles", "default")
}

func preferredBrowserExecutable() string {
	for _, key := range []string{"ATHENA_BROWSER_EXECUTABLE_PATH", "AGENT_BROWSER_EXECUTABLE_PATH"} {
		if candidate := strings.TrimSpace(os.Getenv(key)); executableFileExists(candidate) {
			return candidate
		}
	}
	for _, candidate := range defaultBrowserExecutableCandidates() {
		if executableFileExists(candidate) {
			return candidate
		}
	}
	return ""
}

func defaultBrowserExecutableCandidates() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			filepath.Join(os.Getenv("HOME"), "Applications/Google Chrome.app/Contents/MacOS/Google Chrome"),
		}
	case "windows":
		return []string{
			filepath.Join(os.Getenv("PROGRAMFILES"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("PROGRAMFILES(X86)"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "Application", "chrome.exe"),
		}
	default:
		return []string{"/usr/bin/google-chrome", "/usr/bin/google-chrome-stable", "/snap/bin/chromium"}
	}
}

func executableFileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (b *browserController) capabilities() []string {
	if b.available() {
		return []string{
			"browser.open", "browser.navigate", "browser.observe", "browser.click", "browser.type", "browser.press",
			"browser.scroll", "browser.wait", "browser.download", "browser.screenshot", "browser.close",
		}
	}
	return []string{"browser.open", "browser.navigate"}
}

func (b *browserController) available() bool {
	_, err := b.executable()
	return err == nil
}

func (b *browserController) runSystemBrowserFallback(ctx context.Context, request browserExecuteRequest) (map[string]any, bool, error) {
	if request.Action != "navigate" {
		return nil, false, nil
	}
	raw, _ := request.Arguments["url"].(string)
	target, err := validateBrowserTarget(raw)
	if err != nil {
		return nil, true, err
	}
	if err := openSystemURL(ctx, target); err != nil {
		return nil, true, fmt.Errorf("agent-browser is not installed and system browser fallback failed: %w", err)
	}
	message := "Opened with the system default browser because agent-browser is not installed. Install or update the Agent Browser package for semantic click/type/observe control."
	result := map[string]any{
		"url":                     target,
		"title":                   "",
		"content":                 "",
		"snapshot":                message,
		"controller":              "system_browser_fallback",
		"agent_browser_installed": false,
		"message":                 message,
		"page":                    map[string]any{"url": target, "title": ""},
		"key_elements":            []map[string]string{},
	}
	if b.runtime != nil {
		if request.Arguments == nil {
			request.Arguments = make(map[string]any)
		}
		request.Arguments["url"] = target
		result = b.observeBrowser(request, result, nil, nil)
	}
	return result, true, nil
}

func browserObservation(run func(time.Duration, ...string) (string, error), sessionArgs []string) (map[string]any, error) {
	currentURL, _ := run(10*time.Second, append(sessionArgs, "get", "url")...)
	title, _ := run(10*time.Second, append(sessionArgs, "get", "title")...)
	content, _ := run(15*time.Second, append(sessionArgs, "get", "text", "body")...)
	snapshot, err := run(15*time.Second, append(sessionArgs, "snapshot", "-i", "--urls", "-c", "-d", "6")...)
	if err != nil {
		return nil, err
	}
	content = truncateBrowserOutput(content, 120000)
	snapshot = truncateBrowserOutput(snapshot, 80000)
	return map[string]any{
		"url": currentURL, "title": title, "content": content, "snapshot": snapshot,
		"page":         map[string]any{"url": currentURL, "title": title},
		"key_elements": browserKeyElements(snapshot, 80),
	}, nil
}

func detectBrowserChallenge(state map[string]any) map[string]any {
	if state == nil {
		return nil
	}
	currentURL := lowerStateString(state, "url")
	title := lowerStateString(state, "title")
	content := lowerStateString(state, "content")
	snapshot := lowerStateString(state, "snapshot")
	combined := strings.Join([]string{currentURL, title, content, snapshot}, "\n")
	for _, signal := range []struct {
		kind    string
		message string
		needles []string
	}{
		{
			kind:    "google_unusual_traffic",
			message: "Google blocked the automated browser with an unusual-traffic verification page. User verification or a non-Google route is required.",
			needles: []string{
				"google.com/sorry", "our systems have detected unusual traffic", "unusual traffic from your computer network",
				"to continue, please type the characters",
			},
		},
		{
			kind:    "captcha",
			message: "The page is asking for CAPTCHA or human verification. User takeover is required.",
			needles: []string{"captcha", "verify you are human", "i am not a robot", "prove you are human"},
		},
		{
			kind:    "cloudflare_challenge",
			message: "The page is behind a Cloudflare or browser integrity challenge. User takeover may be required.",
			needles: []string{"checking if the site connection is secure", "cf-challenge", "cloudflare ray id", "attention required"},
		},
		{
			kind:    "access_denied",
			message: "The page returned an access-denied or bot-block page instead of the expected content.",
			needles: []string{"access denied", "403 forbidden", "request blocked", "blocked due to suspicious activity"},
		},
	} {
		for _, needle := range signal.needles {
			if strings.Contains(combined, needle) {
				return map[string]any{
					"kind":                   signal.kind,
					"message":                signal.message,
					"current_url":            state["url"],
					"title":                  state["title"],
					"requires_user_takeover": true,
				}
			}
		}
	}
	return nil
}

func lowerStateString(state map[string]any, key string) string {
	value, _ := state[key].(string)
	return strings.ToLower(value)
}

func browserKeyElements(snapshot string, limit int) []map[string]string {
	if limit <= 0 {
		limit = 80
	}
	elements := make([]map[string]string, 0, limit)
	for _, rawLine := range strings.Split(snapshot, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		match := browserElementLineRef.FindStringSubmatch(line)
		if len(match) == 0 {
			continue
		}
		ref := match[1]
		if strings.HasPrefix(ref, "[ref=") && len(match) > 2 {
			ref = "@" + strings.ToLower(match[2])
		}
		if !browserRefPattern.MatchString(ref) {
			continue
		}
		label := strings.TrimSpace(browserElementLineRef.ReplaceAllString(line, ""))
		label = strings.Trim(label, "- \t")
		if len(label) > 240 {
			label = label[:240] + "..."
		}
		elements = append(elements, map[string]string{"ref": ref, "label": label})
		if len(elements) >= limit {
			break
		}
	}
	return elements
}

func (b *browserController) executable() (string, error) {
	if candidate := strings.TrimSpace(os.Getenv("ATHENA_AGENT_BROWSER_BIN")); candidate != "" {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	state, _ := loadState(b.home)
	if state != nil {
		if version := strings.TrimSpace(state.Installed["agent-browser"]); version != "" {
			name := "agent-browser"
			if runtime.GOOS == "windows" {
				name += ".exe"
			}
			candidate := filepath.Join(b.home, "browser", version, name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate, nil
			}
		}
	}
	entries, _ := filepath.Glob(filepath.Join(b.home, "browser", "*", "agent-browser*"))
	sort.Sort(sort.Reverse(sort.StringSlice(entries)))
	for _, candidate := range entries {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if candidate, err := exec.LookPath("agent-browser"); err == nil {
		return candidate, nil
	}
	return "", fmt.Errorf("agent-browser is not installed on this device")
}

func openSystemURL(ctx context.Context, address string) error {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		command = exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", address)
	case "darwin":
		command = exec.CommandContext(ctx, "open", address)
	default:
		command = exec.CommandContext(ctx, "xdg-open", address)
	}
	if err := command.Start(); err != nil {
		return fmt.Errorf("open URL in default browser: %w", err)
	}
	return nil
}

func validateBrowserTarget(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", fmt.Errorf("browser URL must be an absolute HTTP(S) URL without credentials")
	}
	return parsed.String(), nil
}

func truncateBrowserOutput(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n[truncated]"
}
