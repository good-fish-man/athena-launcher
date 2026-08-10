# Athena Perception Layer

The Perception Layer answers one question: "What does the user's world look like now?"

It does not plan tasks or call an LLM. Browser Runtime executes actions, Perception verifies and compresses the resulting state, and Agent Runtime decides the next action.

```text
Perception Layer
|-- Perception Orchestrator
|   |-- Page Classifier
|   |-- Intent Signal Analyzer
|   |-- Confidence Evaluator
|   |-- Capture Policy
|   `-- Observation Budget
|-- Semantic Perception
|   |-- Accessibility
|   |-- ARIA
|   |-- Metadata
|   `-- Focused DOM
|-- Visual Perception
|   |-- Element Screenshot
|   |-- Viewport Screenshot
|   |-- Full Page Screenshot
|   `-- OCR
`-- Spatial Perception
    |-- Bounding Box
    |-- Coordinates
    |-- Element Position
    `-- Mouse Mapping
```

## Processing Flow

```text
Browser Action
  -> collect raw local browser state
  -> classify page and inspect intent signals
  -> select focused semantic evidence
  -> compare with the previous Session observation
  -> evaluate confidence and post-action changes
  -> optionally collect boxes, screenshot, annotations, or OCR
  -> verify the action outcome
  -> return a bounded structured Observation
```

Raw body text and accessibility snapshots are processed locally. They are not persisted or forwarded in full. Compatibility fields such as `content`, `snapshot`, and `key_elements` contain only the bounded semantic view.

## Version 2: Visual And Spatial Providers

Version 2 connects Capture Policy to real `agent-browser` providers:

| Evidence | Provider behavior |
| --- | --- |
| Viewport screenshot | `agent-browser screenshot <path>` |
| Element screenshot | `agent-browser screenshot <ref> <path>` |
| Full-page screenshot | `agent-browser screenshot --full <path>` |
| Annotated screenshot | Adds `--annotate --json` and retains the redacted annotation legend |
| Bounding box | Uses `agent-browser get box <ref> --json` for at most eight relevant refs |
| OCR | Uses local Tesseract when installed; otherwise reports `tesseract_not_installed` without failing the browser action |

OCR only accepts files under Athena's screenshot directory, validates the requested language, has a 45-second timeout, and returns at most 4,000 characters.

Screenshots include a SHA-256-based local Artifact identity and MIME type. Version 4 promotes selected artifacts to bounded `athena.agent.v3` Observation attachments. Runtime Client validates their decoded size and digest, strips bytes before persistence or frontend emission, and passes them to Agent Runtime only when the active model declares visual-input support.

Capture is automatic when visual appearance, image text, relative position, a challenge page, low confidence, or missing page identity requires more evidence. `screenshot: false` disables automatic capture for that request.

## Version 3: Incremental Verification

Each browser Session owns an in-memory perception baseline. Every observation includes:

- a monotonically increasing sequence;
- whether a previous observation exists;
- URL, title, content, and interactive-element changes;
- added, removed, and changed semantic refs;
- a stable/changed/baseline state;
- an action verification result.

Verification outcomes are:

| Status | Meaning |
| --- | --- |
| `verified` | The expected post-action state was observed. |
| `observed` | Observation completed but no state-changing postcondition was required. |
| `uncertain` | The action ran, but there is not enough observable evidence to claim success. |
| `blocked` | CAPTCHA, anti-bot, login, or another challenge requires user takeover. |
| `failed` | The browser explicitly reported an action error. |

For `click`, `press`, and `scroll`, no semantic change triggers one automatic visual capture. The result remains `uncertain` and recommends observing the same Session after 500 ms instead of pretending the action succeeded.

Closing a browser Session clears its incremental perception baseline.

## Version 4: Bounded Multimodal Evidence

Visual evidence now reaches capable models as native multimodal input instead of Base64 prompt text:

- Launcher accepts files only from the Athena screenshot directory and resolves symbolic links before checking containment.
- PNG, JPEG, and WebP content is sniffed and must match the filename extension.
- Each attachment is limited to 4 MiB and each Observation to two attachments.
- Runtime Client decodes Base64, verifies size and SHA-256, and maps evidence only for models declaring `vision`, `multimodal`, or `image-input`.
- Agent Runtime creates Eino `UserInputMultiContent` image parts with low/auto/high detail.
- Attachment bytes are excluded from database rows, task history, logs, and frontend stream events.
- Models without visual support continue through semantic, annotation-legend, and OCR evidence.

## Version 5: Adaptive Stabilization And Recovery

Browser Runtime no longer assumes that the first snapshot after a command is final. Navigation and interactive actions use a bounded settle window and compare page fingerprints until two observations match or the time budget expires. The final Observation reports attempts, elapsed time, stability, and probe failures.

Perception chooses a local observation profile without calling an LLM:

| Level | Profile | Evidence |
| --- | --- | --- |
| 1 | `semantic_minimum` | Metadata plus a small focused semantic view |
| 2 | `interactive` | Larger semantic evidence for action verification |
| 3 | `unstable_page` | Expanded evidence when the page did not settle |
| 4 | `multimodal` | Full configured semantic budget plus visual/spatial/OCR evidence |

Uncertain outcomes create a progressive recovery plan: settle and observe, capture an annotated viewport, refresh semantics and choose an alternative target, then request user takeover. Repeating the same unverified action three times opens a local circuit breaker so the Agent is told to stop looping.

## Observation Budget

| Evidence | Default limit |
| --- | ---: |
| Focused actionable elements | 30 |
| Content excerpt | 6,000 characters |
| Focused accessibility snapshot | 12,000 characters |
| Spatial refs | 8 |
| OCR text | 4,000 characters |
| Automatic captures per observation | 1 |

## Output Contract

```json
{
  "url": "https://www.youtube.com/",
  "title": "YouTube",
  "key_elements": [
    {"ref": "@e1", "label": "textbox Search"}
  ],
  "verification": {
    "action": "click",
    "status": "verified",
    "reason": "post_action_state_changed",
    "evidence": ["url_changed", "interactive_elements_changed"]
  },
  "perception": {
    "schema": "athena.perception.v6",
    "observation_id": "obs-...",
    "orchestrator": {
      "page_classifier": {"type": "media_catalog", "confidence": 0.95},
      "intent_signal_analyzer": {"visual": false, "spatial": false},
      "confidence_evaluator": {"score": 0.82},
      "capture_policy": {"capture": false, "scope": "viewport"},
      "adaptive_policy": {"level": 2, "profile": "interactive"},
      "observation_budget": {}
    },
    "semantic": {},
    "visual": {},
    "spatial": {},
    "incremental": {
      "sequence": 4,
      "has_previous": true,
      "changed": true,
      "url_changed": true,
      "stability": "changed"
    },
    "verification": {},
    "recovery": {"strategy": "continue", "circuit_open": false},
    "privacy": {
      "raw_page_forwarded": false,
      "raw_page_persisted": false
    }
  }
}
```

## Responsibility Boundary

- Browser Runtime owns profiles, browser processes, workspaces, windows, tabs, navigation, downloads, cookies, and Session lifecycle.
- Perception owns evidence selection, local visual/spatial providers, observation compression, change detection, and action verification.
- Agent Runtime owns planning and decides the next action from the Observation.
- Semantic refs are preferred for interaction. Coordinates are a fallback only when a real bounding box was observed.
