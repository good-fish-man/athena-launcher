package browser_runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestAuthenticatedChromeArgsUsePersistentNonDefaultProfile(t *testing.T) {
	profile := filepath.Join(t.TempDir(), "authenticated-profile")
	args := authenticatedChromeArgs(profile)
	for _, expected := range []string{
		"--remote-debugging-port=9222",
		"--remote-debugging-address=127.0.0.1",
		"--user-data-dir=" + profile,
		"https://accounts.google.com/",
	} {
		if !containsAuthenticatedChromeArg(args, expected) {
			t.Fatalf("authenticated Chrome args missing %q: %v", expected, args)
		}
	}
	for _, forbidden := range []string{"--disable-sync", "--use-mock-keychain", "--password-store=basic"} {
		if containsAuthenticatedChromeArg(args, forbidden) {
			t.Fatalf("authenticated Chrome args contain login-breaking flag %q: %v", forbidden, args)
		}
	}
}

func TestAuthenticatedChromeResultUsesAthenaHome(t *testing.T) {
	home := t.TempDir()
	result := authenticatedChromeResult(home)
	if result.Profile != filepath.Join(home, "browser", "authenticated-profile") {
		t.Fatalf("profile = %q", result.Profile)
	}
	if result.LogPath != filepath.Join(home, "logs", "browser-auth.log") || result.DebugURL != "http://127.0.0.1:9222" {
		t.Fatalf("result = %#v", result)
	}
}

func TestBrowserCDPReadyRequiresWebSocketEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(response, `{"webSocketDebuggerUrl":"ws://127.0.0.1/devtools/browser/test"}`)
	}))
	defer server.Close()
	if !browserCDPReady(context.Background(), server.URL) {
		t.Fatal("valid CDP version endpoint was not detected")
	}

	invalid := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, _ = fmt.Fprint(response, `{}`)
	}))
	defer invalid.Close()
	if browserCDPReady(context.Background(), invalid.URL) {
		t.Fatal("endpoint without a websocket debugger URL was accepted")
	}
}

func containsAuthenticatedChromeArg(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
