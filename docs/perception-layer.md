# Athena Perception Layer

The Perception Layer is the single place that answers: "What does the user's world look like now?"

It is separate from execution. Browser Runtime, OS Runtime, File Runtime, and Terminal Runtime perform Actions; Perception collects verified state after those Actions and returns Observations to the Runtime Client.

```text
Perception Layer
  |-- Browser Observation Engine
  |-- Desktop Observation Engine
  |-- File Observation Engine
  |-- Terminal Observation Engine
  |-- Vision Observation Engine
  `-- Audio Observation Engine
```

## Layer Rules

- Perception does not call the LLM.
- Perception does not decide the next step.
- Perception does not execute user actions.
- Perception reports observed facts, not assumed success.
- Perception redacts credentials, tokens, cookies, passwords, and sensitive input values.
- `PROGRESS` is UI feedback only; the final `OBSERVATION` is authoritative.

## Engines

| Engine | Responsibility |
| --- | --- |
| Browser Observation Engine | URL, title, tabs, page snapshot, key elements, cookie summary, downloads, screenshots, takeover hints |
| Desktop Observation Engine | Active app, windows, focus, screen state, application readiness |
| File Observation Engine | Authorized roots, search results, file metadata, read status |
| Terminal Observation Engine | Command status, stdout/stderr summaries, exit codes, working directory |
| Vision Observation Engine | Screenshot understanding, visual element hints, OCR, challenge detection |
| Audio Observation Engine | Microphone state, speech session state, transcript readiness |

## Current Implementation

The Browser Observation Engine currently delegates to existing browser state managers for profile, workspace, window, tab, navigation, DOM, download, cookie, and session metadata. This keeps current browser behavior stable while making the architectural boundary explicit.

Future work should move each browser observation provider behind this layer directly. Browser Runtime should remain action-only: open, navigate, click, type, press, scroll, wait, download, screenshot, and close.
