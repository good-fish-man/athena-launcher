# Athena Launcher

Athena Launcher 是一个零第三方 Go 依赖的单文件安装器和服务管理器。最终用户只需下载与系统匹配的 `athena-launcher`，它会自动完成：

- 识别 `darwin/linux/windows` 与 `amd64/arm64` 平台。
- 下载并校验 PostgreSQL、`agent-runtime`、`agent-runtime-client` 和 Athena UI 的 SHA256。
- 在 `~/.athena` 初始化独立 PostgreSQL 数据目录，随机生成数据库密码并自动创建 `agent_runtime` 数据库。
- 生成共享数据库、runtime、client 和 skills 配置。
- 按 PostgreSQL、runtime、client 顺序启动并等待健康检查。
- 下载并在 `http://127.0.0.1:3000` 托管 Athena 前端单页应用。
- 监控业务进程，异常退出后自动重启；正常停止时按相反顺序关闭。
- 再次执行时复用已校验的安装包和数据库，不会重复下载或覆盖用户数据。

`agent-runtime-client` 启动时会执行现有的全表迁移，runtime 会执行 memory 表迁移，因此用户无需手工建表。

## 用户使用

发布版本应在编译时写入正式清单 URL，用户可直接运行：

```bash
./athena-launcher start
./athena-launcher status
./athena-launcher stop
./athena-launcher update
```

首次启动可能需要几分钟。日志位于 `~/.athena/logs/`，生成配置位于 `~/.athena/config/`，数据库数据位于 `~/.athena/data/postgres/`。

未内置清单 URL 的开发版本可以显式传入清单：

```bash
./athena-launcher start --manifest /absolute/path/release-manifest.json
./athena-launcher run --manifest https://downloads.example.com/athena/release-manifest.json
```

`run` 在前台运行，适合调试或由 systemd/launchd/Windows Service 托管；`start` 在后台运行。

## 发布构建

运行测试并生成五个平台的单文件启动器：

```bash
make test
make release VERSION=0.1.0 MANIFEST_URL=https://downloads.example.com/athena/release-manifest.json
```

生成 runtime/client 平台包：

```bash
TARGET_OS=darwin TARGET_ARCH=arm64 VERSION=0.1.0 ./scripts/package-services.sh
TARGET_OS=linux TARGET_ARCH=amd64 VERSION=0.1.0 ./scripts/package-services.sh
```

脚本会同时输出 SHA256，把地址和校验值写入 `release-manifest.json`。清单结构可参考 [release-manifest.example.json](release-manifest.example.json)。示例中的域名和 `REPLACE_WITH_64_CHAR_SHA256` 必须替换后才能使用。

正式 Release 可在 GitHub Actions 中运行 `Publish Release Manifest`。该工作流会：

1. 下载并校验 Maven Central 的 PostgreSQL 多平台精简包。
2. 将 PostgreSQL 重新打包成 launcher 使用的标准目录。
3. 下载同版本 runtime/client Release 资产并计算 SHA256。
4. 下载同版本 Athena UI 静态资产并写入清单。
5. 生成并发布 `release-manifest.json`、`SHA256SUMS` 和 PostgreSQL 平台包。
6. 重新构建内置该 manifest URL 的 launcher。

PostgreSQL 发布包需要由发布流水线准备为自包含压缩包，解压后根目录必须包含：

```text
bin/initdb
bin/pg_ctl
bin/postgres
```

Windows 文件带 `.exe`。若使用不同目录，可修改清单的 `database.bin_dir`。公网下载地址必须使用 HTTPS，本地开发地址允许 `localhost` HTTP 或绝对文件路径。

## 扩展服务

清单中的 `services` 是通用的有序服务列表，不限于当前两个后端。后续要托管其他本地服务，只需添加平台产物、启动参数、环境变量和健康检查：

```json
{
  "name": "local-worker",
  "order": 30,
  "args": ["--config", "{config}/worker.yaml"],
  "env": { "ATHENA_HOME": "{home}" },
  "health_url": "http://127.0.0.1:19000/healthz",
  "artifacts": {}
}
```

参数支持 `{home}`、`{config}` 和 `{install}` 占位符。

## 安全边界

- 数据库仅监听 `127.0.0.1:15432`，密码随机生成并以 `0600` 权限保存。
- 下载产物必须匹配清单 SHA256，解压时拒绝绝对路径和 `../` 路径穿越。
- 安装更新只替换版本化服务目录，不删除 `~/.athena/data`。
- 发布环境应通过 HTTPS 分发清单，并由发布系统保护清单写权限。
