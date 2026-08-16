# Athena Launcher

[English](README.md) | [简体中文](README.zh-CN.md)

设备运行时设计：[Agent Desktop Runtime](docs/agent-desktop-runtime.md)

浏览器：[常见命令手册](docs/browser-command-guide.md#简体中文) | [Athena Browser System v3](docs/browser-system-v3.md#简体中文)

源码导航：[项目结构](docs/project-structure.md#简体中文)

Athena Launcher 是基于 Wails 的 Athena 桌面应用和本地服务管理器。用户只需下载与操作系统匹配的一个安装包；Launcher 会安装独立 PostgreSQL，下载并校验 Runtime、Client、UI，生成相互兼容的配置，按顺序启动服务，并在原生桌面窗口中展示启动中心和 Athena UI。

桌面外壳使用 Wails v2 和系统 WebView；React UI 仍可作为独立版本化产物更新，但由 Wails 窗口直接加载，不再启动单独的前端 HTTP 服务。

<p align="center">
  <img src="docs/images/startup-center-overview.jpg" alt="展示服务健康状态和浏览器控制模式的 Athena 启动中心">
</p>
<p align="center"><sub>真实启动中心：已完成 Manifest 校验、服务健康检查和浏览器模式识别。</sub></p>

## 管理的内容

- 自动识别 macOS、Linux、Windows 和 `amd64`/`arm64`。
- 下载当前平台的 PostgreSQL、Agent Browser、Agent Runtime、Agent Runtime Client 和 Athena Agent UI。
- 使用 `release-manifest.json` 中的 SHA-256 校验所有产物。
- 在 `~/.athena` 初始化 PostgreSQL，随机生成密码并创建 `agent_runtime` 数据库。
- 自动生成匹配的 Runtime、Client 和 Skills 配置。
- 按 PostgreSQL、Runtime、Client、UI 的依赖顺序启动并执行健康检查。
- 在 Wails 原生窗口中展示安装、启动、更新、实时日志和 Athena UI。
- 桌面模式不监听 `17890` 或 `3000`；显式使用 `start`/`run` 的无界面兼容模式仍提供浏览器页面。
- 监控托管服务，并在异常退出后自动重启。
- 每次启动比较本地与远程包 Hash，更新前由用户确认。
- 复用已校验的安装包，并在服务更新时保留 PostgreSQL 和用户数据。

## 本地或远程模式

桌面应用每次启动时先显示连接方式：

- **本地工作空间**：下载并管理 PostgreSQL、Agent Browser、Runtime、Runtime Client 和 UI。
- **远程服务**：只下载或复用 Athena UI，检查远端 `agent-runtime-client` 的 `/healthz`，不下载、不启动本地数据库、Browser、Runtime 或 Client。

远端地址会保存在 `~/.athena/state.json`。公网地址必须使用 HTTPS；只有 `localhost` 和其他回环地址可以使用 HTTP。可以填写服务根地址，也可以粘贴完整的 `/api/agent-runtime-client/v1` 地址，Launcher 会自动规范化。切换远端服务器时会清除旧服务器的登录 Token，用户需要在新服务器重新登录。远端服务必须允许 Athena 桌面来源进行 CORS 请求。

## 架构

```mermaid
flowchart TD
    Package["DMG、Windows 安装包、AppImage 或单文件"] --> Launcher["Athena Launcher"]
    Launcher --> Manifest["Release Manifest + SHA-256"]
    Manifest --> Packages["各平台产物"]
    Launcher --> Desktop["Wails 原生窗口"]
    Desktop --> Startup["启动中心"]
    Desktop --> UI["Athena React UI"]
    Launcher --> PG["托管 PostgreSQL :15432"]
    Launcher --> Runtime["Agent Runtime :18080/:18081"]
    Launcher --> Client["Runtime Client :8090"]
    PG --> Runtime
    PG --> Client
    Client --> Runtime
    UI --> Client
```

启动流程：

1. 用户选择本地工作空间或输入远端 Runtime Client 地址。
2. 读取发布清单，并验证当前平台是否有可用产物。
3. 本地模式下载全部缺失组件；远程模式只准备 UI。所有下载都校验 SHA-256。
4. 本地模式生成配置并启动 PostgreSQL、Runtime 和 Client；远程模式验证远端 `/healthz`。
5. 把已校验 UI 切换到 Wails 窗口并进入 Athena。

## 普通用户安装

从 [GitHub Releases](https://github.com/good-fish-man/athena-launcher/releases/latest) 下载最新安装包：

| 平台 | 安装包 |
| --- | --- |
| Apple Silicon Mac | `Athena_<version>_macOS_arm64.dmg` |
| Intel Mac | `Athena_<version>_macOS_amd64.dmg` |
| Windows 10/11 x64 | `Athena-Setup_<version>_windows_amd64.exe` |
| Linux x86-64 | `Athena_<version>_linux_x86_64.AppImage` |
| Linux ARM64 | `Athena_<version>_linux_aarch64.AppImage` |

### macOS

打开 DMG，把 **Athena** 拖入 Applications 后启动。如果发布流程未配置正式签名，macOS 可能提示 Apple 无法验证应用。可右键 Athena 选择 **打开**，或在 **系统设置 > 隐私与安全性** 中允许。正式公开分发应配置 Developer ID 签名和公证。

### Windows

运行安装程序，然后从开始菜单或桌面快捷方式启动。正式发布建议配置 Authenticode 签名，避免 SmartScreen 警告。

### Linux

为 AppImage 添加执行权限后运行：

```bash
chmod +x Athena_<version>_linux_x86_64.AppImage
./Athena_<version>_linux_x86_64.AppImage
```

本地模式首次启动需要下载并初始化 PostgreSQL 与服务包，可能耗时几分钟；远程模式只准备 UI。

### 首次登录

Agent Runtime Client 只会在数据库中不存在 `athena` 账号时创建初始管理员：

```text
账号：athena
密码：athena
```

重启 Athena 不会重复创建账号，也不会把已修改的密码重置为 `athena`。该凭据只用于可信本机环境中的首次使用；向其他机器开放 Athena 前必须替换默认密码。

## 启动中心与日志

Wails 桌面窗口会立即展示启动中心，包括 Manifest、安装包、配置、数据库、Runtime、Client 和 UI 步骤。失败时可切换日志来源、复制错误，处理原因后点击 **重试启动**。

默认目录：

```text
~/.athena/
├── config/                 自动生成的服务配置
├── data/postgres/          持久化 PostgreSQL 数据
├── data/uploads/           上传和生成的报告
├── data/skills/            用户 Skills
├── logs/launcher.log
├── logs/postgres.log
├── logs/agent-runtime.log
├── logs/agent-runtime-client.log
├── packages/               已校验的下载包
├── services/               按版本安装的服务
├── state.json
└── startup-status.json
```

更新 PostgreSQL 安装包不会删除 `data/postgres`。服务程序、配置和用户数据位于不同目录，可独立更新。

## 命令行

桌面安装包内部调用的也是同一个命令行程序：

```bash
athena-launcher launch
athena-launcher start
athena-launcher status
athena-launcher stop
athena-launcher update
```

| 命令 | 用途 |
| --- | --- |
| `launch` | 安装包构建中打开 Wails 桌面窗口；CLI 构建中打开浏览器界面 |
| `start` | 在后台启动托管 Launcher |
| `run` | 前台运行，适合调试或系统服务管理器 |
| `install` | 只下载和准备包/配置 |
| `update` | 准备最新 Manifest 中的安装包 |
| `validate` | 验证当前平台的 Manifest |
| `status` | 输出各组件健康状态 |
| `stop` | 优雅停止 Launcher 和托管服务 |
| `version` | 输出 Launcher 和平台版本 |

常用参数和环境变量：

```bash
athena-launcher run --home /custom/athena --manifest /path/to/release-manifest.json
athena-launcher validate --manifest https://example.com/release-manifest.json

export ATHENA_HOME="$HOME/.athena"
export ATHENA_MANIFEST_URL="https://example.com/release-manifest.json"
```

正式 Launcher 会在编译时写入 Manifest URL。开发版本可使用绝对本地路径或 localhost HTTP 地址；公网远程 Manifest 必须使用 HTTPS。

## 更新与恢复

启动时，Launcher 会比较已安装 Hash 与当前 Manifest。如果包发生变化，启动中心会先征求用户同意，再停止旧进程并安装新版本。Hash 相同的包会复用，不会重复下载。

如果端口由 Athena 记录的托管进程占用，Launcher 会在重启前停止该进程；不会静默结束其他应用。可使用启动中心日志和 `athena-launcher status` 定位端口冲突。

如果只想重置下载的服务包，应先停止 Athena，再删除 `~/.athena/services` 或 `~/.athena/packages` 中对应版本；除非希望同时清除用户数据，否则不要删除 `~/.athena/data`。

## 从源码构建

需要 Go 1.24 或更高版本。桌面构建使用 Wails v2；Linux 构建还需要 GTK3 和 WebKit2GTK 4.1 开发包。

```bash
git clone https://github.com/good-fish-man/athena-launcher.git
cd athena-launcher
make test
make build
make desktop
make desktop-run
```

`make build` 生成保留浏览器界面的无界面/CLI 版本；`make desktop` 编译启用开发者工具的本地 Wails 桌面外壳。测试本地前端时使用 `make desktop-run`：它会先在相邻的 `frontend/agent-ui` 项目执行 `npm run build`，再让 Athena 直接加载该项目的 `dist`。按 `F12` 打开 WebView 检查器，Mac 紧凑键盘可能需要按 `Fn+F12`。macOS 上还会生成带麦克风、语音识别和定位权限声明的 `dist/Athena.app`，确保系统能够正常弹出授权。开发模式会跳过已下载前端包及前端更新提示，后端服务仍由 Manifest 管理；正式 Release 工作流不会包含 `devtools` 标签。如果仓库不是默认相邻目录结构，可传入 `FRONTEND_PROJECT=/path/to/agent-ui`，也可直接使用 `--frontend-dir /absolute/path/to/dist`。Linux 请先安装 `build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev`，并在发行构建中使用 `webkit2_41` 标签。

构建全部平台单文件：

```bash
make release VERSION=0.1.7 \
  MANIFEST_URL=https://github.com/good-fish-man/athena-launcher/releases/latest/download/release-manifest.json
```

桌面格式由 `packaging/macos`、`packaging/windows` 和 `packaging/linux` 中的脚本构建，也可直接运行仓库的 `Release` GitHub Actions 工作流。

## Release Manifest

[`release-manifest.example.json`](release-manifest.example.json) 展示了完整结构。数据库、可选浏览器、前端和服务产物需要声明：

- `darwin-arm64`、`windows-amd64` 等平台 Key。
- HTTPS 下载地址。
- 64 位十六进制 SHA-256。
- 压缩格式和可执行文件路径。
- 服务启动顺序和健康检查地址。

`services` 是通用有序列表，可以不修改 Launcher 就添加新的本地服务：

```json
{
  "name": "local-worker",
  "order": 30,
  "args": ["--config", "{config}/worker.yaml"],
  "env": {"ATHENA_HOME": "{home}"},
  "health_url": "http://127.0.0.1:19000/healthz",
  "artifacts": {}
}
```

参数支持 `{home}`、`{config}` 和 `{install}` 占位符。

## 发布顺序

Launcher 与服务可以使用不同版本。当前统一版本中，Launcher 与服务均使用 `v0.1.7`：

1. 在 `agent-runtime` 发布相同 Tag。
2. 在 `agent-runtime-client` 发布相同 Tag。
3. 在 `athena-agent-ui` 发布相同 Tag。
4. 推送 Launcher 的发布 Tag。运行 **Release** 时分别填写 `tag=v0.1.7` 和 `service_tag=v0.1.7`；工作流会收集对应服务产物、打包 PostgreSQL、计算 Hash 并把 `release-manifest.json` 发布到 Launcher Release。
5. **Publish Release Manifest** 会使用已发布的 Manifest URL 再构建一次启动器。Release 如果缺少清单，可填写服务 Tag 和 Launcher Tag 手动补跑该工作流。

GitHub Token 默认只能操作当前仓库，因此其他三个仓库的 Release 必须先存在。

## 安全边界

- 托管 PostgreSQL 只监听 `127.0.0.1:15432`。
- 随机数据库密码保存在权限为 `0600` 的配置/状态文件中。
- agent-browser Vault 加密密钥只生成一次并保存在权限为 `0600` 的 Launcher 状态文件中，服务和安装包更新后继续复用。
- 独立随机的内部服务令牌用于验证 Runtime 向 Client 创建定时任务的请求。
- 所有下载产物都必须匹配 SHA-256。
- 解压时拒绝绝对路径和 `..` 路径穿越。
- 更新替换版本化安装目录，不删除 `~/.athena/data`。
- 远程 Manifest 必须使用 HTTPS。
- 公开发布时应保护 Manifest 写权限，并配置平台代码签名。

## 相关项目

- [`agent-runtime`](https://github.com/good-fish-man/agent-runtime)
- [`agent-runtime-client`](https://github.com/good-fish-man/agent-runtime-client)
- [`athena-agent-ui`](https://github.com/good-fish-man/athena-agent-ui)

## 许可证

Athena Launcher 使用 [Apache License 2.0](LICENSE)。版权和第三方说明参见 [NOTICE](NOTICE) 与 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。PostgreSQL 和下载/打包的服务继续使用各自许可证。
