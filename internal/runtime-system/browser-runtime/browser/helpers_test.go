package browser

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseBrowserTabsFromJSON(t *testing.T) {
	tabs, activeID, raw := parseBrowserTabs(`{"tabs":[{"id":"t1","title":"YouTube","url":"https://youtube.com","active":true},{"id":"t2","label":"docs","title":"Docs","url":"https://docs.example"}]}`)
	if raw != "" || len(tabs) != 2 || activeID != "t1" {
		t.Fatalf("tabs=%#v active=%q raw=%q", tabs, activeID, raw)
	}
	if tabs[1]["label"] != "docs" {
		t.Fatalf("tab label missing: %#v", tabs[1])
	}
}

func TestParseBrowserTabsFromAgentBrowserJSON(t *testing.T) {
	tabs, activeID, raw := parseBrowserTabs(`{"success":true,"data":{"tabs":[{"active":false,"label":null,"tabId":"t1","title":"YouTube","url":"https://www.youtube.com/"},{"active":true,"label":"youtube","tabId":"t2","title":"Liked videos - YouTube","url":"https://www.youtube.com/playlist?list=LL"}]}}`)
	if raw != "" || len(tabs) != 2 || activeID != "t2" {
		t.Fatalf("tabs=%#v active=%q raw=%q", tabs, activeID, raw)
	}
	if tabs[0]["id"] != "t1" || tabs[1]["id"] != "t2" {
		t.Fatalf("agent-browser tab ids were not normalized: %#v", tabs)
	}
}

func TestSummarizeBrowserCookiesRedactsValues(t *testing.T) {
	summary := summarizeBrowserCookies(`{"cookies":[{"name":"SID","value":"secret","domain":".youtube.com"},{"name":"pref","value":"hidden","domain":".google.com","expires":-1}]}`)
	if summary["raw_cookies_exposed"] != false || summary["count"] != 2 || summary["has_cookies"] != true {
		t.Fatalf("bad summary: %#v", summary)
	}
	text := strings.TrimSpace(fmt.Sprintf("%v", summary))
	if text == "" || strings.Contains(text, "secret") || strings.Contains(text, "hidden") {
		t.Fatalf("cookie values leaked: %#v", summary)
	}
}

func TestSafeBrowserDownloadFilename(t *testing.T) {
	if safeBrowserDownloadFilename("../report.pdf", "athena-test") != "report.pdf" {
		t.Fatal("unsafe filename was not sanitized")
	}
}
