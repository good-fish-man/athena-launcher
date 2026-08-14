# Athena 1.0 安装、升级与恢复

[English](ga-install-upgrade-v1.0.md) | [简体中文](ga-install-upgrade-v1.0.zh-CN.md)

Launcher 1.0 是桌面外壳、安装包校验器、本地服务 Supervisor、设备/浏览器 Runtime、备份协调器和升级边界。

## 支持模式

- **本地模式**：在 `~/.athena` 下安装已校验的 PostgreSQL、Agent Runtime、Runtime Client、Agent Browser 和 Agent UI。
- **远程模式**：本机只保留桌面/UI/设备组件，通过 HTTPS 连接远程 Runtime Client，不会停止或替换无关的本机服务。

## GA Manifest

正式 1.0 Manifest 必须固定协议 `1.0.0`、组件版本、各平台产物 URL/SHA-256，以及 `compatibility/v1.0.json` 的 URL/SHA-256。组件缺失、兼容矩阵不一致、升级来源不支持或 Hash 校验失败时，Launcher 会在替换服务前拒绝升级。

Release Manifest 与兼容矩阵采用严格 JSON 解码：未知字段、多个 JSON 值、重复组件，以及 Protocol、Runtime、Runtime Client、Launcher、UI 中任一版本不一致都会失败关闭。只有矩阵原始字节与签名 Manifest 固定的 Hash 一致后，Launcher 才会信任其内容。

支持原地升级的来源是 `0.9.0`。更老版本必须制定显式迁移方案，不能静默尽力升级。

## 安全升级

1. 执行 `athena-launcher readiness` 并处理失败项。
2. 在 Operations 页面创建并验证加密备份。
3. 下载并验证签名 1.0 Manifest 与兼容矩阵。
4. 下载到版本化暂存目录并校验每个 Digest。
5. 只有全部验证成功后才停止 Launcher 管理的进程。
6. 原子切换新包，执行 Health/Readiness，并保留上一版本用于回滚。
7. 健康检查失败时恢复上一套程序；只有数据迁移确有需要时，才从独立验证过的备份恢复数据。

Runtime Client 迁移保持增量与幂等；发布回归测试会确认代表性的 v0.9 用户、会话和记忆记录在 v1.0 初始化后仍然存在。程序回滚不会删除 PostgreSQL 数据目录。

替换 PostgreSQL 程序包不会删除 `~/.athena/data/postgres`。备份密钥独立于自动生成的服务配置。

## Readiness 命令

```bash
athena-launcher readiness
```

JSON 报告检查安装身份恢复、Manifest/兼容矩阵固定、本地或远程包完整性、备份身份与库存、平台签名证据，以及脱离前端的后台运行。非零退出码表示本机尚未达到发布就绪。

## 日志与恢复

检查 `~/.athena/logs/launcher.log`、`postgres.log`、`agent-runtime.log` 和 `agent-runtime-client.log`。保护 `state.json`、恢复密钥、备份密钥和 PostgreSQL 数据目录。删除 `state.json` 后应使用受保护恢复身份，不能在已有数据目录上重新生成数据库密码。

## 外部发布门禁

本地测试无法证明 Apple 公证、Windows Authenticode 信誉、Linux 发行版兼容性、公网 TLS、真实供应商/浏览器账号和长时间可用性压测。这些项目在发布流水线附加真实证据前保持 `EXTERNAL_REQUIRED`。
