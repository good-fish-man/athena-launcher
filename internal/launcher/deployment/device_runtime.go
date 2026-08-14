package deployment

import (
	browser_runtime "athena-launcher/internal/runtime-system/browser-runtime"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	log "github.com/good-fish-man/logx"
	"golang.org/x/net/websocket"
)

type deviceRuntime struct {
	deviceID string
	name     string
	url      string
	urls     []string
	token    string
	bridge   *desktopBridge
	journal  string

	mu             sync.Mutex
	completed      map[string]deviceObservation
	inflight       map[string]context.CancelFunc
	durable        map[string]journalAction
	sequences      map[string]int64
	leaseOwner     string
	fencingToken   uint64
	leaseExpiresAt time.Time
}

func newDeviceRuntime(state *launcherState, bridge *desktopBridge) (*deviceRuntime, error) {
	if state == nil || bridge == nil {
		return nil, fmt.Errorf("launcher state and desktop bridge are required")
	}
	endpoints, err := deviceWebSocketURLs(deploymentFromState(state))
	if err != nil {
		return nil, err
	}
	journalPath := filepath.Join(bridge.home, "data", "device-action-journal-v4.json")
	journal, err := loadDeviceActionJournal(journalPath)
	if err != nil {
		return nil, err
	}
	for key, observation := range journal.Completed {
		if observation.DeviceID == "" {
			observation.DeviceID = state.DeviceID
			journal.Completed[key] = observation
		}
	}
	if err := saveDeviceActionJournal(journalPath, journal); err != nil {
		return nil, err
	}
	return &deviceRuntime{
		deviceID: state.DeviceID, name: "Athena Desktop", url: endpoints[0], urls: endpoints,
		token: deviceRuntimeToken(state), bridge: bridge, journal: journalPath,
		completed: journal.Completed, inflight: make(map[string]context.CancelFunc), durable: journal.InFlight, sequences: journal.Sequences,
	}, nil
}

func deviceRuntimeToken(state *launcherState) string {
	if state != nil && state.ConnectionMode == connectionModeRemote && strings.TrimSpace(state.RemoteDeviceToken) != "" {
		return strings.TrimSpace(state.RemoteDeviceToken)
	}
	if state == nil {
		return ""
	}
	return state.InternalServiceToken
}

func deviceWebSocketURL(selection deploymentSelection) (string, error) {
	endpoints, err := deviceWebSocketURLs(selection)
	if err != nil {
		return "", err
	}
	return endpoints[0], nil
}

func deviceWebSocketURLs(selection deploymentSelection) ([]string, error) {
	base := "http://127.0.0.1:8090"
	if selection.Mode == connectionModeRemote {
		base = selection.RemoteURL
	}
	parsed, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Runtime Client URL")
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return nil, fmt.Errorf("Runtime Client URL must use HTTP or HTTPS")
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	paths := []string{"/v1/control/device", "/api/agent-runtime-client/v1/control/device"}
	if basePath != "" {
		paths = append([]string{basePath + "/control/device"}, paths...)
	}
	seen := make(map[string]bool, len(paths))
	endpoints := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		candidate := *parsed
		candidate.Path = path
		candidate.RawQuery = ""
		candidate.Fragment = ""
		endpoints = append(endpoints, candidate.String())
	}
	return endpoints, nil
}

func (d *deviceRuntime) Run(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		connectedAt := time.Now()
		if err := d.connect(ctx); err != nil && ctx.Err() == nil {
			fmt.Printf("[device-runtime] connection failed: %v\n", err)
		}
		if time.Since(connectedAt) >= 30*time.Second {
			delay = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}

func (d *deviceRuntime) connect(ctx context.Context) error {
	endpoints := d.urls
	if len(endpoints) == 0 && d.url != "" {
		endpoints = []string{d.url}
	}
	var failures []string
	for _, endpoint := range endpoints {
		if err := d.connectEndpoint(ctx, endpoint); err != nil {
			failures = append(failures, endpoint+": "+err.Error())
			continue
		}
		d.url = endpoint
		return nil
	}
	return fmt.Errorf("all device endpoints failed: %s", strings.Join(failures, "; "))
}

func (d *deviceRuntime) connectEndpoint(ctx context.Context, endpoint string) error {
	config, err := websocket.NewConfig(endpoint, "http://localhost")
	if err != nil {
		return err
	}
	config.Header = http.Header{"Authorization": []string{"Bearer " + d.token}}
	connection, err := websocket.DialConfig(config)
	if err != nil {
		if detail := d.probeDeviceEndpoint(ctx, endpoint); detail != "" {
			return fmt.Errorf("%w (%s)", err, detail)
		}
		return err
	}
	defer connection.Close()
	connectionCtx, cancelConnection := context.WithCancel(ctx)
	defer cancelConnection()
	connectionClosed := make(chan struct{})
	defer close(connectionClosed)
	if err := websocket.JSON.Send(connection, map[string]any{
		"protocol": deviceProtocol, "type": "HELLO", "device_id": d.deviceID, "name": d.name,
		"platform": runtime.GOOS, "architecture": runtime.GOARCH, "capabilities": d.capabilities(),
		"capability_instances": d.capabilityInstances(), "sent_at": time.Now().UTC(),
	}); err != nil {
		return err
	}
	writer := &deviceWriter{connection: connection}
	var welcomePayload string
	if err := websocket.Message.Receive(connection, &welcomePayload); err != nil {
		return fmt.Errorf("receive device lease: %w", err)
	}
	var welcome deviceMessage
	if err := decodeDeviceProtocol([]byte(welcomePayload), &welcome); err != nil || welcome.Type != "WELCOME" || welcome.DeviceID != d.deviceID {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal([]byte(welcomePayload), &failure)
		if strings.TrimSpace(failure.Error) != "" {
			return fmt.Errorf("device registration rejected: %s", failure.Error)
		}
		return fmt.Errorf("device registration did not return a valid fenced lease")
	}
	if welcome.FencingToken > 0 && (welcome.LeaseOwner == "" || welcome.LeaseExpiresAt.IsZero()) {
		return fmt.Errorf("device registration returned an incomplete fenced lease")
	}
	d.setLease(welcome.LeaseOwner, welcome.FencingToken, welcome.LeaseExpiresAt)
	stopHeartbeat := make(chan struct{})
	defer close(stopHeartbeat)
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-ticker.C:
				owner, token, expiresAt := d.currentLease()
				_ = writer.Send(deviceMessage{Protocol: deviceProtocol, Type: "HEARTBEAT", DeviceID: d.deviceID, LeaseOwner: owner, FencingToken: token, LeaseExpiresAt: expiresAt, SentAt: time.Now().UTC()})
			}
		}
	}()
	go func() {
		select {
		case <-connectionCtx.Done():
			_ = connection.Close()
		case <-connectionClosed:
		}
	}()
	for {
		var payload string
		if err := websocket.Message.Receive(connection, &payload); err != nil {
			return err
		}
		var envelope struct {
			Protocol string `json:"protocol"`
			Type     string `json:"type"`
		}
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil || envelope.Protocol != deviceProtocol {
			continue
		}
		switch envelope.Type {
		case "HEARTBEAT_ACK":
			var heartbeat deviceMessage
			if err := decodeDeviceProtocol([]byte(payload), &heartbeat); err != nil {
				return fmt.Errorf("decode heartbeat lease: %w", err)
			}
			owner, token, _ := d.currentLease()
			if heartbeat.LeaseOwner != owner || heartbeat.FencingToken != token {
				return fmt.Errorf("control plane changed the device fencing token")
			}
			d.setLease(owner, token, heartbeat.LeaseExpiresAt)
		case "ACTION":
			var action deviceAction
			if err := decodeDeviceProtocol([]byte(payload), &action); err != nil {
				continue
			}
			go d.runAction(connectionCtx, writer, action)
		case "CANCEL":
			var cancel deviceCancel
			if err := decodeDeviceProtocol([]byte(payload), &cancel); err == nil {
				d.cancelAction(cancel.ActionID)
			}
		}
	}
}

func (d *deviceRuntime) setLease(owner string, token uint64, expiresAt time.Time) {
	d.mu.Lock()
	d.leaseOwner = owner
	d.fencingToken = token
	d.leaseExpiresAt = expiresAt
	d.mu.Unlock()
}

func (d *deviceRuntime) currentLease() (string, uint64, time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.leaseOwner, d.fencingToken, d.leaseExpiresAt
}

func (d *deviceRuntime) acceptsLease(action deviceAction) bool {
	owner, token, expiresAt := d.currentLease()
	if token == 0 {
		return action.FencingToken == 0 && action.LeaseOwner == ""
	}
	return action.LeaseOwner == owner && action.FencingToken == token && expiresAt.After(time.Now())
}

func (d *deviceRuntime) probeDeviceEndpoint(ctx context.Context, endpoint string) string {
	requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	url := strings.Replace(endpoint, "ws://", "http://", 1)
	url = strings.Replace(url, "wss://", "https://", 1)
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	request.Header.Set("Authorization", "Bearer "+d.token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
	detail := strings.TrimSpace(string(body))
	if response.StatusCode == http.StatusUnauthorized {
		if strings.Contains(detail, "invalid device token") {
			return "device endpoint rejected the device token (HTTP 401). Set the remote Device token to the same value as agent-runtime-client control.device_token"
		}
		if strings.Contains(detail, "device token required") {
			return "device endpoint requires a device token (HTTP 401). Configure agent-runtime-client control.device_token and enter the same token in Athena remote mode"
		}
		return "device endpoint rejected authentication (HTTP 401). Check the Athena remote Device token"
	}
	if detail != "" {
		return fmt.Sprintf("device endpoint returned HTTP %d: %s", response.StatusCode, detail)
	}
	return fmt.Sprintf("device endpoint returned HTTP %d", response.StatusCode)
}

func (d *deviceRuntime) runAction(parent context.Context, writer *deviceWriter, action deviceAction) {
	if !d.acceptsLease(action) {
		_ = writer.Send(deviceObservation{
			Protocol: deviceProtocol, Type: "OBSERVATION", ObservationID: newDeviceProtocolID("observation"),
			TaskID: action.TaskID, StepID: action.StepID, ActionID: action.ActionID, DeviceID: d.deviceID,
			LeaseOwner: action.LeaseOwner, FencingToken: action.FencingToken,
			AgentBuildID: action.AgentBuildID, RunManifestID: action.RunManifestID,
			SessionID: action.SessionID, Sequence: action.Sequence, Revision: action.Revision,
			Status: "FAILED", FinishedAt: time.Now().UTC(), ObservedAt: time.Now().UTC(), Error: "action fencing token is stale or expired",
		})
		return
	}
	ctx, cancel := context.WithCancel(parent)
	d.mu.Lock()
	if _, exists := d.inflight[action.ActionID]; exists {
		d.mu.Unlock()
		cancel()
		_ = writer.Send(deviceObservation{
			Protocol: deviceProtocol, Type: "OBSERVATION", ObservationID: newDeviceProtocolID("observation"),
			TaskID: action.TaskID, StepID: action.StepID, ActionID: action.ActionID, DeviceID: d.deviceID,
			LeaseOwner: action.LeaseOwner, FencingToken: action.FencingToken,
			AgentBuildID: action.AgentBuildID, RunManifestID: action.RunManifestID,
			SessionID: action.SessionID, Sequence: action.Sequence, Revision: action.Revision,
			Status: "FAILED", FinishedAt: time.Now().UTC(), ObservedAt: time.Now().UTC(), Error: "action is already running",
		})
		return
	}
	d.inflight[action.ActionID] = cancel
	d.mu.Unlock()
	defer func() {
		cancel()
		d.mu.Lock()
		delete(d.inflight, action.ActionID)
		d.mu.Unlock()
	}()
	observation := d.execute(ctx, action, func(progress deviceProgress) {
		progress.DeviceID = d.deviceID
		progress.LeaseOwner = action.LeaseOwner
		progress.FencingToken = action.FencingToken
		if err := writer.Send(progress); err != nil && parent.Err() == nil {
			fmt.Printf("[device-runtime] send progress %s: %v\n", action.ActionID, err)
		}
	})
	if err := writer.Send(observation); err != nil && parent.Err() == nil {
		fmt.Printf("[device-runtime] send observation %s: %v\n", action.ActionID, err)
	}
}

func (d *deviceRuntime) cancelAction(actionID string) {
	d.mu.Lock()
	cancel := d.inflight[actionID]
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

type deviceWriter struct {
	connection *websocket.Conn
	mu         sync.Mutex
}

func (w *deviceWriter) Send(value any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return websocket.JSON.Send(w.connection, value)
}

func (d *deviceRuntime) capabilities() []string {
	capabilities := []string{"app.open", "app.activate", "app.observe", "app.press", "app.type", "app.close", "file.search"}
	if d.bridge != nil && d.bridge.browser != nil {
		capabilities = append(capabilities, d.bridge.browser.Capabilities()...)
	}
	return capabilities
}

func (d *deviceRuntime) capabilityInstances() []map[string]any {
	capabilities := d.capabilities()
	instances := make([]map[string]any, 0, len(capabilities))
	for _, capability := range capabilities {
		instances = append(instances, map[string]any{
			"instance_id": d.capabilityInstanceID(capability),
			"capability":  capability,
			"version":     "0.2",
		})
	}
	return instances
}

func (d *deviceRuntime) capabilityInstanceID(capability string) string {
	return d.deviceID + ":" + strings.ReplaceAll(capability, ".", "-")
}

func (d *deviceRuntime) execute(ctx context.Context, action deviceAction, progress func(deviceProgress)) (result deviceObservation) {
	if strings.TrimSpace(action.TraceID) != "" {
		ctx = log.WithReqID(ctx, action.TraceID)
	}
	startedAt := time.Now().UTC()
	executionSpan := log.StartSpan(ctx, "device.execute",
		"task_id", action.TaskID,
		"action_id", action.ActionID,
		"device_id", d.deviceID,
		"capability", action.Capability,
	)
	base := deviceObservation{
		Protocol: deviceProtocol, Type: "OBSERVATION", ObservationID: newDeviceProtocolID("observation"),
		TaskID: action.TaskID, StepID: action.StepID, ActionID: action.ActionID, DeviceID: d.deviceID,
		LeaseOwner: action.LeaseOwner, FencingToken: action.FencingToken,
		TraceID: action.TraceID, AgentBuildID: action.AgentBuildID, RunManifestID: action.RunManifestID,
		SessionID: action.SessionID, Sequence: action.Sequence, Revision: action.Revision,
		StartedAt: startedAt, ObservedAt: startedAt,
	}
	defer func() {
		finishedAt := time.Now().UTC()
		result.FinishedAt = finishedAt
		result.ObservedAt = finishedAt
		executionSpan.End(deviceObservationError(result),
			"outcome_status", result.Status,
			"session_id", result.SessionID,
			"evidence_count", len(result.Evidence),
			"attachment_count", len(result.Attachments),
		)
	}()
	if err := action.Validate(); err != nil {
		base.Status, base.Error = "FAILED", "invalid action envelope: "+err.Error()
		return base
	}
	if !d.acceptsLease(action) {
		base.Status, base.Error = "FAILED", "action fencing token is stale or expired"
		return base
	}
	if action.CapabilityInstanceID != "" && action.CapabilityInstanceID != d.capabilityInstanceID(action.Capability) {
		base.Status, base.Error = "FAILED", "capability instance does not belong to this device runtime"
		return base
	}
	action.Policy.Risk = raiseDeviceRisk(action.Policy.Risk, minimumDeviceRisk(action))
	if action.Deadline.IsZero() || time.Now().After(action.Deadline) {
		base.Status, base.Error = "EXPIRED", "action deadline has expired"
		return base
	}
	d.mu.Lock()
	if completed, ok := d.completed[action.IdempotencyKey]; ok {
		d.mu.Unlock()
		completed.DeviceID = d.deviceID
		completed.LeaseOwner = action.LeaseOwner
		completed.FencingToken = action.FencingToken
		return completed
	}
	d.mu.Unlock()
	if err := d.beginDurableAction(action); err != nil {
		base.Status, base.Error = "FAILED", err.Error()
		return base
	}
	if action.Policy.Decision == "BLOCK" {
		base.Status, base.Error = "BLOCKED", "action is blocked by policy"
		if err := d.remember(action, base); err != nil {
			base.Status, base.Error = "FAILED", "blocked outcome could not be persisted: "+err.Error()
		}
		return base
	}
	if action.Policy.Decision == "ASK_USER" {
		base.Status, base.Error = "WAITING_APPROVAL", "desktop approval is required"
		if err := d.releaseDurableAction(action); err != nil {
			base.Status, base.Error = "FAILED", "approval deferral could not be persisted: "+err.Error()
		}
		return base
	}
	var perceptionSpan *log.Span
	if strings.HasPrefix(action.Capability, "browser.") {
		perceptionSpan = log.StartSpan(ctx, "perception.observe",
			"task_id", action.TaskID,
			"action_id", action.ActionID,
			"capability", action.Capability,
			"requested_session_id", action.SessionID,
		)
		defer func() {
			perceptionSpan.End(deviceObservationError(result),
				"outcome_status", result.Status,
				"session_id", result.SessionID,
				"state_field_count", len(result.State),
				"evidence_count", len(result.Evidence),
			)
		}()
	}
	state, sessionID, err := d.executeCapability(ctx, action, progress)
	base.SessionID = sessionID
	base.State = state
	if err == nil && strings.HasPrefix(action.Capability, "browser.") && d.bridge != nil {
		attachments, attachmentErr := collectObservationAttachments(d.bridge.home, state)
		redactObservationAttachmentPaths(state)
		if attachmentErr != nil {
			if base.State == nil {
				base.State = make(map[string]any)
			}
			base.State["attachment_transport"] = map[string]any{"available": false, "error": attachmentErr.Error()}
		} else if len(attachments) > 0 {
			base.Attachments = attachments
			base.State["attachment_transport"] = map[string]any{
				"available": true, "count": len(attachments), "protocol": deviceProtocol,
			}
		}
	}
	if ctx.Err() != nil {
		base.Status, base.Error = "CANCELLED", ctx.Err().Error()
	} else if browserUserInterventionDetected(state) {
		base.Status, base.Error = "WAITING_USER", browserUserInterventionMessage(state)
	} else if err != nil {
		base.Status, base.Error = "FAILED", err.Error()
	} else if boolArgument(action.Arguments["user_takeover"]) {
		base.Status = "WAITING_USER"
	} else {
		base.Status = "SUCCEEDED"
	}
	if err := d.remember(action, base); err != nil {
		base.Status = "FAILED"
		base.Error = "action outcome could not be persisted: " + err.Error()
		if base.State == nil {
			base.State = make(map[string]any)
		}
		base.State["verification_required"] = true
		base.State["journal_persisted"] = false
	}
	return base
}

func deviceObservationError(observation deviceObservation) error {
	switch observation.Status {
	case "FAILED", "CANCELLED", "EXPIRED":
		if strings.TrimSpace(observation.Error) != "" {
			return fmt.Errorf("%s", observation.Error)
		}
		return fmt.Errorf("device observation ended with status %s", observation.Status)
	default:
		return nil
	}
}

func minimumDeviceRisk(action deviceAction) string {
	switch action.Capability {
	case "app.close", "browser.click", "browser.play", "browser.pause", "browser.type", "browser.press", "browser.download", "browser.close":
		return deviceRiskReversible
	default:
		return deviceRiskReadOnly
	}
}

func raiseDeviceRisk(serverRisk, localRisk string) string {
	rank := map[string]int{deviceRiskReadOnly: 0, deviceRiskReversible: 1, deviceRiskExternalWrite: 2, deviceRiskSensitive: 3}
	if rank[localRisk] > rank[serverRisk] {
		return localRisk
	}
	return serverRisk
}

func browserChallengeDetected(state map[string]any) bool {
	detected, _ := state["challenge_detected"].(bool)
	return detected
}

func browserChallengeMessage(state map[string]any) string {
	challenge, _ := state["challenge"].(map[string]any)
	if challenge != nil {
		if message, _ := challenge["message"].(string); strings.TrimSpace(message) != "" {
			return message
		}
	}
	return "browser challenge detected; user takeover is required"
}

func browserUserInterventionDetected(state map[string]any) bool {
	if state == nil {
		return false
	}
	if required, _ := state["user_intervention_required"].(bool); required {
		return true
	}
	return browserChallengeDetected(state)
}

func browserUserInterventionMessage(state map[string]any) string {
	if intervention, _ := state["intervention"].(map[string]any); intervention != nil {
		if message, _ := intervention["message"].(string); strings.TrimSpace(message) != "" {
			return message
		}
	}
	return browserChallengeMessage(state)
}

func validDevicePolicy(policy devicePolicy) bool {
	riskValid := policy.Risk == deviceRiskReadOnly || policy.Risk == deviceRiskReversible || policy.Risk == deviceRiskExternalWrite || policy.Risk == deviceRiskSensitive
	decisionValid := policy.Decision == "ALLOW" || policy.Decision == "ASK_USER" || policy.Decision == "BLOCK"
	return riskValid && decisionValid
}

func (d *deviceRuntime) executeCapability(ctx context.Context, action deviceAction, progress func(deviceProgress)) (map[string]any, string, error) {
	value := func(key string) string {
		text, _ := action.Arguments[key].(string)
		return strings.TrimSpace(text)
	}
	switch action.Capability {
	case "browser.task":
		arguments := action.Arguments
		if arguments == nil {
			arguments = map[string]any{}
		}
		forceNewSession := boolArgument(arguments["new_session"]) || boolArgument(arguments["newSession"]) || boolArgument(arguments["isolated"])
		sessionID, err := d.bridge.browserSession(action.SessionID, true, forceNewSession, browserSessionTargetKey(arguments))
		if err != nil {
			return nil, action.SessionID, err
		}
		result, err := d.bridge.browser.RunTask(ctx, browser_runtime.TaskRequest{
			RequestID: action.ActionID, SessionID: sessionID, Goal: value("goal"), Target: value("target"), Query: value("query"),
			ContextualMediaTitle: boolArgument(arguments["contextual_media_title"]),
			Progress: func(update browser_runtime.Progress) {
				if progress == nil {
					return
				}
				progress(deviceProgress{
					Protocol: deviceProtocol, Type: "PROGRESS", TaskID: action.TaskID, ActionID: action.ActionID,
					StepID: action.StepID, TraceID: action.TraceID, DeviceID: d.deviceID, SessionID: sessionID,
					Sequence: action.Sequence, Revision: action.Revision, Stage: update.Stage, Message: update.Message,
					Progress: update.Progress, Bytes: update.Bytes, Total: update.Total, State: update.State, SentAt: time.Now().UTC(),
				})
			},
		})
		return result, sessionID, err
	case "browser.automation":
		operation := strings.ToLower(value("operation"))
		sessionID := action.SessionID
		if operation == "create" {
			var err error
			sessionID, err = d.bridge.browserSession(action.SessionID, false, false, "")
			if err != nil {
				return nil, action.SessionID, err
			}
		}
		result, err := d.bridge.browser.ManageAutomation(ctx, browser_runtime.AutomationRequest{
			Operation: operation, SessionID: sessionID, AutomationID: value("automation_id"), TabID: value("tab_id"),
			Trigger: browser_runtime.BrowserAutomationTrigger{
				Type: value("trigger_type"), Target: browser_runtime.BrowserAutomationSelector{
					Role: value("trigger_role"), Name: value("trigger_name"), Kind: value("trigger_kind"), URLContains: value("trigger_url_contains"),
				},
			},
			Action: browser_runtime.BrowserAutomationAction{
				Type: value("action_type"), Value: value("action_value"), URL: value("action_url"),
				Target: browser_runtime.BrowserAutomationSelector{
					Role: value("action_role"), Name: value("action_name"), Kind: value("action_kind"), URLContains: value("action_url_contains"),
				},
			},
			Verification: browser_runtime.BrowserAutomationVerification{
				Type: value("verification_type"), Target: browser_runtime.BrowserAutomationSelector{
					Role: value("verification_role"), Name: value("verification_name"), Kind: value("verification_kind"), URLContains: value("verification_url_contains"),
				},
			},
			CooldownMS: intArgument(action.Arguments["cooldown_ms"]),
		})
		return result, sessionID, err
	case "browser.open", "browser.navigate", "browser.click", "browser.play", "browser.pause", "browser.type", "browser.hover", "browser.select", "browser.drag", "browser.press", "browser.scroll", "browser.back", "browser.forward", "browser.refresh", "browser.wait", "browser.download", "browser.screenshot", "browser.close", "browser.observe":
		arguments := action.Arguments
		if arguments == nil {
			arguments = map[string]any{}
		}
		createSession := action.Capability == "browser.open" || action.Capability == "browser.navigate"
		forceNewSession := boolArgument(arguments["new_session"]) || boolArgument(arguments["newSession"]) || boolArgument(arguments["isolated"])
		sessionID, err := d.bridge.browserSession(action.SessionID, createSession, forceNewSession, browserSessionTargetKey(arguments))
		if err != nil {
			return nil, action.SessionID, err
		}
		browserAction := strings.TrimPrefix(action.Capability, "browser.")
		if browserAction == "open" {
			browserAction = "navigate"
		}
		if browserAction == "observe" {
			browserAction = "extract"
		}
		if action.Capability == "browser.open" {
			arguments["headed"] = true
			arguments["open_mode"] = "tab"
		}
		result, err := d.bridge.browser.RunAction(ctx, browser_runtime.Request{
			RequestID: action.ActionID, SessionID: sessionID, Action: browserAction, Arguments: arguments,
			RiskLevel: action.Policy.Risk, Decision: action.Policy.Decision, UserTakeover: boolArgument(arguments["user_takeover"]), Approved: true,
			Progress: func(update browser_runtime.Progress) {
				if progress == nil {
					return
				}
				progress(deviceProgress{
					Protocol: deviceProtocol, Type: "PROGRESS", TaskID: action.TaskID, ActionID: action.ActionID,
					StepID: action.StepID, TraceID: action.TraceID, DeviceID: d.deviceID, SessionID: sessionID,
					Sequence: action.Sequence, Revision: action.Revision, Stage: update.Stage, Message: update.Message,
					Progress: update.Progress, Bytes: update.Bytes, Total: update.Total, State: update.State, SentAt: time.Now().UTC(),
				})
			},
		})
		if action.Capability == "browser.close" && err == nil {
			d.bridge.clearBrowserSession(sessionID)
		}
		return result, sessionID, err
	case "app.open":
		application := value("application")
		if err := validateDesktopApplication(application); err != nil {
			return nil, action.SessionID, err
		}
		if err := launchDesktopApplication(ctx, application); err != nil {
			return nil, action.SessionID, err
		}
		sessionID := action.SessionID
		if sessionID == "" {
			sessionID = newDeviceAppSessionID()
		}
		processName := application
		time.Sleep(350 * time.Millisecond)
		if active, err := activeDesktopApplication(ctx); err == nil && active != "" {
			processName = active
		}
		d.bridge.sessions.Store(sessionID, desktopSession{Application: application, ProcessName: processName})
		return map[string]any{"application": application, "process_name": processName}, sessionID, nil
	case "app.activate", "app.observe", "app.press", "app.type", "app.close":
		stored, ok := d.bridge.sessions.Load(action.SessionID)
		if !ok {
			return nil, action.SessionID, fmt.Errorf("application session is unavailable")
		}
		operation := strings.TrimPrefix(action.Capability, "app.")
		if operation == "type" {
			operation = "type_text"
		}
		if operation == "close" {
			operation = "close_application"
		}
		state, err := runDesktopSessionAction(ctx, stored.(desktopSession), operation, value("value"))
		if err == nil && action.Capability == "app.close" {
			d.bridge.sessions.Delete(action.SessionID)
		}
		return state, action.SessionID, err
	case "file.search":
		request := desktopSearchRequest{
			Roots: d.bridge.authorizedRoots(), Query: value("query"), Mode: value("mode"),
			Extensions: stringSliceArgument(action.Arguments["extensions"]), MaxResults: intArgument(action.Arguments["max_results"]),
			IncludeHidden: boolArgument(action.Arguments["include_hidden"]),
		}
		if request.Query == "" {
			return nil, action.SessionID, fmt.Errorf("query is required")
		}
		if len(request.Roots) == 0 {
			return map[string]any{"status": "authorization_required"}, action.SessionID, fmt.Errorf("no local folder is authorized")
		}
		if request.Mode == "" {
			request.Mode = "both"
		}
		limit := request.MaxResults
		if limit <= 0 || limit > desktopSearchMaxResults {
			limit = 50
		}
		matches, truncated, err := searchDesktopFiles(ctx, request.Roots, request, limit)
		return map[string]any{"matches": matches, "count": len(matches), "truncated": truncated}, action.SessionID, err
	default:
		return nil, action.SessionID, fmt.Errorf("unsupported capability %q", action.Capability)
	}
}

func browserSessionTargetKey(arguments map[string]any) string {
	if arguments == nil {
		return ""
	}
	for _, key := range []string{"target", "url", "query", "goal", "engine"} {
		if value, ok := arguments[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stringSliceArgument(value any) []string {
	items, ok := value.([]any)
	if !ok {
		if strings, ok := value.([]string); ok {
			return strings
		}
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func intArgument(value any) int {
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	default:
		return 0
	}
}

func boolArgument(value any) bool {
	result, _ := value.(bool)
	return result
}

func newDeviceBrowserSessionID() string { return "athena-" + randomDeviceHex(16) }
func newDeviceAppSessionID() string     { return "desktop-" + randomDeviceHex(12) }

func randomDeviceHex(size int) string {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return strings.Repeat("0", size*2)
	}
	return hex.EncodeToString(value)
}
