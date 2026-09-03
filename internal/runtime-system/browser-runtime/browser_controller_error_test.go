package browser_runtime

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestBrowserCommandOperationDoesNotExposeTargetValues(t *testing.T) {
	tests := []struct {
		arguments []string
		want      string
	}{
		{[]string{"--namespace", "athena", "--session", "secret-session", "open", "https://example.test/private?token=secret"}, "open"},
		{[]string{"--session", "secret-session", "tab", "new", "--label", "private", "https://example.test"}, "tab.new"},
		{[]string{"--session", "secret-session", "tab", "close", "t2"}, "tab.close"},
		{[]string{"--session", "secret-session", "get", "url"}, "get"},
	}
	for _, test := range tests {
		if got := browserCommandOperation(test.arguments); got != test.want {
			t.Fatalf("browserCommandOperation(%v) = %q, want %q", test.arguments, got, test.want)
		}
	}
}

func TestRunBrowserNavigationCommandRecoversAppliedTargetWithoutDuplicateOpen(t *testing.T) {
	openCalls := 0
	run := func(_ time.Duration, arguments ...string) (string, error) {
		operation := strings.Join(arguments, " ")
		switch {
		case strings.Contains(operation, " open "):
			openCalls++
			return "", errors.New("response timeout")
		case strings.Contains(operation, " tab list --json"):
			return `{"data":{"tabs":[{"active":true,"tabId":"t2","url":"https://example.test/target"}]}}`, nil
		default:
			return "", errors.New("unexpected command")
		}
	}
	if err := runBrowserNavigationCommand(run, []string{"--session", "session"}, "https://example.test/target", "--session", "session", "open", "https://example.test/target"); err != nil {
		t.Fatal(err)
	}
	if openCalls != 1 {
		t.Fatalf("navigation was duplicated after its target was observed: calls=%d", openCalls)
	}
}

func TestRunIdempotentBrowserCommandRetriesOnlyOnce(t *testing.T) {
	calls := 0
	run := func(_ time.Duration, _ ...string) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("transient failure")
		}
		return "ok", nil
	}
	if err := runIdempotentBrowserCommand(run, time.Second, "tab", "t2"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("idempotent command calls = %d, want 2", calls)
	}
}

func TestSafeBrowserTabLabelPrefixesNumericHosts(t *testing.T) {
	if got := safeBrowserTabLabel("http://127.0.0.1:28641/submission"); got != "page-127-0-0-1" {
		t.Fatalf("safeBrowserTabLabel() = %q, want page-127-0-0-1", got)
	}
}
