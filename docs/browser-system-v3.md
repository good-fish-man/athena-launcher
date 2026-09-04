# Athena Browser System v3

User-facing examples: [Browser Command Guide](browser-command-guide.md)

Athena Browser System v3 is the local, general-purpose browser subsystem used by Athena Desktop. It accepts high-level goals, resolves them against a bounded observation of the current page, executes semantic actions, verifies the result, and keeps the browser session available for later commands.

Chinese version: [简体中文](#简体中文)

## Architecture

```text
Agent Runtime
  Intent Parser
  Capability Router
       |
       | athena.agent.v3 ACTION
       v
Runtime Client WebSocket Control Plane
       |
       v
Athena Launcher
  Browser Task Planner
       |
       v
  Perception Orchestrator
    Semantic + Visual + Spatial
       |
       v
  UI Tree + Pattern Recognition + Page Model
       |
       v
  Candidate Resolver -> Target Resolver -> Risk-aware Confidence
       |
       +---- low confidence / sensitive ----> Re-observe or HITL
       |
       +---- semantic target -------------> Interaction Engine -> agent-browser
       |
       +---- visual-only target ----------> Pointer Grounding -> guarded CDP input
       |
       v
  Verification -> bounded OBSERVATION -> Agent Runtime
```

Search and browser interaction are separate capabilities. When `browser.task` receives a site name without an exact URL, it emits `athena.capability-handoff.v3`. Agent Runtime resolves an exact HTTP(S) origin through Search System and resumes `browser.task` with the same browser session.

## Implemented Modules

| v3 responsibility | Implementation |
| --- | --- |
| Session, profile, workspace, window, tab, navigation, download, cookie state | `internal/runtime-system/browser-runtime/browser/` |
| High-level deterministic task planning and action budget | `task_planner.go`, `browser_task.go` |
| Bounded semantic, visual, spatial and incremental perception | `perception/`, `perception_layer.go` |
| Site-independent UI tree and page understanding | `perception/ui_tree.go`, `patterns.go`, `page_model.go` |
| Candidate and target resolution | `target_resolver.go`, `target_grounding.go` |
| Risk policy, re-observation, retry and verification | `interaction_engine.go`, `stabilization.go` |
| Persistent rules, watch mode and event lifecycle | `automation_engine.go` |
| CDP event monitor with lightweight polling fallback | `automation_cdp.go`, `automation_probe.go` |
| Short-lived screenshot grounding and guarded CDP pointer input | `pointer_engine.go` |
| Optional declarative semantic knowledge | `siteknowledge/` |
| Device Action/Observation transport | `internal/launcher/deployment/device_runtime.go` |

The Browser Runtime supports navigation, click, play, type, hover, select, drag, key press, four-direction scrolling, back, forward, refresh, wait, download, screenshot, observe, automation, bounded pointer control and close. Normal element interactions accept observed semantic refs such as `@e12`; model-generated CSS, XPath, JavaScript and ungrounded coordinates are not accepted.

## Protocols

| Schema | Purpose |
| --- | --- |
| `athena.agent.v3` | Device Action/Observation envelope |
| `athena.browser.task-plan.v3` | Deterministic task plan and constraints |
| `athena.perception.v6` | Bounded multimodal page observation |
| `athena.browser.target-resolution.v3` | Candidates, evidence, confidence and decision |
| `athena.browser.interaction-transaction.v3` | Policy, attempts, verification and duration |
| `athena.browser.automation.v3` | Persistent automation rule and watch state |
| `athena.browser.pointer-grounding.v1` | Screenshot, page-revision and coordinate calibration contract |
| `athena.browser.pointer-result.v1` | Executed pointer action and verification inputs |
| `athena.capability-handoff.v3` | Browser-to-Search URL resolution handoff |

Target resolution decisions are `execute`, `reobserve`, `ask_user` or `block`. Thresholds rise with action risk. A visually grounded target must have candidate-specific evidence; a whole-page screenshot alone never proves that a candidate matches.

## Perception And Privacy

The default observation budget is:

| Evidence | Limit |
| --- | ---: |
| Focused elements | 30 |
| Content excerpt | 6,000 characters |
| Accessibility snapshot | 12,000 characters |
| Spatial refs | 8 |
| OCR | 4,000 characters |
| Automatic captures | 1 |

Raw DOM, passwords, verification codes, cookie values and screenshot bytes are not persisted in browser state or chat history. Chat history stores only a bounded execution trace: page identity, target candidates, confidence, action budget, verified interaction timings, Search handoff and watch events.

Visual evidence is adaptive. Athena starts with semantic evidence, captures an element or viewport only when visual/spatial intent or low confidence requires it, and exposes at most two validated image attachments to a model that declares image-input support.

## Pointer Control

`browser.pointer` is a fallback for visual-only surfaces such as Canvas, WebGL and unlabeled media overlays. It does not replace `browser.action` and cannot target an ordinary DOM control that has a semantic ref.

1. A viewport screenshot is captured and calibrated against the active CDP page.
2. The Observation receives an opaque `pointer_grounding` containing the session, screenshot ID, page revision, viewport metrics, allowed coordinate spaces and a two-minute expiry.
3. The caller must echo those identifiers exactly and provide either `normalized_1000` or screenshot-pixel coordinates.
4. Launcher verifies the document, URL, viewport, zoom and scroll state again before dispatch.
5. A local hit test rejects semantic controls, editable fields, frames, credentials, consent/auth/download targets and challenge pages.
6. The grounding is single-use. Click and drag dispatch through `Input.dispatchMouseEvent`, then require a fresh screenshot or semantic state change for post-action verification.

`move` is low risk, `click` is medium risk, and `drag` requires user approval. Any stale, expired, mismatched, already-used or unverifiable grounding fails closed and requires a new Observation.

## Verification And Recovery

Every meaningful interaction produces a transaction containing:

- risk and policy decision;
- selected semantic target and evidence scores;
- attempts and elapsed milliseconds;
- post-action verification;
- whether Athena re-observed or recovered;
- the remaining task action budget.

Reversible actions may be retried once after re-observation. Sensitive labels, downloads, uploads and drag operations require confirmation or user takeover. Repeated unverified actions open a local circuit breaker instead of looping indefinitely.

## Automation

Watch Mode stores generic trigger/action/verification rules under `~/.athena/data/browser-automation-v3.json`. The event monitor listens to CDP page, DOM, lifecycle and media events. It does not continuously invoke an LLM. If CDP events are unavailable, Athena explicitly reports the degradation and uses a 30-second lightweight observation fallback.

Automation lifecycle:

```text
created -> active -> triggered -> executing -> verifying -> active
                                      |
                                      +-> failed -> re-observe / HITL
```

## Acceptance Coverage

Automated tests cover:

- one stable browser session across different sites and commands;
- real tab reconciliation and no duplicate browser window by default;
- UI Tree and generic page patterns;
- ordinal, semantic, visual color and anchor-relative spatial targeting;
- confidence decisions and no fabricated visual evidence;
- reversible retry, risk blocking and verification;
- Browser/Search handoff with same-session continuation;
- automation persistence, event matching, cooldown and lifecycle;
- CDP monitor fallback without corrupting action status;
- pointer grounding correlation, expiry, single use and stale-page rejection;
- high-DPI screenshot calibration, bounded click/drag dispatch and post-action verification;
- pointer refusal for semantic, editable, framed, sensitive and challenge targets;
- bounded frontend trace persistence without DOM, cookie or credential leakage.

Real sites can still require user login, CAPTCHA, DRM, regional access or anti-bot intervention. These are represented as takeover Observations and are not bypassed.

---

## 简体中文

用户常见命令示例：[浏览器命令手册](browser-command-guide.md#简体中文)

Athena Browser System v3 是 Athena Desktop 的本地通用浏览器子系统。Agent 只描述用户目标，浏览器系统负责感知页面、解析语义目标、执行操作、验证结果，并保留会话供后续命令继续使用。

### 核心边界

- Agent Runtime 负责意图、规划和能力路由。
- Runtime Client 负责带身份认证的 Action/Observation WebSocket 控制面。
- Launcher Browser Runtime 负责确定性执行。
- Perception Layer 负责语义、视觉、空间、UI Tree、页面模型和增量观察。
- Interaction Engine 负责风险、等待、重观察、重试和结果验证。
- Search System 只负责发现准确网址，不代替浏览器交互。

普通浏览器操作只接受页面观察产生的语义 ref，不接受模型编造的 CSS、XPath、JavaScript 或未绑定截图的坐标。默认先使用 Accessibility/ARIA/Focused DOM；只有 Canvas、WebGL 等缺少语义目标的视觉表面，才可使用最新 Observation 生成的短期 `pointer_grounding`。

### 已实现能力

- 持久化 Session/Profile/Workspace/Window/Tab/Navigation 状态；
- 同一个浏览器窗口内复用会话并按需创建或切换标签页；
- 通用 UI Tree、搜索框/列表/卡片/表单/媒体等模式识别；
- Candidate Resolver、Target Resolver 和风险感知置信度；
- 语义、颜色视觉、绝对位置和相对锚点空间定位；
- 点击、播放、输入、悬停、下拉选择、拖拽、按键、四向滚动、前进、后退、刷新、等待、下载和截图；
- 每次重要动作后的验证、重观察、可逆重试和熔断；
- 登录、验证码、二维码、反爬和敏感操作的人工接管；
- 基于 CDP 事件的 Watch Mode、持久化规则和轻量轮询降级；
- 基于截图、页面版本和会话校准的受控 `browser.pointer` 移动、点击与拖拽；
- Browser 与 Search 的结构化 handoff，并在解析网址后继续原 Session；
- 前端可读执行轨迹及刷新后的安全恢复。

### Pointer 安全边界

- 优先使用 `browser.action` 和 `@ref`；存在语义目标时拒绝 Pointer。
- Pointer 只接受最新视口截图随 Observation 返回的 `grounding_id`、`screenshot_id` 和 `page_revision`。
- Grounding 两分钟过期、单次使用；页面、滚动、缩放或视口变化后必须重新截图。
- Launcher 将截图坐标校准为主 frame 的 CSS viewport 坐标，并在动作前重新命中测试。
- 输入框、密码、登录、授权、同意、上传、下载、验证码、iframe 和其他敏感目标一律拒绝。
- 点击和拖拽后必须观察到截图或语义状态变化，否则返回未验证失败；拖拽还必须经过用户审批。

### 安全与隐私

完整 DOM、Cookie 值、密码、验证码、截图二进制和输入框敏感内容不会写入聊天记录。前端只保存经过白名单裁剪的 URL、标题、目标候选、置信度、动作耗时、验证结果、预算和监控事件。

真实网站仍可能需要用户完成登录、验证码、DRM 或区域限制。Athena 会保持原浏览器 Session 并请求人工接管，不会尝试绕过网站安全机制。
