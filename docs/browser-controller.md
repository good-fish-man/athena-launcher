# Athena User-Side Browser Controller

## Responsibility boundary

- `internet.search` and `internet.fetch` handle normal public research.
- Browser capabilities are reserved for login, JavaScript pages, forms, scrolling, downloads, CAPTCHA, and complex interaction.
- Agent Runtime emits an `athena.agent.v2` Action; it never starts Chromium or reads a browser profile.
- Runtime Client routes Actions, waits for Observations, and resumes Agent Core.
- Athena Launcher owns the browser process, local execution, and Perception Layer.
- Browser Runtime executes browser Actions only; the Browser Observation Engine produces browser Observations.
- Frontend only displays progress and approval state. It never parses or executes model output.

## Execution flow

```text
Agent Runtime
  -> ACTION
Runtime Client WebSocket control plane
  -> Launcher Device Runtime
  -> agent-browser / Chromium
  -> Perception Layer
  -> OBSERVATION
Runtime Client
  -> Agent Runtime
```

## Browser capabilities

- `browser.open`: opens a URL or search-result page and creates a persistent browser session.
- `browser.navigate`: navigates an existing or new session to an exact HTTP(S) URL.
- `browser.observe`: returns the current URL, title, visible body text, accessibility snapshot, and normalized `key_elements`.
- `browser.click`: clicks a semantic ref such as `@e12` from the latest observation.
- `browser.type`: fills a semantic input ref with text.
- `browser.press`: sends a bounded navigation key such as `Enter`, `Escape`, or `Tab`.
- `browser.scroll`: scrolls up or down.
- `browser.wait`: waits a bounded number of milliseconds, then observes the page again.
- `browser.download`: downloads a user-requested file by clicking a semantic ref and stores it in Athena's download directory.
- `browser.screenshot`: captures a page screenshot and returns the local image path.
- `browser.close`: closes the session.

## Policy

- `ALLOW`: read-only navigation, extraction, scrolling, waiting, screenshots, and reversible browser interactions such as clicking a public result, typing a search query, or pressing Enter.
- `ASK_USER`: login, CAPTCHA, QR, 2FA, downloads, uploads, or user takeover.
- `BLOCK`: the device runtime refuses execution.

Session IDs are opaque and validated. URLs must be absolute HTTP(S) URLs without embedded credentials. Upload remains blocked until a native file picker supplies a user-approved path.

The browser controller has no public HTTP execution API. All execution arrives through the authenticated Action/Observation WebSocket.

The controller preserves one visible Athena browser session across normal browser commands. `browser.open` reuses that browser window and opens or switches to a labeled tab for the requested target; it does not create a second browser window unless the action explicitly requests an isolated/new session. Every action is followed by a Perception Layer Observation; Agent Runtime must evaluate that Observation before choosing the next step.

## Perception Boundary

Athena uses three distinct layers:

- **Perception Layer** answers "what does the world look like now?"
- **Decision Layer** answers "what should happen next?"
- **Action Layer** answers "how is the decision executed?"

Browser Runtime belongs to the Action Layer. It should open, navigate, click, type, press, scroll, wait, download, screenshot, and close. It must not decide that a task succeeded.

Browser Observation Engine belongs to the Perception Layer. It reads browser state after execution and emits URL, title, page snapshot, key elements, screenshot metadata, download state, cookie summary, tab state, and takeover hints.

```text
Perception Layer
  |-- Browser Observation Engine
  |-- Desktop Observation Engine
  |-- File Observation Engine
  |-- Terminal Observation Engine
  |-- Vision Observation Engine
  `-- Audio Observation Engine
```

## Browser Observation Providers

The Launcher browser path keeps the existing browser managers as observation providers behind the Browser Observation Engine. Every browser action is executed first, then the Perception Layer asks these providers for verified state:

| Manager | Current responsibility |
| --- | --- |
| Profile Manager | Resolves `isolated`, `profile`, or `auto_connect` mode and builds safe `agent-browser` profile arguments. |
| Workspace Manager | Keeps one default browser workspace for normal user browsing, so unrelated sites become tabs in the same visible browser instead of separate browser windows. |
| Window Manager | Tracks the visible window identity for each browser session and reports it in Observations. |
| Tab Manager | Reads the real `agent-browser tab list --json` result when available, tracks active tab URL/title, and returns tab metadata to Agent Runtime. |
| Navigation Manager | Records navigation target, current URL/title, and marks that postconditions must be evaluated from Observation. |
| DOM Observer | Normalizes accessibility snapshot refs into `key_elements` and reports DOM observation metadata without secrets. |
| Download Manager | Uses the real `agent-browser download <ref> <path>` command, reserves an Athena download directory, emits best-effort `PROGRESS` updates while the target or temporary download file grows, reports file path, size, completion, and marks downloads as approval-sensitive. |
| Cookie Manager | Reads cookie status with `agent-browser cookies get --json`, reports only counts/domains/session-cookie summary, and never exposes cookie values. |
| Session Manager | Creates one stable default `athena-*` browser session for normal opens, supports explicit isolated sessions when requested, persists active sessions, and preserves continuity across restarts. |

Observation state is persisted under the Athena home directory as `data/browser-runtime-state.json`. The state contains workspace/session/tab metadata only; it does not store raw cookies, passwords, tokens, or page secrets.

Every browser Observation now includes:

```json
{
  "session_id": "athena-...",
  "workspace_id": "athena-...",
  "window_id": "window-athena-...",
  "tab_id": "tab-main",
  "browser_runtime": {
    "profile": {"mode": "isolated"},
    "workspace": {"key": "youtube", "active_session_id": "athena-..."},
    "window": {"active": true},
    "tab": {"url": "https://www.youtube.com", "title": "YouTube"},
    "session": {"active": true, "current_url": "https://www.youtube.com"},
    "download": {"requested": false},
    "cookies": {"raw_cookies_exposed": false, "count": 4, "domains": [".youtube.com"]}
  },
  "tabs": {"available": true, "active_tab_id": "t1", "count": 1},
  "cookie_status": {"available": true, "raw_cookies_exposed": false, "count": 4},
  "screenshot": {"available": true, "path": "/Users/me/.athena/browser/screenshots/athena-...png"},
  "session_diagnostics": {"available": true}
}
```

For downloads, Launcher may emit WebSocket `PROGRESS` messages before the final Observation:

```json
{
  "type": "PROGRESS",
  "stage": "downloading",
  "message": "Downloading file: 12.4 MB",
  "progress": 42,
  "bytes": 13002342
}
```

The final Observation remains authoritative and includes the completed download path and file size.

This Perception boundary is intentionally independent from the `agent-browser` implementation. Future versions can replace the internal CLI adapter with CDP, Playwright, or native tab/window APIs without changing the Action/Observation contract.

When a page reaches login, CAPTCHA, QR, 2FA, or anti-bot verification, the Observation includes `takeover`. Agent Runtime should keep the browser open and resume with `browser.observe` on the same `resume_session_id` after the user completes the visible browser step.
