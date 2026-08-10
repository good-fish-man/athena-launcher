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
       v
  Interaction Engine -> Browser Runtime -> agent-browser / CDP
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
| Optional declarative semantic knowledge | `siteknowledge/` |
| Device Action/Observation transport | `internal/launcher/deployment/device_runtime.go` |

The Browser Runtime supports navigation, click, play, type, hover, select, drag, key press, four-direction scrolling, back, forward, refresh, wait, download, screenshot, observe, automation and close. Element interactions accept observed semantic refs such as `@e12`; model-generated CSS, XPath, JavaScript and coordinates are not accepted.

## Protocols

| Schema | Purpose |
| --- | --- |
| `athena.agent.v3` | Device Action/Observation envelope |
| `athena.browser.task-plan.v3` | Deterministic task plan and constraints |
| `athena.perception.v6` | Bounded multimodal page observation |
| `athena.browser.target-resolution.v3` | Candidates, evidence, confidence and decision |
| `athena.browser.interaction-transaction.v3` | Policy, attempts, verification and duration |
| `athena.browser.automation.v3` | Persistent automation rule and watch state |
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

浏览器操作只接受页面观察产生的语义 ref，不接受模型编造的 CSS、XPath、JavaScript 或屏幕坐标。默认先使用 Accessibility/ARIA/Focused DOM；语义证据不足时才采集候选区域或视口截图。

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
- Browser 与 Search 的结构化 handoff，并在解析网址后继续原 Session；
- 前端可读执行轨迹及刷新后的安全恢复。

### 安全与隐私

完整 DOM、Cookie 值、密码、验证码、截图二进制和输入框敏感内容不会写入聊天记录。前端只保存经过白名单裁剪的 URL、标题、目标候选、置信度、动作耗时、验证结果、预算和监控事件。

真实网站仍可能需要用户完成登录、验证码、DRM 或区域限制。Athena 会保持原浏览器 Session 并请求人工接管，不会尝试绕过网站安全机制。
