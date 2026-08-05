package main

import (
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

func TestDeviceRuntimeRejectsExpiredAction(t *testing.T) {
	runtime := &deviceRuntime{completed: make(map[string]deviceObservation), inflight: make(map[string]context.CancelFunc)}
	observation := runtime.execute(context.Background(), deviceAction{
		TaskID: "task", ActionID: "action", Sequence: 1, IdempotencyKey: "task:1",
		Capability: "browser.open", Deadline: time.Now().Add(-time.Second), Policy: devicePolicy{Risk: "LOW", Decision: "ALLOW"},
	}, nil)
	if observation.Status != "EXPIRED" {
		t.Fatalf("unexpected observation: %+v", observation)
	}
}

func TestDeviceRuntimeReportsCancelledAction(t *testing.T) {
	runtime := &deviceRuntime{completed: make(map[string]deviceObservation), inflight: make(map[string]context.CancelFunc)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	observation := runtime.execute(ctx, deviceAction{
		TaskID: "task", ActionID: "action", Sequence: 1, IdempotencyKey: "task:1",
		Capability: "unsupported", Deadline: time.Now().Add(time.Second), Policy: devicePolicy{Risk: "LOW", Decision: "ALLOW"},
	}, nil)
	if observation.Status != "CANCELLED" {
		t.Fatalf("unexpected observation: %+v", observation)
	}
}

func TestDeviceRuntimeRejectsOutOfOrderAction(t *testing.T) {
	runtime := &deviceRuntime{completed: make(map[string]deviceObservation), inflight: make(map[string]context.CancelFunc), sequences: make(map[string]int64)}
	observation := runtime.execute(context.Background(), deviceAction{
		TaskID: "task", ActionID: "action-2", Sequence: 2, IdempotencyKey: "task:2",
		Capability: "app.open", Deadline: time.Now().Add(time.Second), Policy: devicePolicy{Risk: "LOW", Decision: "BLOCK"},
	}, nil)
	if observation.Status != "FAILED" {
		t.Fatalf("unexpected observation: %+v", observation)
	}
}

func TestDeviceRuntimeDeduplicatesBlockedAction(t *testing.T) {
	runtime := &deviceRuntime{completed: make(map[string]deviceObservation), inflight: make(map[string]context.CancelFunc), sequences: make(map[string]int64)}
	action := deviceAction{
		TaskID: "task", ActionID: "action-1", Sequence: 1, IdempotencyKey: "task:1",
		Capability: "app.open", Deadline: time.Now().Add(time.Second), Policy: devicePolicy{Risk: "LOW", Decision: "BLOCK"},
	}
	first := runtime.execute(context.Background(), action, nil)
	second := runtime.execute(context.Background(), action, nil)
	if first.Status != "BLOCKED" || second.Status != first.Status || second.Error != first.Error {
		t.Fatalf("observations were not deduplicated: first=%+v second=%+v", first, second)
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
	for _, capability := range []string{"browser.open", "browser.observe", "browser.click", "browser.type", "browser.scroll", "browser.wait", "browser.download", "browser.screenshot"} {
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
	if !browserSessionPattern.MatchString(youtube) {
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
