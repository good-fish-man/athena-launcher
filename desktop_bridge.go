package main

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	desktopBridgePrefix       = "/__athena/desktop/"
	desktopSearchMaxResults   = 200
	desktopSearchMaxVisits    = 100000
	desktopSearchMaxFileBytes = 2 << 20
)

type desktopBridge struct {
	selectFolder         func() (string, error)
	browser              *browserController
	sessions             sync.Map
	browserMu            sync.Mutex
	activeBrowserSession string
	rootsMu              sync.RWMutex
	roots                []string
	rootsPath            string
}

type desktopSession struct {
	Application string `json:"application"`
	ProcessName string `json:"process_name,omitempty"`
}

type desktopSearchRequest struct {
	Roots         []string `json:"roots"`
	Query         string   `json:"query"`
	Mode          string   `json:"mode"`
	Extensions    []string `json:"extensions"`
	MaxResults    int      `json:"max_results"`
	IncludeHidden bool     `json:"include_hidden"`
}

type desktopFileMatch struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
	Line       int       `json:"line,omitempty"`
	Snippet    string    `json:"snippet,omitempty"`
}

func newDesktopBridge(home string, selectFolder func() (string, error)) *desktopBridge {
	bridge := &desktopBridge{selectFolder: selectFolder, browser: newBrowserController(home), rootsPath: filepath.Join(home, "data", "authorized-roots.json")}
	if data, err := os.ReadFile(bridge.rootsPath); err == nil {
		var roots []string
		if json.Unmarshal(data, &roots) == nil {
			bridge.roots = normalizeDesktopRoots(roots)
		}
	}
	return bridge
}

func (b *desktopBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case desktopBridgePrefix + "status":
		if r.Method != http.MethodGet {
			writeDesktopError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeDesktopJSON(w, http.StatusOK, map[string]any{"available": true, "platform": runtime.GOOS, "architecture": runtime.GOARCH})
	case desktopBridgePrefix + "select-folder":
		if r.Method != http.MethodPost || b.selectFolder == nil {
			writeDesktopError(w, http.StatusMethodNotAllowed, "folder selection is unavailable")
			return
		}
		path, err := b.selectFolder()
		if err != nil {
			writeDesktopError(w, http.StatusBadRequest, err.Error())
			return
		}
		b.authorizeRoot(path)
		writeDesktopJSON(w, http.StatusOK, map[string]string{"path": path})
	default:
		writeDesktopError(w, http.StatusNotFound, "desktop permission endpoint not found")
	}
}

func (b *desktopBridge) authorizeRoot(path string) {
	roots := normalizeDesktopRoots([]string{path})
	if len(roots) == 0 {
		return
	}
	b.rootsMu.Lock()
	defer b.rootsMu.Unlock()
	for _, current := range b.roots {
		if current == roots[0] {
			return
		}
	}
	b.roots = append(b.roots, roots[0])
	if data, err := json.Marshal(b.roots); err == nil {
		_ = os.MkdirAll(filepath.Dir(b.rootsPath), 0o700)
		_ = os.WriteFile(b.rootsPath, data, 0o600)
	}
}

func (b *desktopBridge) authorizedRoots() []string {
	b.rootsMu.RLock()
	defer b.rootsMu.RUnlock()
	return append([]string(nil), b.roots...)
}

func (b *desktopBridge) browserSession(requested string, create bool, forceNew bool, targetKey string) (string, error) {
	b.browserMu.Lock()
	defer b.browserMu.Unlock()
	if b.browser == nil || b.browser.runtime == nil {
		return "", fmt.Errorf("browser runtime is unavailable")
	}
	sessionID, err := b.browser.runtime.resolveSession(requested, create, forceNew, targetKey)
	if err == nil {
		b.activeBrowserSession = sessionID
	}
	return sessionID, err
}

func (b *desktopBridge) clearBrowserSession(sessionID string) {
	b.browserMu.Lock()
	defer b.browserMu.Unlock()
	if b.browser != nil && b.browser.runtime != nil {
		b.browser.runtime.closeSession(sessionID)
	}
	if sessionID == "" || b.activeBrowserSession == sessionID {
		b.activeBrowserSession = ""
	}
}

func stableBrowserSessionID(targetKey string) string {
	key := normalizeBrowserSessionKey(targetKey)
	if key == "" {
		key = "default"
	}
	sum := md5.Sum([]byte(key))
	return "athena-" + hex.EncodeToString(sum[:])
}

func normalizeBrowserWorkspaceKey(value string) string {
	return "default"
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

func writeDesktopError(w http.ResponseWriter, status int, message string) {
	writeDesktopJSON(w, status, map[string]any{"success": false, "error": message})
}

func writeDesktopJSON(w http.ResponseWriter, status int, value any) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func normalizeDesktopRoots(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		absolute, err := filepath.Abs(strings.TrimSpace(value))
		if err != nil || absolute == "" {
			continue
		}
		resolved, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil || !info.IsDir() || seen[resolved] {
			continue
		}
		seen[resolved] = true
		result = append(result, resolved)
	}
	return result
}

func searchDesktopFiles(ctx context.Context, roots []string, request desktopSearchRequest, limit int) ([]desktopFileMatch, bool, error) {
	needle := strings.ToLower(request.Query)
	extensions := map[string]bool{}
	for _, extension := range request.Extensions {
		extension = strings.ToLower(strings.TrimSpace(extension))
		if extension != "" && !strings.HasPrefix(extension, ".") {
			extension = "." + extension
		}
		if extension != "" {
			extensions[extension] = true
		}
	}
	matches := make([]desktopFileMatch, 0, limit)
	visited := 0
	truncated := false
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if path != root && entry.IsDir() && skipDesktopDirectory(entry.Name(), request.IncludeHidden) {
				return filepath.SkipDir
			}
			if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			visited++
			if visited > desktopSearchMaxVisits || len(matches) >= limit {
				truncated = true
				return fs.SkipAll
			}
			if !request.IncludeHidden && strings.HasPrefix(entry.Name(), ".") {
				return nil
			}
			if len(extensions) > 0 && !extensions[strings.ToLower(filepath.Ext(entry.Name()))] {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			match := desktopFileMatch{Name: info.Name(), Path: path, Size: info.Size(), ModifiedAt: info.ModTime()}
			if (request.Mode == "name" || request.Mode == "both") && strings.Contains(strings.ToLower(entry.Name()), needle) {
				matches = append(matches, match)
				return nil
			}
			if request.Mode == "content" || request.Mode == "both" {
				match.Line, match.Snippet = searchDesktopContent(ctx, path, info.Size(), needle)
				if match.Line > 0 {
					matches = append(matches, match)
				}
			}
			return nil
		})
		if err != nil && ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		if truncated || len(matches) >= limit {
			break
		}
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].ModifiedAt.After(matches[j].ModifiedAt) })
	return matches, truncated, nil
}

func skipDesktopDirectory(name string, includeHidden bool) bool {
	if !includeHidden && strings.HasPrefix(name, ".") {
		return true
	}
	switch strings.ToLower(name) {
	case "node_modules", "vendor", "__pycache__", ".git", ".idea", ".vscode":
		return true
	}
	return false
}

func searchDesktopContent(ctx context.Context, path string, size int64, needle string) (int, string) {
	if size <= 0 || size > desktopSearchMaxFileBytes {
		return 0, ""
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, ""
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for line := 1; scanner.Scan(); line++ {
		if ctx.Err() != nil {
			return 0, ""
		}
		text := strings.TrimSpace(scanner.Text())
		if strings.Contains(strings.ToLower(text), needle) {
			if len([]rune(text)) > 300 {
				text = string([]rune(text)[:300])
			}
			return line, text
		}
	}
	return 0, ""
}

func validateDesktopApplication(value string) error {
	if value == "" || len([]rune(value)) > 100 || strings.HasPrefix(value, "-") {
		return fmt.Errorf("invalid application name")
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) || unicode.IsSpace(character) || strings.ContainsRune("._+-()", character) {
			continue
		}
		return fmt.Errorf("application must be a name without paths, URLs, arguments, or shell syntax")
	}
	return nil
}

func launchDesktopApplication(ctx context.Context, application string) error {
	if err := launchDesktopApplicationName(ctx, application); err != nil {
		return fmt.Errorf("could not open %s: %w", application, err)
	}
	return nil
}

func launchDesktopApplicationName(ctx context.Context, application string) error {
	switch runtime.GOOS {
	case "darwin":
		return runDesktopLauncher(exec.CommandContext(ctx, "/usr/bin/open", "-a", application))
	case "windows":
		return runDesktopLauncher(exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", "Start-Process -FilePath $args[0]", application))
	case "linux":
		if launcher, err := exec.LookPath("gtk-launch"); err == nil {
			return runDesktopLauncher(exec.CommandContext(ctx, launcher, application))
		}
		if executable, err := exec.LookPath(application); err == nil {
			command := exec.CommandContext(ctx, executable)
			if err := command.Start(); err != nil {
				return err
			}
			return command.Process.Release()
		}
		return fmt.Errorf("no supported application launcher found")
	default:
		return fmt.Errorf("opening applications is unsupported on %s", runtime.GOOS)
	}
}

func runDesktopLauncher(command *exec.Cmd) error {
	output, err := command.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message != "" {
			return fmt.Errorf("%w: %s", err, message)
		}
		return err
	}
	return nil
}

func activeDesktopApplication(ctx context.Context) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		output, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `tell application "System Events" to get name of first application process whose frontmost is true`).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("inspect active application: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return strings.TrimSpace(string(output)), nil
	case "windows":
		return "", nil
	case "linux":
		launcher, err := exec.LookPath("xdotool")
		if err != nil {
			return "", err
		}
		output, err := exec.CommandContext(ctx, launcher, "getactivewindow", "getwindowname").CombinedOutput()
		return strings.TrimSpace(string(output)), err
	default:
		return "", fmt.Errorf("desktop observation is unsupported on %s", runtime.GOOS)
	}
}

func runDesktopSessionAction(ctx context.Context, session desktopSession, action, value string) (map[string]any, error) {
	application := session.ProcessName
	if application == "" {
		application = session.Application
	}
	switch action {
	case "activate":
		if err := activateDesktopApplication(ctx, application, session.Application); err != nil {
			return nil, err
		}
		return map[string]any{"application": session.Application, "active": true}, nil
	case "observe":
		if err := activateDesktopApplication(ctx, application, session.Application); err != nil {
			return nil, err
		}
		title, err := activeDesktopWindowTitle(ctx, application)
		if err != nil {
			return nil, err
		}
		return map[string]any{"application": session.Application, "process_name": application, "window_title": title}, nil
	case "press":
		if err := sendDesktopKey(ctx, application, value); err != nil {
			return nil, err
		}
		return map[string]any{"application": session.Application, "key": value}, nil
	case "type_text":
		if len([]rune(value)) > 4000 {
			return nil, fmt.Errorf("text is too long")
		}
		if err := typeDesktopText(ctx, application, value); err != nil {
			return nil, err
		}
		return map[string]any{"application": session.Application, "characters": len([]rune(value))}, nil
	case "close_application":
		if err := closeDesktopApplication(ctx, application); err != nil {
			return nil, err
		}
		return map[string]any{"application": session.Application, "closed": true}, nil
	default:
		return nil, fmt.Errorf("unsupported desktop session action %q", action)
	}
}

func activateDesktopApplication(ctx context.Context, processName, applicationName string) error {
	switch runtime.GOOS {
	case "darwin":
		return runDesktopLauncher(exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `on run argv`, "-e", `tell application "System Events" to tell process (item 1 of argv) to set frontmost to true`, "-e", `end run`, processName))
	case "windows":
		return runDesktopLauncher(exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `(New-Object -ComObject WScript.Shell).AppActivate($args[0]) | Out-Null`, processName))
	case "linux":
		launcher, err := exec.LookPath("xdotool")
		if err != nil {
			return err
		}
		return runDesktopLauncher(exec.CommandContext(ctx, launcher, "search", "--name", applicationName, "windowactivate", "--sync"))
	default:
		return fmt.Errorf("desktop control is unsupported on %s", runtime.GOOS)
	}
}

func activeDesktopWindowTitle(ctx context.Context, processName string) (string, error) {
	switch runtime.GOOS {
	case "darwin":
		output, err := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `on run argv`, "-e", `tell application "System Events" to tell process (item 1 of argv) to get name of front window`, "-e", `end run`, processName).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("observe application window: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return strings.TrimSpace(string(output)), nil
	case "windows":
		return processName, nil
	case "linux":
		launcher, err := exec.LookPath("xdotool")
		if err != nil {
			return "", err
		}
		output, err := exec.CommandContext(ctx, launcher, "getactivewindow", "getwindowname").CombinedOutput()
		return strings.TrimSpace(string(output)), err
	default:
		return "", fmt.Errorf("desktop observation is unsupported on %s", runtime.GOOS)
	}
}

func sendDesktopKey(ctx context.Context, processName, key string) error {
	allowed := map[string]string{"Enter": "36", "Escape": "53", "Tab": "48", "Space": "49", "ArrowUp": "126", "ArrowDown": "125", "ArrowLeft": "123", "ArrowRight": "124"}
	code, ok := allowed[key]
	if !ok {
		return fmt.Errorf("key is not allowed")
	}
	switch runtime.GOOS {
	case "darwin":
		return runDesktopLauncher(exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `on run argv`, "-e", `tell application "System Events" to tell process (item 1 of argv) to key code `+code, "-e", `end run`, processName))
	case "windows":
		keys := map[string]string{"Enter": "{ENTER}", "Escape": "{ESC}", "Tab": "{TAB}", "Space": " ", "ArrowUp": "{UP}", "ArrowDown": "{DOWN}", "ArrowLeft": "{LEFT}", "ArrowRight": "{RIGHT}"}
		return runDesktopLauncher(exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `$w=New-Object -ComObject WScript.Shell; $w.AppActivate($args[0])|Out-Null; $w.SendKeys($args[1])`, processName, keys[key]))
	case "linux":
		launcher, err := exec.LookPath("xdotool")
		if err != nil {
			return err
		}
		return runDesktopLauncher(exec.CommandContext(ctx, launcher, "key", key))
	default:
		return fmt.Errorf("desktop key input is unsupported on %s", runtime.GOOS)
	}
}

func typeDesktopText(ctx context.Context, processName, value string) error {
	switch runtime.GOOS {
	case "darwin":
		return runDesktopLauncher(exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `on run argv`, "-e", `tell application "System Events" to tell process (item 1 of argv) to keystroke (item 2 of argv)`, "-e", `end run`, processName, value))
	case "windows":
		return runDesktopLauncher(exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `$w=New-Object -ComObject WScript.Shell; $w.AppActivate($args[0])|Out-Null; $w.SendKeys($args[1])`, processName, value))
	case "linux":
		launcher, err := exec.LookPath("xdotool")
		if err != nil {
			return err
		}
		return runDesktopLauncher(exec.CommandContext(ctx, launcher, "type", "--clearmodifiers", "--", value))
	default:
		return fmt.Errorf("desktop text input is unsupported on %s", runtime.GOOS)
	}
}

func closeDesktopApplication(ctx context.Context, processName string) error {
	switch runtime.GOOS {
	case "darwin":
		return runDesktopLauncher(exec.CommandContext(ctx, "/usr/bin/osascript", "-e", `on run argv`, "-e", `tell application "System Events" to tell process (item 1 of argv) to keystroke "q" using command down`, "-e", `end run`, processName))
	case "windows":
		return runDesktopLauncher(exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", `$w=New-Object -ComObject WScript.Shell; $w.AppActivate($args[0])|Out-Null; $w.SendKeys('%{F4}')`, processName))
	case "linux":
		launcher, err := exec.LookPath("xdotool")
		if err != nil {
			return err
		}
		return runDesktopLauncher(exec.CommandContext(ctx, launcher, "key", "alt+F4"))
	default:
		return fmt.Errorf("closing applications is unsupported on %s", runtime.GOOS)
	}
}
