package browser_runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const authenticatedChromePort = 9222

var authenticatedChromeStartMu sync.Mutex

type AuthenticatedChromeResult struct {
	State     string `json:"state"`
	Message   string `json:"message"`
	Profile   string `json:"profile"`
	DebugURL  string `json:"debugUrl"`
	LogPath   string `json:"logPath"`
	ProcessID int    `json:"processId,omitempty"`
}

func StartAuthenticatedChrome(ctx context.Context, home string) (AuthenticatedChromeResult, error) {
	authenticatedChromeStartMu.Lock()
	defer authenticatedChromeStartMu.Unlock()

	result := authenticatedChromeResult(home)
	if browserCDPReady(ctx, result.DebugURL) {
		result.State = "running"
		result.Message = "Authenticated Chrome is already running"
		return result, nil
	}
	if !localPortAvailable(authenticatedChromePort) {
		return result, fmt.Errorf("browser authentication port %d is occupied by a non-CDP process", authenticatedChromePort)
	}
	executable := preferredBrowserExecutable()
	if executable == "" {
		return result, fmt.Errorf("Google Chrome is not installed or could not be found")
	}
	if err := os.MkdirAll(result.Profile, 0o700); err != nil {
		return result, fmt.Errorf("create authenticated Chrome profile: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(result.LogPath), 0o700); err != nil {
		return result, fmt.Errorf("create browser log directory: %w", err)
	}
	logFile, err := os.OpenFile(result.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return result, fmt.Errorf("open authenticated Chrome log: %w", err)
	}
	command := exec.Command(executable, authenticatedChromeArgs(result.Profile)...)
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return result, fmt.Errorf("start authenticated Chrome: %w", err)
	}
	result.ProcessID = command.Process.Pid
	exited := make(chan error, 1)
	go func() {
		exited <- command.Wait()
		_ = logFile.Close()
	}()

	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = command.Process.Kill()
			return result, fmt.Errorf("wait for authenticated Chrome: %w", ctx.Err())
		case err := <-exited:
			if browserCDPReady(context.Background(), result.DebugURL) {
				result.State = "running"
				result.Message = "Authenticated Chrome is ready"
				return result, nil
			}
			if err == nil {
				err = fmt.Errorf("process exited before CDP became ready")
			}
			return result, fmt.Errorf("authenticated Chrome exited: %w (log: %s)", err, result.LogPath)
		case <-ticker.C:
			if browserCDPReady(ctx, result.DebugURL) {
				result.State = "running"
				result.Message = "Authenticated Chrome is ready"
				return result, nil
			}
		case <-deadline.C:
			_ = command.Process.Kill()
			return result, fmt.Errorf("authenticated Chrome did not expose CDP within 15 seconds (log: %s)", result.LogPath)
		}
	}
}

func authenticatedChromeResult(home string) AuthenticatedChromeResult {
	return AuthenticatedChromeResult{
		State:    "starting",
		Message:  "Starting authenticated Chrome",
		Profile:  filepath.Join(home, "browser", "authenticated-profile"),
		DebugURL: fmt.Sprintf("http://127.0.0.1:%d", authenticatedChromePort),
		LogPath:  filepath.Join(home, "logs", "browser-auth.log"),
	}
}

func authenticatedChromeArgs(profile string) []string {
	return []string{
		fmt.Sprintf("--remote-debugging-port=%d", authenticatedChromePort),
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=" + profile,
		"--no-default-browser-check",
		"--new-window",
		"https://accounts.google.com/",
	}
}

func browserCDPReady(ctx context.Context, baseURL string) bool {
	requestCtx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/json/version", nil)
	if err != nil {
		return false
	}
	response, err := (&http.Client{Timeout: time.Second}).Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var payload struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	return json.NewDecoder(response.Body).Decode(&payload) == nil && strings.HasPrefix(payload.WebSocketDebuggerURL, "ws://")
}

func localPortAvailable(port int) bool {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}
