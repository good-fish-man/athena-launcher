# Athena Browser Command Guide

[English](#english) | [简体中文](#简体中文)

Athena accepts natural-language browser goals. These examples are not a rigid command syntax: the Agent Runtime identifies an explicit browser interaction, and Athena Browser Runtime executes it against a retained, observable browser session.

---

## English

### Browser Interaction Versus Web Research

Athena keeps these operations separate:

| User goal | Route | Opens the local browser |
| --- | --- | --- |
| `Open YouTube` | Browser interaction | Yes |
| `Play the second video on this page` | Browser interaction | Uses the current browser session |
| `What is the title of the current page?` | Browser observation | Uses the current browser session |
| `How do I convert a Chinese driving license in Japan?` | Server-side research | No |
| `Find the latest official Japanese license-conversion requirements` | Server-side research | No |
| `Open the browser and search Google for license conversion` | Browser interaction | Yes, because visible browser use was explicit |

An active browser session is context only. It does not give an unrelated conversation or research request permission to control the device.

### Common Commands

#### Open And Navigate

```text
Open YouTube.
Open the QQ Music website.
Open https://news.ycombinator.com/.
Open GitHub.
Go back.
Go forward.
Refresh the current page.
```

When a managed browser already exists, Athena reuses that browser and opens or switches to the appropriate managed tab instead of intentionally starting another browser window.

#### Search, Select, And Open Results

```text
Open YouTube, search for AI Agent tutorials, and play the first video.
Search Netflix for Stranger Things and open the second result.
Open GitHub, search for golang agent, and open the second repository.
Open the second story on this page.
Select the yufu Netflix profile.
```

Use explicit ordinals such as `first`, `second`, or `third`. Athena resolves them against the current semantic result list, not raw DOM order.

#### Video And Music Playback

```text
Play the second video.
Play the current video.
Pause the current video.
Leave the current video.
Play Adele Hello.
Open QQ Music and play the first recommended song.
```

A short title such as `Adele Hello` is treated as media only when the recent conversation is already controlling a media page. In a new conversation, use a complete command such as `Play Adele Hello on YouTube`.

#### Page Interaction

```text
Click the Search button.
Type "Athena Browser" in the search box and press Enter.
Scroll down.
Scroll up.
Wait for the page to finish loading.
Choose English from the language menu.
```

Athena targets observed semantic elements. It does not accept model-invented CSS selectors, XPath, JavaScript, or screen coordinates.

When labels repeat, describe the region and a nearby anchor instead of using the label alone:

```text
On the current YouTube page, click the Shorts filter in the top filter bar, immediately to the right of All.
Click Playlists in the left sidebar, under the You section and below History.
Click the second button from the left in the top filter bar, labeled Shorts.
```

Run these as separate commands when the first click may change the page. A live semantic label and spatial anchor are more reliable than phrases such as `click the red arrow` or raw coordinates.

#### Observe The Current Page

```text
What is the current page title?
Summarize this page.
What video is currently playing?
Show me the important links on this page.
Take a screenshot of the current page.
```

Observation is bounded. Athena normally returns page identity, a focused semantic outline, important elements, and only the visual evidence needed for the task rather than uploading the complete DOM.

#### Download, Login, And Close

```text
Download the PDF on this page.
Log in to this website.
Open the Netflix login page.
Close the browser session.
```

Downloads and other higher-risk actions may require confirmation. Passwords, CAPTCHA, QR login, 2FA, DRM, regional restrictions, and anti-bot challenges require user takeover; Athena does not bypass them.

### Multi-turn Example

Commands continue in the same retained browser session:

```text
User: Open YouTube.
User: Search for AI Agent tutorials.
User: Play the second video.
User: Pause the current video.
User: What is the video title?
```

The later commands do not need to repeat `YouTube`, provided the current browser context is still relevant. If several sites are open, name the target to remove ambiguity: `On YouTube, play the second video`.

### Wording Tips

- Prefer `Open QQ Music website` over `Open music`; the latter may mean a desktop music application.
- Prefer `Play the second video` over fragments such as `music.play second vidos`.
- State the site, action, content, and ordinal in one sentence for multi-step work.
- Say `current page`, `current video`, or the site name when continuing an existing session.
- Minor spelling mistakes such as `youtub` or `vido` may be tolerated, but clear wording is more reliable.
- Opening a site is not proof of task completion. Athena verifies the observed URL, title, selected target, and post-action state before reporting success.

### Safety Boundary

Athena can perform reversible page interactions. It does not silently submit purchases, bookings, appointments, messages, account changes, deletion, consent, credentials, verification codes, or payment. These actions require an explicit product workflow and user confirmation.

---

## 简体中文

### 浏览器操作与联网研究的区别

Athena 会严格区分以下两类请求：

| 用户目标 | 路由 | 是否打开本地浏览器 |
| --- | --- | --- |
| `打开 YouTube` | 浏览器操作 | 是 |
| `播放当前页面的第二个视频` | 浏览器操作 | 复用当前浏览器会话 |
| `当前页面标题是什么？` | 浏览器观察 | 复用当前浏览器会话 |
| `中国驾照如何换成日本驾照？` | 服务端联网研究 | 否 |
| `查询最新的日本驾照换证官方要求` | 服务端联网研究 | 否 |
| `打开浏览器，用 Google 搜索日本驾照换证` | 浏览器操作 | 是，因为用户明确要求可见浏览器操作 |

浏览器已经打开只代表存在上下文，不代表普通聊天或联网研究自动获得设备操作权限。

### 常见命令

#### 打开与导航

```text
打开 YouTube。
打开 QQ 音乐网站。
打开 https://news.ycombinator.com/。
打开 GitHub。
返回上一页。
前进到下一页。
刷新当前页面。
```

已有受控浏览器时，Athena 会复用同一个浏览器，并在其内部打开或切换受管理的标签页，而不是主动创建第二个浏览器窗口。

#### 搜索、选择和打开结果

```text
打开 YouTube，搜索 AI Agent 教程，并播放第一个视频。
在 Netflix 搜索 Stranger Things，然后打开第二个结果。
打开 GitHub，搜索 golang agent，并打开第二个仓库。
打开当前页面的第二篇文章。
选择名为 yufu 的 Netflix Profile。
```

建议明确使用“第一个、第二个、第三个”。Athena 会依据当前页面识别出的语义结果列表选择，而不是直接使用原始 DOM 顺序。

#### 视频和音乐播放

```text
播放第二个视频。
播放当前视频。
暂停当前视频。
退出当前视频。
播放 Adele Hello。
打开 QQ 音乐并播放第一首推荐歌曲。
```

只有最近的会话已经在控制媒体页面时，`Adele Hello` 这样的短标题才会被当作连续媒体命令。新会话中建议使用完整描述，例如：`在 YouTube 播放 Adele Hello`。

#### 页面交互

```text
点击搜索按钮。
在搜索框输入“Athena Browser”，然后按回车。
向下滚动。
向上滚动。
等待页面加载完成。
在语言菜单中选择 English。
```

Athena 只操作页面观察得到的语义元素，不接受模型编造的 CSS、XPath、JavaScript 或屏幕坐标。

页面存在同名元素时，不要只描述文字，应同时提供区域和邻近锚点：

```text
在当前 YouTube 页面，点击顶部筛选栏中 All 右边的 Shorts 按钮。
点击左侧边栏 You 分组中的 Playlists，它位于 History 下方。
点击顶部筛选栏从左向右第二个按钮，文字是 Shorts。
```

如果第一次点击可能改变页面，建议分成两条命令依次执行。实时页面中的语义文字和相对位置，比“点击红色箭头”或屏幕坐标更可靠。

#### 读取当前页面

```text
当前页面的标题是什么？
总结当前页面。
现在正在播放什么视频？
列出这个页面的重要链接。
给当前页面截图。
```

页面观察有明确预算。Athena 通常只返回页面身份、聚焦后的语义结构、重要元素和任务所需的视觉证据，不会把完整 DOM 上传给模型。

#### 下载、登录和关闭

```text
下载当前页面中的 PDF。
登录这个网站。
打开 Netflix 登录页面。
关闭浏览器会话。
```

下载等较高风险动作可能要求确认。密码、验证码、扫码登录、2FA、DRM、区域限制和反爬验证需要用户人工接管，Athena 不会绕过这些安全机制。

### 连续对话示例

下面的命令会在同一个浏览器会话中继续执行：

```text
用户：打开 YouTube。
用户：搜索 AI Agent 教程。
用户：播放第二个视频。
用户：暂停当前视频。
用户：这个视频叫什么？
```

浏览器上下文仍然相关时，后续命令不必重复写 YouTube。如果同时打开了多个网站，建议写明目标：`在 YouTube 播放第二个视频`。

### 表达建议

- 使用 `打开 QQ 音乐网站`，不要只说 `open music`；后者也可能表示打开桌面音乐应用。
- 使用 `播放第二个视频`，不要使用 `music.play second vidos` 这样的片段式表达。
- 多步骤任务尽量在一句话中写清网站、动作、内容和序号。
- 继续操作时使用“当前页面”“当前视频”或明确写出网站名称。
- `youtub`、`vido` 等轻微拼写错误可能被容错，但清晰完整的表达更可靠。
- 打开网站不等于任务完成。Athena 会检查 URL、标题、目标元素和动作后的页面状态，再报告成功。

### 安全边界

Athena 可以执行可逆的页面操作，但不会静默提交购买、预订、挂号、消息、账号修改、删除、授权、密码、验证码或付款。这些动作必须进入明确的产品流程，并由用户确认。
