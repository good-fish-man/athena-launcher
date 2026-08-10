package browser_runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/websocket"
)

const (
	browserCDPEventDebounce = 350 * time.Millisecond
	browserCDPHealthPeriod  = 12 * time.Second
)

const browserCDPObservationScript = `(() => {
  if (window.__athenaBrowserEventMonitorV3) return true;
  window.__athenaBrowserEventMonitorV3 = true;
  let mutationTimer = 0;
  const emit = (kind) => console.debug("__ATHENA_BROWSER_EVENT_V3__:" + kind);
  const observer = new MutationObserver(() => {
    clearTimeout(mutationTimer);
    mutationTimer = setTimeout(() => emit("dom_changed"), 180);
  });
  const start = () => {
    if (document.documentElement) {
      observer.observe(document.documentElement, {subtree:true, childList:true, attributes:true, attributeFilter:["aria-label","aria-hidden","disabled","href","src"]});
    }
  };
  if (document.documentElement) start(); else document.addEventListener("DOMContentLoaded", start, {once:true});
  document.addEventListener("play", () => emit("media_playing"), true);
  document.addEventListener("pause", () => emit("media_paused"), true);
  document.addEventListener("ended", () => emit("media_ended"), true);
  return true;
})()`

type browserCDPMessage struct {
	ID     int            `json:"id,omitempty"`
	Method string         `json:"method,omitempty"`
	Params map[string]any `json:"params,omitempty"`
}

type browserCDPReceive struct {
	message browserCDPMessage
	err     error
}

func (e *browserAutomationEngine) watchCDPEvents(
	ctx context.Context,
	controller *browserController,
	sessionID string,
) error {
	endpoint, err := controller.automationCDPEndpoint(ctx, sessionID)
	if err != nil {
		return err
	}
	config, err := websocket.NewConfig(endpoint, "http://localhost")
	if err != nil {
		return fmt.Errorf("create browser CDP websocket config: %w", err)
	}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		return fmt.Errorf("connect browser CDP event stream: %w", err)
	}
	defer connection.Close()

	for id, command := range []browserCDPMessage{
		{Method: "Page.enable"},
		{Method: "DOM.enable"},
		{Method: "Runtime.enable"},
		{Method: "Page.setLifecycleEventsEnabled", Params: map[string]any{"enabled": true}},
		{Method: "Page.addScriptToEvaluateOnNewDocument", Params: map[string]any{"source": browserCDPObservationScript}},
		{Method: "Runtime.evaluate", Params: map[string]any{"expression": browserCDPObservationScript}},
	} {
		command.ID = id + 1
		if err := websocket.JSON.Send(connection, command); err != nil {
			return fmt.Errorf("subscribe browser CDP events: %w", err)
		}
	}

	received := make(chan browserCDPReceive, 16)
	go func() {
		for {
			var message browserCDPMessage
			err := websocket.JSON.Receive(connection, &message)
			select {
			case received <- browserCDPReceive{message: message, err: err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	if err := e.probeFromBrowserEvent(ctx, controller, sessionID); err != nil {
		return err
	}
	health := time.NewTicker(browserCDPHealthPeriod)
	defer health.Stop()
	var debounce *time.Timer
	var debounceChannel <-chan time.Time
	queueProbe := func() {
		if debounce == nil {
			debounce = time.NewTimer(browserCDPEventDebounce)
		} else {
			if !debounce.Stop() {
				select {
				case <-debounce.C:
				default:
				}
			}
			debounce.Reset(browserCDPEventDebounce)
		}
		debounceChannel = debounce.C
	}
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case result := <-received:
			if result.err != nil {
				return fmt.Errorf("browser CDP event stream closed: %w", result.err)
			}
			if browserCDPObservationEvent(result.message) {
				queueProbe()
			}
		case <-debounceChannel:
			debounceChannel = nil
			if err := e.probeFromBrowserEvent(ctx, controller, sessionID); err != nil {
				return err
			}
		case <-health.C:
			if !e.hasEnabledRules(sessionID) {
				return nil
			}
			current, endpointErr := controller.automationCDPEndpoint(ctx, sessionID)
			if endpointErr != nil {
				return endpointErr
			}
			if current != endpoint {
				return fmt.Errorf("active browser CDP target changed")
			}
		}
	}
}

func (e *browserAutomationEngine) probeFromBrowserEvent(
	ctx context.Context,
	controller *browserController,
	sessionID string,
) error {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	probe, err := controller.probeAutomationState(probeCtx, sessionID)
	if err != nil {
		return err
	}
	e.evaluateProbe(ctx, controller, sessionID, probe)
	return nil
}

func browserCDPObservationEvent(message browserCDPMessage) bool {
	switch message.Method {
	case "Page.loadEventFired", "Page.domContentEventFired", "Page.frameNavigated", "Page.lifecycleEvent", "DOM.documentUpdated":
		return true
	case "Runtime.consoleAPICalled":
		arguments, _ := message.Params["args"].([]any)
		for _, raw := range arguments {
			argument, _ := raw.(map[string]any)
			if strings.HasPrefix(browserStringValue(argument["value"]), "__ATHENA_BROWSER_EVENT_V3__:") {
				return true
			}
		}
	}
	return false
}

func (b *browserController) automationCDPEndpoint(ctx context.Context, sessionID string) (string, error) {
	executable, err := b.executable()
	if err != nil {
		return "", err
	}
	commandCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	arguments := append(b.sessionArgs(sessionID), "get", "cdp-url", "--json")
	output, err := b.browserCommand(commandCtx, executable, arguments...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read browser CDP endpoint: %w: %s", err, strings.TrimSpace(string(output)))
	}
	endpoint, ok := parseBrowserCDPEndpoint(strings.TrimSpace(string(output)))
	if !ok {
		return "", fmt.Errorf("agent-browser did not return a valid local CDP websocket endpoint")
	}
	return endpoint, nil
}

func parseBrowserCDPEndpoint(output string) (string, bool) {
	var parsed any
	if json.Unmarshal([]byte(output), &parsed) == nil {
		if endpoint := findBrowserCDPEndpoint(parsed); endpoint != "" {
			return endpoint, true
		}
	}
	return validateBrowserCDPEndpoint(strings.Trim(output, `"`))
}

func findBrowserCDPEndpoint(value any) string {
	switch typed := value.(type) {
	case string:
		endpoint, _ := validateBrowserCDPEndpoint(typed)
		return endpoint
	case map[string]any:
		for _, key := range []string{"cdp_url", "cdpUrl", "webSocketDebuggerUrl", "websocket_url", "url", "data", "result"} {
			if nested, exists := typed[key]; exists {
				if endpoint := findBrowserCDPEndpoint(nested); endpoint != "" {
					return endpoint
				}
			}
		}
	case []any:
		for _, nested := range typed {
			if endpoint := findBrowserCDPEndpoint(nested); endpoint != "" {
				return endpoint
			}
		}
	}
	return ""
}

func validateBrowserCDPEndpoint(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") || parsed.User != nil {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return "", false
	}
	return parsed.String(), true
}
