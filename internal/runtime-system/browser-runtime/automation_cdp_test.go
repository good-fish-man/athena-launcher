package browser_runtime

import "testing"

func TestParseBrowserCDPEndpoint(t *testing.T) {
	for _, input := range []string{
		`ws://127.0.0.1:9222/devtools/page/abc`,
		`{"success":true,"data":{"cdpUrl":"ws://localhost:9222/devtools/page/abc"}}`,
	} {
		endpoint, ok := parseBrowserCDPEndpoint(input)
		if !ok || endpoint == "" {
			t.Fatalf("endpoint not parsed from %q", input)
		}
	}
	if _, ok := parseBrowserCDPEndpoint(`ws://remote.example/devtools/page/abc`); ok {
		t.Fatal("non-local CDP endpoint was accepted")
	}
}

func TestBrowserCDPObservationEvent(t *testing.T) {
	if !browserCDPObservationEvent(browserCDPMessage{Method: "Page.frameNavigated"}) {
		t.Fatal("navigation event was ignored")
	}
	if !browserCDPObservationEvent(browserCDPMessage{
		Method: "Runtime.consoleAPICalled",
		Params: map[string]any{"args": []any{map[string]any{"value": "__ATHENA_BROWSER_EVENT_V3__:media_ended"}}},
	}) {
		t.Fatal("injected media event was ignored")
	}
	if browserCDPObservationEvent(browserCDPMessage{Method: "Network.dataReceived"}) {
		t.Fatal("unrelated high-volume event triggered an observation")
	}
}
