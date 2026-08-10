# Athena Launcher Project Structure

[English](#english) | [简体中文](#简体中文)

## English

Athena Launcher is organized as real Go package directories. The repository
root contains project metadata only; executable and runtime code live under
`cmd/` and `internal/`.

```text
athena-launcher/
├── cmd/
│   └── athena-launcher/       process entrypoint only
├── internal/
│   ├── launcher/              thin public entry facade
│   │   └── deployment/        startup, deployment and desktop orchestration
│   ├── runtime-system/
│   │   └── browser-runtime/
│   │       ├── browser-runtime package  browser controller, tasks and perception
│   │       └── browser/       persistent browser state managers
│   ├── control/               Action/Observation wire contracts
│   ├── release/               release manifest contracts and validation
│   └── state/                 persistent launcher state and local secrets
├── packaging/                 macOS, Windows and Linux installers
├── scripts/                   release and dependency packaging scripts
└── docs/                      architecture and operating documentation
```

### Package boundaries

| Package | Responsibility |
| --- | --- |
| `cmd/athena-launcher` | Parse process exit status and call `launcher.Run`; no product logic. |
| `internal/launcher` | Thin public facade used by the process entrypoint. |
| `internal/launcher/deployment` | CLI commands, startup flow, service supervision, desktop bridge, database, frontend and platform adapters. |
| `internal/runtime-system/browser-runtime` | Browser controller, browser tasks, settings, platform adapters and perception layer. |
| `internal/runtime-system/browser-runtime/browser` | Browser profiles, workspaces, sessions, windows, tabs, downloads, cookies and persisted observations. |
| `internal/control` | Stable WebSocket `Action`, `Observation`, `Progress`, `Cancel` and policy payloads. |
| `internal/release` | Release manifest schema and platform artifact validation. |
| `internal/state` | Read and atomically persist `~/.athena/state.json`; generate local secrets. |

### Dependency direction

```text
cmd/athena-launcher
        |
        v
internal/launcher
        |
        v
internal/launcher/deployment
        |
        +------> browser-runtime ------> browser state
        +------> control / release / state
```

Lower-level packages never import `internal/launcher`. OS-specific files remain
inside `internal/launcher/deployment` behind build tags because they implement orchestration
ports rather than own product workflows.

### Where changes belong

- CLI and startup behavior: `internal/launcher/deployment/command.go`, `prepare.go`,
  `local_runtime.go`, `startup.go` and `services.go`.
- Browser state or lifecycle: the matching manager under `internal/runtime-system/browser-runtime/browser/`.
- Browser command execution: `internal/runtime-system/browser-runtime/browser_controller.go` or
  `browser_task.go`; do not persist state there.
- Device protocol fields: `internal/control/protocol.go`; device connection and
  routing behavior stays in `internal/launcher/deployment/device_runtime.go`.
- Manifest schema: `internal/release/manifest.go`; download/install workflows
  stay in `internal/launcher/deployment/download.go` and `update.go`.
- Persistent installation state: `internal/state/state.go`.

## 简体中文

Athena Launcher 现在按照真实 Go 包目录组织。仓库根目录只保留项目元数据，
可执行入口和运行代码分别位于 `cmd/` 与 `internal/`。

```text
athena-launcher/
├── cmd/
│   └── athena-launcher/       进程入口
├── internal/
│   ├── launcher/              对外入口门面
│   │   └── deployment/        启动、部署与桌面编排
│   ├── runtime-system/
│   │   └── browser-runtime/
│   │       ├── browser-runtime 包  浏览器控制、任务与感知
│   │       └── browser/       浏览器状态管理
│   ├── control/               Action/Observation 通信协议
│   ├── release/               Release Manifest 定义与校验
│   └── state/                 Launcher 状态与本地密钥
├── packaging/                 macOS、Windows、Linux 安装包
├── scripts/                   发布和依赖打包脚本
└── docs/                      架构与使用文档
```

目录职责如下：

- `cmd/athena-launcher` 只负责启动进程、输出错误和设置退出码，不放业务逻辑。
- `internal/launcher` 只负责对外暴露 `Run` 入口。
- `internal/launcher/deployment` 负责命令、启动流程、服务监管、桌面桥接、数据库、前端及平台适配。
- `internal/runtime-system/browser-runtime` 负责浏览器控制器、浏览器任务、浏览器配置和感知层。
- `internal/runtime-system/browser-runtime/browser` 负责 Profile、Workspace、Session、Window、Tab、下载、Cookie 和浏览器状态持久化。
- `internal/control` 只定义稳定的 WebSocket Action/Observation 协议，不执行桌面动作。
- `internal/release` 负责发布清单结构及平台产物安全校验。
- `internal/state` 负责读取和原子写入 `~/.athena/state.json`，并生成本地密钥。

依赖方向只能从 `cmd` 指向 `internal/launcher`，再由 `internal/launcher`
调用 `browser`、`control`、`release`、`state`。底层包不能反向依赖启动编排包，
这样后续拆分浏览器进程或替换设备传输层时不会牵动整个项目。

修改代码时建议按以下路径定位：

- 启动问题：`internal/launcher/deployment/command.go` → `prepare.go` → `local_runtime.go` → `services.go`。
- 浏览器状态：进入 `internal/runtime-system/browser-runtime/browser/` 对应 Manager；浏览器命令执行进入 `internal/runtime-system/browser-runtime/browser_controller.go`。
- 设备协议字段：修改 `internal/control/protocol.go`；连接与路由修改 `internal/launcher/deployment/device_runtime.go`。
- 安装或更新：`internal/release/manifest.go` → `internal/launcher/deployment/download.go` / `update.go`。
- 状态或密码：`internal/state/state.go`；数据库进程：`internal/launcher/deployment/database.go`。
