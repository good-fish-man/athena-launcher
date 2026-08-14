package deployment

import (
	browser_runtime "athena-launcher/internal/runtime-system/browser-runtime"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestDeviceWebSocketURL(t *testing.T) {
	for _, test := range []struct {
		selection deploymentSelection
		want      string
	}{
		{deploymentSelection{Mode: connectionModeLocal}, "ws://127.0.0.1:8090/v1/control/device"},
		{deploymentSelection{Mode: connectionModeRemote, RemoteURL: "https://athena.example.com"}, "wss://athena.example.com/v1/control/device"},
	} {
		got, err := deviceWebSocketURL(test.selection)
		if err != nil || got != test.want {
			t.Fatalf("deviceWebSocketURL() = %q, %v; want %q", got, err, test.want)
		}
	}
}

func TestDeviceWebSocketURLsIncludePublicPrefixFallback(t *testing.T) {
	got, err := deviceWebSocketURLs(deploymentSelection{Mode: connectionModeLocal})
	want := []string{
		"ws://127.0.0.1:8090/v1/control/device",
		"ws://127.0.0.1:8090/api/agent-runtime-client/v1/control/device",
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("deviceWebSocketURLs() = %#v, %v; want %#v", got, err, want)
	}
}

func TestDeviceWebSocketURLsUseRemoteBasePathFirst(t *testing.T) {
	got, err := deviceWebSocketURLs(deploymentSelection{Mode: connectionModeRemote, RemoteURL: "https://athena.example.com/api/agent-runtime-client/v1"})
	want := []string{
		"wss://athena.example.com/api/agent-runtime-client/v1/control/device",
		"wss://athena.example.com/v1/control/device",
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("deviceWebSocketURLs() = %#v, %v; want %#v", got, err, want)
	}
}

func TestDeviceRuntimeTokenUsesRemoteTokenWhenConfigured(t *testing.T) {
	got := deviceRuntimeToken(&launcherState{
		ConnectionMode:       connectionModeRemote,
		RemoteDeviceToken:    "remote-secret",
		InternalServiceToken: "internal-secret",
	})
	if got != "remote-secret" {
		t.Fatalf("deviceRuntimeToken() = %q, want remote token", got)
	}
}

func testDeviceAction(capability string) deviceAction {
	now := time.Now().UTC()
	return deviceAction{
		Protocol: deviceProtocol, Type: "ACTION", TaskID: "task", StepID: "step-1", ActionID: "action-1",
		TraceID: "trace-device-1", AgentBuildID: "build-1", RunManifestID: "manifest-1",
		Sequence: 1, Revision: 1, IdempotencyKey: "task:step-1:action-1", IssuedAt: now,
		Deadline: now.Add(time.Second), Capability: capability,
		Policy: devicePolicy{Risk: deviceRiskReadOnly, Decision: "ALLOW"},
	}
}

func TestDeviceRuntimeRejectsExpiredAction(t *testing.T) {
	runtime := &deviceRuntime{completed: make(map[string]deviceObservation), inflight: make(map[string]context.CancelFunc)}
	action := testDeviceAction("browser.open")
	action.IssuedAt = time.Now().Add(-2 * time.Second)
	action.Deadline = time.Now().Add(-time.Second)
	observation := runtime.execute(context.Background(), action, nil)
	if observation.Status != "EXPIRED" {
		t.Fatalf("unexpected observation: %+v", observation)
	}
	if observation.TraceID != action.TraceID {
		t.Fatalf("observation trace_id = %q, want %q", observation.TraceID, action.TraceID)
	}
	if observation.AgentBuildID != action.AgentBuildID || observation.RunManifestID != action.RunManifestID {
		t.Fatalf("observation deployment provenance = %+v", observation)
	}
}

func TestDeviceRuntimeReportsCancelledAction(t *testing.T) {
	runtime := &deviceRuntime{completed: make(map[string]deviceObservation), inflight: make(map[string]context.CancelFunc)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	observation := runtime.execute(ctx, testDeviceAction("unsupported"), nil)
	if observation.Status != "CANCELLED" {
		t.Fatalf("unexpected observation: %+v", observation)
	}
}

func TestDeviceRuntimeRejectsOutOfOrderAction(t *testing.T) {
	runtime := &deviceRuntime{completed: make(map[string]deviceObservation), inflight: make(map[string]context.CancelFunc), sequences: make(map[string]int64)}
	action := testDeviceAction("app.open")
	action.ActionID, action.StepID, action.Sequence, action.IdempotencyKey = "action-2", "step-2", 2, "task:step-2:action-2"
	action.Policy.Decision = "BLOCK"
	observation := runtime.execute(context.Background(), action, nil)
	if observation.Status != "FAILED" {
		t.Fatalf("unexpected observation: %+v", observation)
	}
}

func TestDeviceRuntimeDeduplicatesBlockedAction(t *testing.T) {
	runtime := &deviceRuntime{completed: make(map[string]deviceObservation), inflight: make(map[string]context.CancelFunc), sequences: make(map[string]int64)}
	action := testDeviceAction("app.open")
	action.Policy.Decision = "BLOCK"
	first := runtime.execute(context.Background(), action, nil)
	second := runtime.execute(context.Background(), action, nil)
	if first.Status != "BLOCKED" || second.Status != first.Status || second.Error != first.Error {
		t.Fatalf("observations were not deduplicated: first=%+v second=%+v", first, second)
	}
}

func TestDeviceRuntimeDoesNotCachePendingApproval(t *testing.T) {
	runtime := &deviceRuntime{
		completed: make(map[string]deviceObservation), inflight: make(map[string]context.CancelFunc),
		durable: make(map[string]journalAction), sequences: make(map[string]int64),
	}
	action := testDeviceAction("app.open")
	action.Policy.Decision = "ASK_USER"
	waiting := runtime.execute(context.Background(), action, nil)
	if waiting.Status != "WAITING_APPROVAL" {
		t.Fatalf("unexpected approval observation: %+v", waiting)
	}
	if _, cached := runtime.completed[action.IdempotencyKey]; cached {
		t.Fatal("pending approval was cached as a completed outcome")
	}
	if _, pending := runtime.durable[action.IdempotencyKey]; pending {
		t.Fatal("pending approval retained an in-flight journal entry")
	}
	if runtime.sequences[action.TaskID] != 0 {
		t.Fatalf("pending approval advanced sequence to %d", runtime.sequences[action.TaskID])
	}
	action.Policy.Decision = "BLOCK"
	blocked := runtime.execute(context.Background(), action, nil)
	if blocked.Status != "BLOCKED" {
		t.Fatalf("same idempotency key could not continue after decision: %+v", blocked)
	}
}

func TestDeviceRuntimeCapabilityInstancesAreStableAndUnique(t *testing.T) {
	runtime := &deviceRuntime{deviceID: "device-1", bridge: newDesktopBridge(t.TempDir(), nil)}
	instances := runtime.capabilityInstances()
	seen := make(map[string]string, len(instances))
	for _, instance := range instances {
		instanceID, _ := instance["instance_id"].(string)
		capability, _ := instance["capability"].(string)
		if instanceID == "" || capability == "" {
			t.Fatalf("invalid capability instance: %#v", instance)
		}
		if previous, exists := seen[instanceID]; exists {
			t.Fatalf("capabilities %q and %q share instance %q", previous, capability, instanceID)
		}
		seen[instanceID] = capability
		if want := runtime.capabilityInstanceID(capability); instanceID != want {
			t.Fatalf("instance for %q = %q, want %q", capability, instanceID, want)
		}
	}
}

func TestDeviceRuntimeRejectsForeignCapabilityInstance(t *testing.T) {
	runtime := &deviceRuntime{
		deviceID: "device-1", completed: make(map[string]deviceObservation),
		inflight: make(map[string]context.CancelFunc), sequences: make(map[string]int64),
	}
	action := testDeviceAction("app.open")
	action.CapabilityInstanceID = "device-2:app-open"
	action.Policy.Decision = "BLOCK"
	observation := runtime.execute(context.Background(), action, nil)
	if observation.Status != "FAILED" || observation.Error != "capability instance does not belong to this device runtime" {
		t.Fatalf("unexpected observation: %+v", observation)
	}
}

func TestDeviceRuntimeRaisesButNeverLowersRisk(t *testing.T) {
	click := testDeviceAction("browser.click")
	if got := raiseDeviceRisk(click.Policy.Risk, minimumDeviceRisk(click)); got != deviceRiskReversible {
		t.Fatalf("browser click risk = %q, want %q", got, deviceRiskReversible)
	}
	click.Policy.Risk = deviceRiskSensitive
	if got := raiseDeviceRisk(click.Policy.Risk, minimumDeviceRisk(click)); got != deviceRiskSensitive {
		t.Fatalf("sensitive server risk was lowered to %q", got)
	}
}

func TestNewDeviceRuntimeRepairsRecoveredObservationDeviceID(t *testing.T) {
	home := t.TempDir()
	bridge := newDesktopBridge(home, nil)
	journalPath := filepath.Join(home, "data", "device-action-journal-v4.json")
	now := time.Now().UTC()
	journal := deviceActionJournal{
		Protocol: deviceProtocol,
		Completed: map[string]deviceObservation{
			"task:step:action": {
				Protocol: deviceProtocol, Type: "OBSERVATION", ObservationID: "observation-1",
				TaskID: "task", StepID: "step", ActionID: "action", Sequence: 1, Revision: 1,
				Status: "FAILED", FinishedAt: now, ObservedAt: now,
			},
		},
		InFlight: make(map[string]journalAction), Sequences: map[string]int64{"task": 1},
	}
	if err := saveDeviceActionJournal(journalPath, journal); err != nil {
		t.Fatal(err)
	}
	runtime, err := newDeviceRuntime(&launcherState{DeviceID: "device-1", ConnectionMode: connectionModeLocal}, bridge)
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.completed["task:step:action"].DeviceID; got != "device-1" {
		t.Fatalf("recovered observation device_id = %q", got)
	}
	reloaded, err := loadDeviceActionJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Completed["task:step:action"].DeviceID; got != "device-1" {
		t.Fatalf("repaired device_id was not persisted: %q", got)
	}
}

func TestDeviceRuntimeRejectsStaleFencingToken(t *testing.T) {
	runtime := &deviceRuntime{}
	runtime.setLease("control-current", 9, time.Now().Add(time.Minute))
	if runtime.acceptsLease(deviceAction{LeaseOwner: "control-old", FencingToken: 8}) {
		t.Fatal("stale control-plane lease was accepted")
	}
	if !runtime.acceptsLease(deviceAction{LeaseOwner: "control-current", FencingToken: 9}) {
		t.Fatal("current control-plane lease was rejected")
	}
	runtime.setLease("control-current", 9, time.Now().Add(-time.Second))
	if runtime.acceptsLease(deviceAction{LeaseOwner: "control-current", FencingToken: 9}) {
		t.Fatal("expired control-plane lease was accepted")
	}
}

func TestBrowserChallengeObservationRequiresUser(t *testing.T) {
	state := map[string]any{
		"challenge_detected": true,
		"challenge": map[string]any{
			"message": "Google blocked the automated browser with an unusual-traffic verification page.",
		},
	}
	if !browserChallengeDetected(state) {
		t.Fatal("challenge state was not detected")
	}
	if got := browserChallengeMessage(state); got == "" || got == "browser challenge detected; user takeover is required" {
		t.Fatalf("challenge message was not preserved: %q", got)
	}
}

func TestBrowserAuthenticationObservationRequiresUser(t *testing.T) {
	state := map[string]any{
		"user_intervention_required": true,
		"intervention": map[string]any{
			"kind":    "authentication_required",
			"message": "Sign in in the visible browser, then continue.",
		},
	}
	if !browserUserInterventionDetected(state) {
		t.Fatal("authentication intervention was not detected")
	}
	if got := browserUserInterventionMessage(state); got != "Sign in in the visible browser, then continue." {
		t.Fatalf("intervention message = %q", got)
	}
}

func TestDeviceRuntimeBrowserCapabilitiesReflectInstalledController(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	fallbackRuntime := &deviceRuntime{bridge: newDesktopBridge(t.TempDir(), nil)}
	fallbackCapabilities := fallbackRuntime.capabilities()
	if !hasCapability(fallbackCapabilities, "browser.open") || !hasCapability(fallbackCapabilities, "browser.navigate") {
		t.Fatalf("fallback browser capabilities missing open/navigate: %v", fallbackCapabilities)
	}
	if hasCapability(fallbackCapabilities, "browser.click") || hasCapability(fallbackCapabilities, "browser.type") {
		t.Fatalf("fallback should not advertise semantic browser control: %v", fallbackCapabilities)
	}

	home := t.TempDir()
	name := "agent-browser"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	browserDir := filepath.Join(home, "browser", "0.33.1")
	if err := os.MkdirAll(browserDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(browserDir, name), []byte("fake"), 0o700); err != nil {
		t.Fatal(err)
	}
	fullRuntime := &deviceRuntime{bridge: newDesktopBridge(home, nil)}
	fullCapabilities := fullRuntime.capabilities()
	for _, capability := range []string{"browser.task", "browser.open", "browser.observe", "browser.click", "browser.play", "browser.type", "browser.hover", "browser.select", "browser.drag", "browser.scroll", "browser.back", "browser.forward", "browser.refresh", "browser.wait", "browser.download", "browser.screenshot"} {
		if !hasCapability(fullCapabilities, capability) {
			t.Fatalf("full browser capability %q missing from %v", capability, fullCapabilities)
		}
	}
}

func TestDesktopBridgeReusesActiveBrowserSession(t *testing.T) {
	bridge := newDesktopBridge(t.TempDir(), nil)
	first, err := bridge.browserSession("", true, false, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	second, err := bridge.browserSession("", true, false, "https://www.youtube.com")
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || second != first {
		t.Fatalf("browser session was not reused: first=%q second=%q", first, second)
	}
}

func TestDesktopBridgeReusesOneBrowserSessionForDifferentTargets(t *testing.T) {
	bridge := newDesktopBridge(t.TempDir(), nil)
	youtube, err := bridge.browserSession("server-random-1", true, false, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	qqMusic, err := bridge.browserSession("server-random-2", true, false, "QQ Music")
	if err != nil {
		t.Fatal(err)
	}
	if youtube != qqMusic {
		t.Fatalf("different browser targets should share one browser session: youtube=%q qq=%q", youtube, qqMusic)
	}
	if !browser_runtime.IsValidSessionID(youtube) {
		t.Fatalf("shared target session must be a valid Athena browser ID: %q", youtube)
	}
}

func TestDesktopBridgeCanForceNewBrowserSession(t *testing.T) {
	bridge := newDesktopBridge(t.TempDir(), nil)
	first, err := bridge.browserSession("", true, false, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	second, err := bridge.browserSession("", true, true, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	if first == "" || second == "" || second == first {
		t.Fatalf("force new browser session did not rotate: first=%q second=%q", first, second)
	}
}

func TestDesktopBridgeRequiresExistingBrowserSessionForInteractions(t *testing.T) {
	bridge := newDesktopBridge(t.TempDir(), nil)
	if _, err := bridge.browserSession("", false, false, ""); err == nil {
		t.Fatal("interaction without an active browser session was accepted")
	}
	created, err := bridge.browserSession("", true, false, "YouTube")
	if err != nil {
		t.Fatal(err)
	}
	reused, err := bridge.browserSession("", false, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if reused != created {
		t.Fatalf("active session was not reused for interaction: created=%q reused=%q", created, reused)
	}
	bridge.clearBrowserSession(created)
	if _, err := bridge.browserSession("", false, false, ""); err == nil {
		t.Fatal("closed browser session was still reused")
	}
}

func hasCapability(capabilities []string, want string) bool {
	for _, capability := range capabilities {
		if capability == want {
			return true
		}
	}
	return false
}
