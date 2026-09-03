package browser_runtime

import (
	statepkg "athena-launcher/internal/state"
	"context"
	"encoding/json"
	"errors"
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
	"sync"
	"time"
)

var (
	browserSessionPattern        = regexp.MustCompile(`^athena-[a-f0-9]{32}$`)
	browserRefPattern            = regexp.MustCompile(`^@e[0-9]+$`)
	browserTabPattern            = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	browserElementLineAtRef      = regexp.MustCompile(`@e[0-9]+`)
	browserElementLineBracketRef = regexp.MustCompile(`\[ref=([eE][0-9]+)(?:\s*,\s*)?`)
)

const (
	browserAuthModeIsolated    = "isolated"
	browserAuthModeProfile     = "profile"
	browserAuthModeAutoConnect = "auto_connect"
)

const browserDocumentObservationScript = `(() => ({
  url: location.href,
  title: document.title || "",
  content: document.body ? (document.body.innerText || "") : ""
}))()`

type browserController struct {
	home           string
	dataDir        string
	encryptionKey  string
	runtime        *browserRuntime
	perception     *perceptionLayer
	taskPlanner    *browserTaskPlanner
	targetResolver *browserTargetResolver
	automation     *browserAutomationEngine
	sessionLocks   sync.Map
}

func newBrowserController(home string) *browserController {
	runtime := newBrowserRuntime(home)
	controller := &browserController{
		home: home, runtime: runtime, perception: newPerceptionLayer(home, runtime),
		taskPlanner: newBrowserTaskPlanner(), targetResolver: newBrowserTargetResolver(),
	}
	controller.automation = newBrowserAutomationEngine(home)
	controller.automation.bind(controller)
	return controller
}

func (b *browserController) runAction(ctx context.Context, request browserExecuteRequest) (map[string]any, error) {
	lock := b.browserSessionLock(request.SessionID)
	lock.Lock()
	defer lock.Unlock()
	result, err := b.runActionUnlocked(ctx, request)
	if err != nil && b.runtime != nil {
		b.runtime.SetSessionStatus(request.SessionID, "ERROR")
	}
	return result, err
}

func (b *browserController) browserSessionLock(sessionID string) *sync.Mutex {
	key := strings.TrimSpace(sessionID)
	if key == "" {
		key = "__default__"
	}
	value, _ := b.sessionLocks.LoadOrStore(key, &sync.Mutex{})
	return value.(*sync.Mutex)
}

func (b *browserController) runActionUnlocked(ctx context.Context, request browserExecuteRequest) (map[string]any, error) {
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
		operation := browserCommandOperation(args)
		commandCtx, cancel := context.WithTimeout(ctx, timeout)
		command := b.browserCommand(commandCtx, executable, args...)
		output, commandErr := command.CombinedOutput()
		commandContextErr := commandCtx.Err()
		cancel()
		if commandContextErr != nil {
			return "", fmt.Errorf("agent-browser %s timed out after %s: %w", operation, timeout, commandContextErr)
		}
		if commandErr != nil {
			detail := strings.TrimSpace(string(output))
			if isRecoverableBrowserConnectionError(detail) && clearStaleBrowserSessionConfig(request.SessionID, b.dataDir) == nil {
				if b.runtime != nil {
					b.runtime.SetHeaded(request.SessionID, false)
				}
				retryCtx, retryCancel := context.WithTimeout(ctx, timeout)
				retryCommand := b.browserCommand(retryCtx, executable, args...)
				retryOutput, retryErr := retryCommand.CombinedOutput()
				retryContextErr := retryCtx.Err()
				retryCancel()
				if retryContextErr != nil {
					return "", fmt.Errorf("agent-browser %s recovery timed out after %s: %w", operation, timeout, retryContextErr)
				}
				if retryErr == nil {
					return cleanBrowserCommandOutput(string(retryOutput)), nil
				}
				commandErr = retryErr
				detail = strings.TrimSpace(string(retryOutput))
			}
			if len(detail) > 4000 {
				detail = detail[len(detail)-4000:]
			}
			if hasBrowserArgument(args, "--auto-connect") && strings.Contains(strings.ToLower(detail), "no running chrome instance") {
				return "", fmt.Errorf("agent-browser %s: auto_connect could not find a Chrome CDP session; launch Chrome with --remote-debugging-port=9222, then retry: %s", operation, detail)
			}
			return "", fmt.Errorf("agent-browser %s: %w: %s", operation, commandErr, detail)
		}
		return cleanBrowserCommandOutput(string(output)), nil
	}
	sessionArgs := b.sessionArgs(request.SessionID)
	var playback map[string]any
	var expectedMediaKind, expectedMediaID string
	switch request.Action {
	case "navigate":
		if err := validateBrowserPagePrecondition(run, sessionArgs, value("expected_page_url")); err != nil {
			return nil, err
		}
		target, err := validateBrowserTarget(value("url"))
		if err != nil {
			return nil, err
		}
		headed, _ := request.Arguments["headed"].(bool)
		hasSessionContent := b.runtime != nil && b.runtime.HasSessionContent(request.SessionID)
		requiresHeadedLaunch := headed && b.runtime != nil && b.runtime.RequiresHeadedLaunch()
		liveSession := hasSessionContent && browserAgentSessionActive(run, sessionArgs)
		restartAsHeaded := shouldRestartBrowserAsHeaded(
			requiresHeadedLaunch,
			hasSessionContent,
			liveSession,
			b.runtime != nil && b.runtime.IsHeaded(request.SessionID),
			b.runtime != nil && b.runtime.WasLoadedHeaded(request.SessionID),
		)
		if restartAsHeaded {
			// Startup flags cannot change an existing agent-browser daemon. Close a
			// confirmed live headless session before reopening it as a visible window.
			_, _ = run(10*time.Second, append(sessionArgs, "close")...)
		}
		if requiresHeadedLaunch && !hasBrowserArgument(sessionArgs, "--headed") {
			sessionArgs = append(sessionArgs, "--headed")
		}
		if browserOpenMode(request.Arguments) == "tab" && liveSession && !restartAsHeaded {
			label := browserTabLabel(request.Arguments, target)
			if label == "" {
				label = "page"
			}
			if tabRef := reusableBrowserTabRef(run, sessionArgs, target, label); tabRef != "" {
				if err := runIdempotentBrowserCommand(run, 30*time.Second, append(sessionArgs, "tab", tabRef)...); err != nil {
					return nil, err
				}
				if err := runBrowserNavigationCommand(run, sessionArgs, target, append(sessionArgs, "open", target)...); err != nil {
					return nil, err
				}
				request.Arguments["tab_ref"] = tabRef
			} else {
				args := append(append([]string{}, sessionArgs...), "tab", "new", "--label", label, target)
				if _, err := run(30*time.Second, args...); err != nil {
					if recoveredRef, recovered := activateExactBrowserTarget(run, sessionArgs, target); recovered {
						request.Arguments["tab_ref"] = recoveredRef
						request.Arguments["navigation_recovered"] = true
					} else {
						return nil, err
					}
				}
			}
			request.Arguments["tab_label"] = label
		} else {
			args := browserOpenArgs(sessionArgs, target, request.UserTakeover || headed)
			if err := runBrowserNavigationCommand(run, sessionArgs, target, args...); err != nil {
				return nil, err
			}
		}
		if headed && b.runtime != nil {
			b.runtime.SetHeaded(request.SessionID, true)
		}
	case "click":
		if err := validateBrowserPagePrecondition(run, sessionArgs, value("expected_page_url")); err != nil {
			return nil, err
		}
		waitForTransition, _ := request.Arguments["wait_for_document_transition"].(bool)
		beforeURL, beforeTitle := "", ""
		if waitForTransition {
			beforeURL, _ = run(10*time.Second, append(sessionArgs, "get", "url")...)
			beforeTitle, _ = run(10*time.Second, append(sessionArgs, "get", "title")...)
		}
		ref := value("ref")
		if !browserRefPattern.MatchString(ref) {
			return nil, fmt.Errorf("%s requires a snapshot ref such as @e12", request.Action)
		}
		if rawTarget := value("target_url"); rawTarget != "" {
			currentURL := value("expected_page_url")
			if currentURL == "" {
				currentURL, _ = run(10*time.Second, append(sessionArgs, "get", "url")...)
			}
			target, err := validateSuggestedBrowserTarget(currentURL, rawTarget)
			if err != nil {
				return nil, err
			}
			if _, err := run(30*time.Second, append(sessionArgs, "open", target)...); err != nil {
				return nil, err
			}
		} else {
			resolvedRef, err := resolveCurrentBrowserRef(run, sessionArgs, ref, value("target_label"))
			if err != nil {
				return nil, err
			}
			if _, err := run(20*time.Second, append(sessionArgs, "click", resolvedRef)...); err != nil {
				return nil, err
			}
		}
		if waitForTransition {
			request.Arguments["document_transition_observed"] = waitForBrowserDocumentTransition(
				run, sessionArgs, beforeURL, beforeTitle, 12*time.Second,
			)
		}
	case "play":
		expectedURL := value("expected_page_url")
		if err := validateBrowserPagePrecondition(run, sessionArgs, expectedURL); err != nil {
			return nil, err
		}
		if rawTarget := value("target_url"); rawTarget != "" {
			currentURL := expectedURL
			if currentURL == "" {
				currentURL, _ = run(10*time.Second, append(sessionArgs, "get", "url")...)
			}
			target, err := validateSuggestedBrowserTarget(currentURL, rawTarget)
			if err != nil {
				return nil, err
			}
			expectedMediaKind, expectedMediaID = browserMediaIdentity(target)
			if normalizeBrowserPageURL(currentURL) != normalizeBrowserPageURL(target) {
				if err := runBrowserNavigationCommand(run, sessionArgs, target, append(sessionArgs, "open", target)...); err != nil {
					return nil, err
				}
				if _, err := run(12*time.Second, append(sessionArgs, "wait", "1200")...); err != nil {
					return nil, err
				}
			}
		} else if ref := value("ref"); ref != "" {
			if !browserRefPattern.MatchString(ref) {
				return nil, fmt.Errorf("play requires a valid snapshot ref")
			}
			resolvedRef, err := resolveCurrentBrowserRef(run, sessionArgs, ref, value("target_label"))
			if err != nil {
				return nil, err
			}
			_, _ = run(15*time.Second, append(sessionArgs, "click", resolvedRef)...)
		}
		var err error
		playback, err = startBrowserPlayback(run, sessionArgs)
		if playback != nil {
			playback = enrichBrowserPlaybackIdentity(run, sessionArgs, playback)
		}
		if err == nil {
			err = validateBrowserPlaybackTarget(playback, expectedMediaKind, expectedMediaID, value("target_label"))
		}
		if err != nil {
			observation, observationErr := browserObservation(run, sessionArgs)
			if observationErr == nil {
				if playback != nil {
					observation["playback"] = playback
				}
				annotateBrowserIntervention(observation)
				observation = b.observeBrowser(request, observation, run, sessionArgs)
				return observation, err
			}
			return nil, err
		}
	case "pause":
		var err error
		playback, err = pauseBrowserPlayback(run, sessionArgs)
		if err != nil {
			observation, observationErr := browserObservation(run, sessionArgs)
			if observationErr == nil {
				if playback != nil {
					observation["playback"] = playback
				}
				annotateBrowserIntervention(observation)
				observation = b.observeBrowser(request, observation, run, sessionArgs)
				return observation, err
			}
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
			request, path, err = b.runtime.PrepareDownload(request)
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
	case "hover":
		ref := value("ref")
		if !browserRefPattern.MatchString(ref) {
			return nil, fmt.Errorf("hover requires a snapshot ref such as @e12")
		}
		resolvedRef, err := resolveCurrentBrowserRef(run, sessionArgs, ref, value("target_label"))
		if err != nil {
			return nil, err
		}
		if _, err := run(20*time.Second, append(sessionArgs, "hover", resolvedRef)...); err != nil {
			return nil, err
		}
	case "select":
		ref := value("ref")
		if !browserRefPattern.MatchString(ref) || value("value") == "" {
			return nil, fmt.Errorf("select requires a snapshot ref and non-empty value")
		}
		resolvedRef, err := resolveCurrentBrowserRef(run, sessionArgs, ref, value("target_label"))
		if err != nil {
			return nil, err
		}
		if _, err := run(20*time.Second, append(sessionArgs, "select", resolvedRef, value("value"))...); err != nil {
			return nil, err
		}
	case "drag":
		sourceRef, targetRef := value("ref"), value("target_ref")
		if !browserRefPattern.MatchString(sourceRef) || !browserRefPattern.MatchString(targetRef) || sourceRef == targetRef {
			return nil, fmt.Errorf("drag requires different source and target snapshot refs")
		}
		resolvedSource, err := resolveCurrentBrowserRef(run, sessionArgs, sourceRef, value("target_label"))
		if err != nil {
			return nil, err
		}
		resolvedTarget, err := resolveCurrentBrowserRef(run, sessionArgs, targetRef, value("destination_label"))
		if err != nil {
			return nil, err
		}
		if _, err := run(25*time.Second, append(sessionArgs, "drag", resolvedSource, resolvedTarget)...); err != nil {
			return nil, err
		}
	case "press":
		if _, err := run(20*time.Second, append(sessionArgs, "press", value("value"))...); err != nil {
			return nil, err
		}
	case "scroll":
		direction := value("value")
		if direction != "up" && direction != "down" && direction != "left" && direction != "right" {
			return nil, fmt.Errorf("scroll direction must be up, down, left, or right")
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
	case "back", "forward", "refresh":
		command := request.Action
		beforeURL, _ := run(10*time.Second, append(sessionArgs, "get", "url")...)
		if command == "refresh" {
			command = "reload"
			persistedURL := ""
			if b.runtime != nil {
				persistedURL = b.runtime.Snapshot().Sessions[request.SessionID].CurrentURL
			}
			var restored bool
			beforeURL, restored, err = prepareBrowserRefreshTarget(run, sessionArgs, beforeURL, persistedURL)
			if err != nil {
				return nil, err
			}
			if restored {
				if request.Arguments == nil {
					request.Arguments = make(map[string]any)
				}
				request.Arguments["restored_current_page"] = true
			}
		}
		beforeTitle, _ := run(10*time.Second, append(sessionArgs, "get", "title")...)
		if _, err := run(30*time.Second, append(sessionArgs, command)...); err != nil {
			return nil, err
		}
		request.Arguments["document_transition_observed"] = waitForBrowserDocumentTransition(
			run, sessionArgs, beforeURL, beforeTitle, 12*time.Second,
		)
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
		captureRequest := request
		captureRequest.Arguments = clonePerceptionArguments(request.Arguments)
		captureRequest.Arguments["screenshot"] = true
		screenshot := b.runtime.CaptureScreenshot(captureRequest, run, sessionArgs)
		if screenshot == nil {
			return nil, fmt.Errorf("browser screenshot provider returned no result")
		}
		if available, _ := screenshot["available"].(bool); !available {
			return nil, fmt.Errorf("browser screenshot failed: %s", browserStringValue(screenshot["error"]))
		}
		result := map[string]any{"screenshot_path": screenshot["path"], "screenshot": screenshot}
		result = b.observeBrowser(request, result, run, sessionArgs)
		return result, nil
	case "upload":
		return nil, fmt.Errorf("upload requires the native file picker and user takeover")
	case "close":
		if tabID := value("tab_id"); tabID != "" {
			if !browserTabPattern.MatchString(tabID) {
				return nil, fmt.Errorf("invalid browser tab id %q", tabID)
			}
			if _, err := run(10*time.Second, append(sessionArgs, "tab", "close", tabID)...); err != nil {
				return nil, err
			}
			observation, err := browserObservation(run, sessionArgs)
			if err != nil {
				return nil, err
			}
			observation["closed"] = true
			observation["closed_tab_id"] = tabID
			annotateBrowserIntervention(observation)
			return b.observeBrowser(request, observation, run, sessionArgs), nil
		}
		daemonPID, pidErr := b.managedBrowserDaemonPID(request.SessionID)
		_, closeErr := run(10*time.Second, append(sessionArgs, "close")...)
		stopErr := b.stopManagedBrowserDaemon(request.SessionID, daemonPID)
		err := errors.Join(pidErr, closeErr, stopErr)
		return map[string]any{"closed": err == nil}, err
	default:
		return nil, fmt.Errorf("unsupported browser action %q", request.Action)
	}
	observation, err := browserObservation(run, sessionArgs)
	if err != nil {
		return nil, err
	}
	observation = stabilizeBrowserObservation(ctx, request, observation, func() (map[string]any, error) {
		return browserObservation(run, sessionArgs)
	})
	annotateBrowserIntervention(observation)
	if playback != nil {
		observation["playback"] = playback
	}
	observation = b.observeBrowser(request, observation, run, sessionArgs)
	return observation, nil
}

func browserCommandOperation(arguments []string) string {
	known := map[string]bool{
		"open": true, "tab": true, "snapshot": true, "get": true, "eval": true,
		"click": true, "fill": true, "hover": true, "select": true, "drag": true,
		"press": true, "scroll": true, "wait": true, "back": true, "forward": true,
		"reload": true, "screenshot": true, "download": true, "close": true, "status": true,
	}
	for index, argument := range arguments {
		if !known[argument] {
			continue
		}
		if argument == "tab" && index+1 < len(arguments) {
			switch arguments[index+1] {
			case "new", "list", "close":
				return "tab." + arguments[index+1]
			}
		}
		return argument
	}
	return "command"
}

func runIdempotentBrowserCommand(run browserCommandRunner, timeout time.Duration, arguments ...string) error {
	if _, err := run(timeout, arguments...); err == nil {
		return nil
	} else {
		firstErr := err
		if _, retryErr := run(timeout, arguments...); retryErr == nil {
			return nil
		} else {
			return fmt.Errorf("browser command failed after one retry: first attempt: %v | retry: %w", firstErr, retryErr)
		}
	}
}

func runBrowserNavigationCommand(run browserCommandRunner, sessionArgs []string, target string, arguments ...string) error {
	if _, err := run(30*time.Second, arguments...); err == nil {
		return nil
	} else {
		firstErr := err
		if _, recovered := activateExactBrowserTarget(run, sessionArgs, target); recovered {
			return nil
		}
		if _, retryErr := run(30*time.Second, arguments...); retryErr == nil {
			return nil
		} else if _, recovered := activateExactBrowserTarget(run, sessionArgs, target); recovered {
			return nil
		} else {
			return fmt.Errorf("browser navigation failed after one retry: first attempt: %v | retry: %w", firstErr, retryErr)
		}
	}
}

func activateExactBrowserTarget(run browserCommandRunner, sessionArgs []string, target string) (string, bool) {
	output, err := run(10*time.Second, append(sessionArgs, "tab", "list", "--json")...)
	if err != nil {
		return "", false
	}
	want := normalizeBrowserPageURL(target)
	for _, tab := range parseBrowserCommandTabs(output) {
		if tab.Ref == "" || normalizeBrowserPageURL(tab.URL) != want {
			continue
		}
		if !tab.Active {
			if err := runIdempotentBrowserCommand(run, 10*time.Second, append(sessionArgs, "tab", tab.Ref)...); err != nil {
				return "", false
			}
		}
		return tab.Ref, true
	}
	return "", false
}

func waitForBrowserDocumentTransition(
	run browserCommandRunner,
	sessionArgs []string,
	beforeURL string,
	beforeTitle string,
	timeout time.Duration,
) bool {
	if timeout <= 0 {
		timeout = 12 * time.Second
	}
	attempts := int(timeout / (500 * time.Millisecond))
	if attempts < 1 {
		attempts = 1
	}
	for range attempts {
		_, _ = run(2*time.Second, append(sessionArgs, "wait", "500")...)
		currentURL, _ := run(5*time.Second, append(sessionArgs, "get", "url")...)
		currentTitle, _ := run(5*time.Second, append(sessionArgs, "get", "title")...)
		if browserDocumentTransitioned(beforeURL, beforeTitle, currentURL, currentTitle) {
			// Let the first post-navigation render settle before collecting options.
			_, _ = run(3*time.Second, append(sessionArgs, "wait", "700")...)
			return true
		}
	}
	return false
}

func browserDocumentTransitioned(beforeURL, beforeTitle, currentURL, currentTitle string) bool {
	beforeURL = normalizeBrowserPageURL(beforeURL)
	currentURL = normalizeBrowserPageURL(currentURL)
	if beforeURL != "" && currentURL != "" && beforeURL != currentURL {
		return true
	}
	beforeTitle = strings.TrimSpace(beforeTitle)
	currentTitle = strings.TrimSpace(currentTitle)
	return beforeTitle != "" && currentTitle != "" && !strings.EqualFold(beforeTitle, currentTitle)
}

func isRecoverableBrowserConnectionError(detail string) bool {
	lower := strings.ToLower(strings.TrimSpace(detail))
	if !strings.Contains(lower, "failed to connect") && !strings.Contains(lower, "connection refused") {
		return false
	}
	return strings.Contains(lower, "no such file or directory") ||
		strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "socket")
}

func clearStaleBrowserSessionConfig(sessionID string, configuredDataDir ...string) error {
	sessionID = strings.TrimSpace(sessionID)
	if !browserSessionPattern.MatchString(sessionID) {
		return fmt.Errorf("invalid browser session id")
	}
	configured := ""
	if len(configuredDataDir) > 0 {
		configured = configuredDataDir[0]
	}
	root := effectiveAgentBrowserDataDir(configured)
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		root = filepath.Join(home, ".agent-browser")
	}
	path := filepath.Join(root, "namespaces", "athena", "run", sessionID+".config")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func browserOpenArgs(sessionArgs []string, target string, headed bool) []string {
	args := append([]string{}, sessionArgs...)
	if headed && !hasBrowserArgument(args, "--headed") {
		args = append(args, "--headed")
	}
	return append(args, "open", target)
}

func browserAgentSessionActive(run browserCommandRunner, sessionArgs []string) bool {
	if run == nil {
		return false
	}
	output, err := run(5*time.Second, append(append([]string{}, sessionArgs...), "session", "info", "--json")...)
	if err != nil {
		return false
	}
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			Active bool `json:"active"`
		} `json:"data"`
	}
	return json.Unmarshal([]byte(output), &response) == nil && response.Success && response.Data.Active
}

func prepareBrowserRefreshTarget(run browserCommandRunner, sessionArgs []string, currentURL, persistedURL string) (string, bool, error) {
	if current, err := validateBrowserTarget(currentURL); err == nil {
		return current, false, nil
	}
	persisted, err := validateBrowserTarget(persistedURL)
	if err != nil {
		return "", false, fmt.Errorf("no live browser page is available to refresh")
	}
	if err := runBrowserNavigationCommand(run, sessionArgs, persisted, append(sessionArgs, "open", persisted)...); err != nil {
		return "", false, fmt.Errorf("restore current browser page before refresh: %w", err)
	}
	restored, err := run(10*time.Second, append(sessionArgs, "get", "url")...)
	if err != nil {
		return "", false, fmt.Errorf("observe restored browser page: %w", err)
	}
	restored, err = validateBrowserTarget(restored)
	if err != nil {
		return "", false, fmt.Errorf("restored browser page is unavailable")
	}
	return restored, true, nil
}

func shouldRestartBrowserAsHeaded(requiresHeaded, hasContent, liveSession, headedNow, loadedHeaded bool) bool {
	return requiresHeaded && hasContent && liveSession && !headedNow && !loadedHeaded
}

func hasBrowserArgument(args []string, expected string) bool {
	for _, arg := range args {
		if arg == expected {
			return true
		}
	}
	return false
}

func (b *browserController) observeBrowser(request browserExecuteRequest, observation map[string]any, run browserCommandRunner, sessionArgs []string) map[string]any {
	if b == nil || b.runtime == nil {
		return observation
	}
	if !browserChallengeDetected(observation) {
		observation = enrichBrowserCandidates(run, sessionArgs, observation)
	}
	perception := b.perception
	if perception == nil {
		perception = newPerceptionLayer(b.home, b.runtime)
	}
	result := perception.ObserveBrowser(request, observation, run, sessionArgs)
	if b.automation != nil {
		automationState := b.automation.sessionState(request.SessionID)
		result["automation_state"] = automationState
		status := "RUNNING"
		if intervention, _ := result["user_intervention_required"].(bool); intervention || browserChallengeDetected(result) {
			status = "WAITING"
		} else if active, _ := automationState["active_count"].(int); active > 0 {
			status = "AUTOMATION_ACTIVE"
		}
		b.runtime.SetSessionStatus(request.SessionID, status)
		if runtimeState, _ := result["browser_runtime"].(map[string]any); runtimeState != nil {
			if sessionState, _ := runtimeState["session"].(map[string]any); sessionState != nil {
				sessionState["status"] = status
			}
		}
	}
	if suggestions := browserSuggestedActions(request.SessionID, result); len(suggestions) > 0 {
		result["suggested_actions"] = suggestions
	} else {
		delete(result, "suggested_actions")
	}
	return result
}

func (b *browserController) sessionArgs(sessionID string) []string {
	if b.runtime != nil {
		return b.runtime.SessionArgs(sessionID)
	}
	return newBrowserRuntime(b.home).SessionArgs(sessionID)
}

func (b *browserController) runDownload(ctx context.Context, executable string, sessionArgs []string, ref, path string, request browserExecuteRequest) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("download path is required")
	}
	started := time.Now()
	request.EmitProgress(browserActionProgress{
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
				request.EmitProgress(browserActionProgress{
					Stage: "downloading", Progress: browserDownloadProgress(bytes, int64Argument(request.Arguments["expected_bytes"]), 15, 85),
					Message: fmt.Sprintf("Downloading file: %s", humanBytes(bytes)),
					Bytes:   bytes, Total: int64Argument(request.Arguments["expected_bytes"]), State: snapshot,
				})
			}
		}
	}()
	args := append(append([]string{}, sessionArgs...), "download", ref, path)
	command := b.browserCommand(commandCtx, executable, args...)
	output, commandErr := command.CombinedOutput()
	close(done)
	if commandCtx.Err() != nil {
		request.EmitProgress(browserActionProgress{Stage: "failed", Progress: 100, Message: "Download timed out", State: map[string]any{"path": path}})
		return fmt.Errorf("browser download timed out: %w", commandCtx.Err())
	}
	if commandErr != nil {
		detail := strings.TrimSpace(string(output))
		if len(detail) > 4000 {
			detail = detail[len(detail)-4000:]
		}
		request.EmitProgress(browserActionProgress{Stage: "failed", Progress: 100, Message: "Download failed", State: map[string]any{"path": path, "error": detail}})
		return fmt.Errorf("agent-browser download: %w: %s", commandErr, detail)
	}
	snapshot := browserDownloadFileSnapshot(path, started)
	bytes, _ := snapshot["bytes"].(int64)
	request.EmitProgress(browserActionProgress{
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

type browserCommandTab struct {
	Ref    string
	Label  string
	URL    string
	Title  string
	Active bool
}

func reusableBrowserTabRef(run browserCommandRunner, sessionArgs []string, target, label string) string {
	if run == nil {
		return ""
	}
	output, err := run(10*time.Second, append(sessionArgs, "tab", "list", "--json")...)
	if err != nil {
		return ""
	}
	tabs := parseBrowserCommandTabs(output)
	bestRef, bestScore := "", 0
	for _, tab := range tabs {
		score := scoreReusableBrowserTab(tab, target, label)
		if score >= 50 && score > bestScore {
			bestRef, bestScore = tab.Ref, score
		}
	}
	return bestRef
}

func parseBrowserCommandTabs(output string) []browserCommandTab {
	var parsed any
	if json.Unmarshal([]byte(output), &parsed) != nil {
		return nil
	}
	var tabs []browserCommandTab
	var visit func(any)
	visit = func(value any) {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				visit(item)
			}
		case map[string]any:
			ref := firstBrowserString(typed, "tabId", "id")
			address := firstBrowserString(typed, "url")
			title := firstBrowserString(typed, "title")
			if ref != "" && (address != "" || title != "") {
				active, _ := typed["active"].(bool)
				tabs = append(tabs, browserCommandTab{Ref: ref, Label: firstBrowserString(typed, "label"), URL: address, Title: title, Active: active})
				return
			}
			for _, nested := range typed {
				visit(nested)
			}
		}
	}
	visit(parsed)
	return tabs
}

func firstBrowserString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, _ := values[key].(string); strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func scoreReusableBrowserTab(tab browserCommandTab, target, label string) int {
	if tab.Ref == "" {
		return 0
	}
	score := 0
	if label != "" && strings.EqualFold(strings.TrimSpace(tab.Label), strings.TrimSpace(label)) {
		score += 30
	}
	targetURL, targetErr := url.Parse(strings.TrimSpace(target))
	tabURL, tabErr := url.Parse(strings.TrimSpace(tab.URL))
	if targetErr == nil && tabErr == nil && targetURL.Hostname() != "" && strings.EqualFold(targetURL.Hostname(), tabURL.Hostname()) {
		score += 50
		if strings.TrimRight(targetURL.Path, "/") == strings.TrimRight(tabURL.Path, "/") && targetURL.RawQuery == tabURL.RawQuery {
			score += 100
		}
	}
	if tab.Active {
		score++
	}
	return score
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
	label := strings.Trim(builder.String(), "-")
	if label == "" {
		return ""
	}
	if label[0] < 'a' || label[0] > 'z' {
		label = "page-" + label
	}
	return label
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
		if state, err := statepkg.Load(home); err == nil && state != nil {
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
	if home != "" {
		if state, err := statepkg.Load(home); err == nil {
			return browserProfileFromState(home, state)
		}
	}
	return browserProfileFromState(home, nil)
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
			"browser.task", "browser.open", "browser.navigate", "browser.observe", "browser.click", "browser.play", "browser.pause", "browser.type", "browser.hover",
			"browser.select", "browser.drag", "browser.press", "browser.scroll", "browser.back", "browser.forward", "browser.refresh", "browser.wait",
			"browser.download", "browser.screenshot", "browser.automation", "browser.close",
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
	currentURL, title, content := "", "", ""
	if output, err := run(15*time.Second, append(sessionArgs, "eval", browserDocumentObservationScript, "--json")...); err == nil {
		if document, ok := parseBrowserDocumentObservation(output); ok {
			currentURL = browserStringValue(document["url"])
			title = browserStringValue(document["title"])
			content = browserStringValue(document["content"])
		}
	}
	if currentURL == "" {
		currentURL, _ = run(10*time.Second, append(sessionArgs, "get", "url")...)
		title, _ = run(10*time.Second, append(sessionArgs, "get", "title")...)
		content, _ = run(15*time.Second, append(sessionArgs, "get", "text", "body")...)
	}
	snapshot, err := run(15*time.Second, append(sessionArgs, "snapshot", "-i", "--urls", "-c", "-d", "6")...)
	if err != nil {
		return nil, err
	}
	content = truncateBrowserOutput(content, 120000)
	snapshot = truncateBrowserOutput(snapshot, 80000)
	keyElements := browserKeyElements(snapshot, 80)
	observationStrategy := "interactive"
	if shouldExpandBrowserSnapshot(content, keyElements) {
		if expanded, expandedErr := run(15*time.Second, append(sessionArgs, "snapshot", "--urls", "-c", "-d", "6")...); expandedErr == nil {
			expanded = truncateBrowserOutput(expanded, 80000)
			expandedElements := browserKeyElements(expanded, 120)
			if merged := mergeBrowserKeyElements(120, keyElements, expandedElements); len(merged) > len(keyElements) {
				keyElements = merged
				snapshot = expanded
				observationStrategy = "interactive_plus_accessibility"
			}
		}
	}
	return map[string]any{
		"url": currentURL, "title": title, "content": content, "snapshot": snapshot,
		"page":                 map[string]any{"url": currentURL, "title": title},
		"key_elements":         keyElements,
		"observation_strategy": observationStrategy,
	}, nil
}

func shouldExpandBrowserSnapshot(content string, elements []map[string]string) bool {
	if len(elements) > 4 {
		return false
	}
	lower := strings.ToLower(content)
	for _, signal := range []string{
		"who's watching", "who is watching", "choose a profile", "select a profile",
		"choose an account", "select an account", "choose one", "pick one",
		"选择个人资料", "选择资料", "选择账号", "选择账户", "选择一个", "誰在觀看", "プロフィールを選択",
	} {
		if strings.Contains(lower, signal) {
			return true
		}
	}
	if len(elements) > 1 {
		return false
	}
	meaningfulLines := 0
	for _, line := range strings.Split(content, "\n") {
		if len([]rune(strings.TrimSpace(line))) >= 2 {
			meaningfulLines++
		}
	}
	return meaningfulLines >= 4
}

func mergeBrowserKeyElements(limit int, groups ...[]map[string]string) []map[string]string {
	if limit <= 0 {
		limit = 120
	}
	result := make([]map[string]string, 0, limit)
	seen := make(map[string]bool, limit)
	for _, group := range groups {
		for _, element := range group {
			ref := strings.TrimSpace(element["ref"])
			label := strings.TrimSpace(element["label"])
			if !browserRefPattern.MatchString(ref) || label == "" || seen[ref] {
				continue
			}
			seen[ref] = true
			result = append(result, map[string]string{"ref": ref, "label": label})
			if len(result) >= limit {
				return result
			}
		}
	}
	return result
}

func parseBrowserDocumentObservation(output string) (map[string]any, bool) {
	var decoded any
	if json.Unmarshal([]byte(strings.TrimSpace(output)), &decoded) != nil {
		return nil, false
	}
	return findBrowserDocumentObservation(decoded)
}

func findBrowserDocumentObservation(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		if _, hasURL := typed["url"]; hasURL {
			if _, hasContent := typed["content"]; hasContent {
				return typed, true
			}
		}
		for _, key := range []string{"data", "result", "value"} {
			if nested, exists := typed[key]; exists {
				if result, found := findBrowserDocumentObservation(nested); found {
					return result, true
				}
			}
		}
	case string:
		var nested any
		if json.Unmarshal([]byte(strings.TrimSpace(typed)), &nested) == nil {
			return findBrowserDocumentObservation(nested)
		}
	}
	return nil, false
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
	if strings.Contains(currentURL, "captcha") || strings.Contains(title, "captcha") {
		return map[string]any{
			"kind":                   "captcha",
			"message":                "The page is asking for CAPTCHA or human verification. User takeover is required.",
			"current_url":            state["url"],
			"title":                  state["title"],
			"requires_user_takeover": true,
		}
	}
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
			needles: []string{"verify you are human", "i am not a robot", "prove you are human", "complete the security check"},
		},
		{
			kind:    "cloudflare_challenge",
			message: "The page is behind a Cloudflare or browser integrity challenge. User takeover may be required.",
			needles: []string{
				"checking if the site connection is secure", "checking your browser", "just a moment",
				"performing security verification", "please wait while we verify", "cf-challenge",
				"cloudflare ray id", "attention required",
			},
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

func detectBrowserIntervention(state map[string]any) map[string]any {
	if challenge := detectBrowserChallenge(state); challenge != nil {
		challenge["category"] = "challenge"
		return challenge
	}
	if state == nil {
		return nil
	}
	snapshot := lowerStateString(state, "snapshot")
	for _, line := range strings.Split(snapshot, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "dialog") {
			continue
		}
		if strings.Contains(line, "log in") || strings.Contains(line, "login") || strings.Contains(line, "sign in") ||
			strings.Contains(line, "登录") || strings.Contains(line, "扫码") || strings.Contains(line, "qr code") || strings.Contains(line, "two-factor") {
			return map[string]any{
				"kind":                   "authentication_required",
				"category":               "authentication",
				"message":                "The website requires sign-in or account verification before this action can continue. Complete it in the visible browser, then resume the same Athena session.",
				"current_url":            state["url"],
				"title":                  state["title"],
				"requires_user_takeover": true,
			}
		}
	}
	return nil
}

func annotateBrowserIntervention(state map[string]any) {
	intervention := detectBrowserIntervention(state)
	if intervention == nil {
		return
	}
	state["user_intervention_required"] = true
	state["intervention"] = intervention
	if browserStringValue(intervention["category"]) == "challenge" {
		state["challenge_detected"] = true
		state["challenge"] = intervention
	}
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
		ref, label, ok := parseBrowserElementLine(line)
		if !ok {
			continue
		}
		if !browserRefPattern.MatchString(ref) {
			continue
		}
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

func parseBrowserElementLine(line string) (string, string, bool) {
	if location := browserElementLineAtRef.FindStringIndex(line); location != nil {
		ref := line[location[0]:location[1]]
		label := strings.TrimSpace(line[:location[0]] + line[location[1]:])
		return ref, label, true
	}
	location := browserElementLineBracketRef.FindStringSubmatchIndex(line)
	if len(location) < 4 {
		return "", "", false
	}
	ref := "@" + strings.ToLower(line[location[2]:location[3]])
	matched := line[location[0]:location[1]]
	remainder := line[location[1]:]
	if strings.Contains(matched, ",") {
		// agent-browser emits [ref=e4, url=...]. Keep the URL metadata in
		// the normalized label while removing only the ref field.
		remainder = "[" + remainder
	} else {
		remainder = strings.TrimPrefix(remainder, "]")
	}
	label := strings.TrimSpace(line[:location[0]] + remainder)
	return ref, label, true
}

func (b *browserController) executable() (string, error) {
	if candidate := strings.TrimSpace(os.Getenv("ATHENA_AGENT_BROWSER_BIN")); candidate != "" {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	state, _ := statepkg.Load(b.home)
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

func OpenSystemURL(ctx context.Context, address string) error { return openSystemURL(ctx, address) }

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
