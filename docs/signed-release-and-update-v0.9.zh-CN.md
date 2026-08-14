# v0.9 签名发布与安全更新

Athena v0.9 使用失败关闭的发布链路：

1. 构建不可变平台产物。
2. 生成 SPDX JSON SBOM 与 SHA-256。
3. 生成 `athena.release-manifest.v1`，固定组件 Hash、Artifact 签名、平台签名要求、协议版本、过期时间和最低升级版本。
4. 使用 Ed25519 签名规范化 Manifest。
5. 将对应公钥编译进 Launcher。
6. Launcher 在安装前验证 Manifest 签名/时效、SBOM Hash、Artifact Hash/签名、平台代码签名和升级下限。
7. 本地更新在停止旧服务前创建 PostgreSQL 加密备份。

远程 Manifest 不允许开启开发模式。生产 Manifest 没有配置公钥时会被拒绝。已经安装且此前验证通过的版本可在 Manifest 过期后继续运行，但不能用过期 Manifest 下载新产物。

macOS 需要有效 Developer ID 签名和公证证据；Windows 需要 Authenticode；Linux 包应使用发行版/包签名。工作流默认阻止缺少平台签名证据的发布；紧急绕过只是需要审计的例外，不能被描述为“签名发布成功”。

```bash
TAG=v0.9.0 ./scripts/generate-release-sbom.sh dist/release-sbom.spdx.json
TAG=v0.9.0 ./scripts/generate-release-manifest.sh dist dist/release-manifest.json
ATHENA_RELEASE_PRIVATE_KEY="$(cat /secure/ed25519.key)" go run ./cmd/release-manifest-sign -input dist/release-manifest.json -output dist/release-manifest.json
go run ./cmd/athena-launcher validate --manifest dist/release-manifest.json
```

禁止提交私钥。发布 Key 应位于构建目录之外，通过显式 Trust Store Release 轮换，并长期保存签名 Manifest 与 SBOM 作为审计证据。
