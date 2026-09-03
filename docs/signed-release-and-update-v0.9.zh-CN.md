# v0.9 签名发布与安全更新

Athena v0.9 使用失败关闭的发布链路：

1. 构建不可变平台产物。
2. 生成 SPDX JSON SBOM 与 SHA-256。
3. 生成 `athena.release-manifest.v1`，固定组件 Hash、精确字节数、Artifact 签名、平台签名要求、协议版本、过期时间和最低升级版本。
4. 使用 Ed25519 签名规范化 Manifest。
5. 将对应公钥编译进 Launcher。
6. Launcher 在安装前验证 Manifest 签名/时效、SBOM Hash、Artifact Hash/签名、平台代码签名和升级下限。
7. 本地更新在停止旧服务前创建 PostgreSQL 加密备份。

远程 Manifest 不允许开启开发模式。生产 Manifest 没有配置公钥时会被拒绝。源码构建固定官方 Release 公钥，正式构建可在编译时替换该值；空的构建参数不会清除源码默认值。Launcher 不会从 Manifest 相同的远程位置自动下载公钥，否则攻击者可以同时替换 Manifest 和公钥。`ATHENA_RELEASE_PUBLIC_KEY` 仅可覆盖本地签名 Manifest 的校验，用于发布流水线和集成测试，不能替换公网 Manifest 的编译期信任根。已经安装且此前验证通过的版本可在 Manifest 过期后继续运行，但不能用过期 Manifest 下载新产物。

macOS 需要有效 Developer ID 签名和公证证据；Windows 需要 Authenticode；Linux 包必须提供发行版、GPG、Cosign 或其他白名单包签名。任一平台缺少签名证据时生产发布都会失败关闭，不提供 unsigned emergency override。恢复发布只能重新生成正确签名的产物和 Manifest，不能降低校验强度。

Launcher 会在 Hash 与解压前验证压缩文件的精确字节数，拒绝 HTTPS 降级或指向不安全字面地址的重定向，并限制归档条目数、单文件大小和总展开大小。安装先写入唯一 staging 目录，再保留旧版本执行原子切换，避免中断更新删除最后一个可运行版本。

```bash
TAG=v0.9.0 ./scripts/generate-release-sbom.sh dist/release-sbom.spdx.json
TAG=v0.9.0 ./scripts/generate-release-manifest.sh dist dist/release-manifest.json
ATHENA_RELEASE_PRIVATE_KEY="$(cat /secure/ed25519.key)" go run ./cmd/release-manifest-sign -input dist/release-manifest.json -output dist/release-manifest.json
go run ./cmd/athena-launcher validate --manifest dist/release-manifest.json
```

禁止提交私钥。发布 Key 应位于构建目录之外，通过显式 Trust Store Release 轮换，并长期保存签名 Manifest 与 SBOM 作为审计证据。只有 Manifest、SBOM、Artifact 签名、平台签名证据和精确文件大小能够整体通过校验时，生产发布才算完成。
