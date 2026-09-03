# Signed Release and Safe Update v0.9

Athena v0.9 uses a fail-closed release chain:

1. Build immutable platform artifacts.
2. Generate an SPDX JSON SBOM and SHA-256.
3. Generate `athena.release-manifest.v1` with component hashes, exact byte sizes, artifact signatures, code-signing expectations, protocol version, expiry, and minimum upgrade version.
4. Sign the canonical manifest with Ed25519.
5. Embed the corresponding public key in Launcher builds.
6. Launcher verifies manifest signature/expiry, SBOM hash, artifact hash/signature, platform code signature, and upgrade floor before install.
7. A local update creates an encrypted PostgreSQL backup before stopping the old services.

Remote manifests cannot enable development mode. Production manifests without a configured public key are rejected. Source builds pin the official release key and production builds may replace it at link time; an empty build parameter does not erase the source default. Launcher never downloads a public key from beside the manifest because an attacker could replace both assets. `ATHENA_RELEASE_PUBLIC_KEY` may override verification only for a local signed manifest used by the release pipeline or integration tests, never the compiled trust root for a public manifest. An already installed and previously verified release may continue to run after manifest expiry, but a new download cannot use an expired manifest.

macOS requires a valid Developer ID signature and notarization evidence. Windows requires Authenticode. Linux packages must carry a distribution, GPG, Cosign, or other allowlisted package signature. Production publication fails closed when any platform evidence is unavailable; there is no unsigned emergency override. Restore service by republishing a correctly signed artifact and manifest, never by weakening verification.

Launcher checks the declared compressed byte size before hashing or extraction, rejects redirects that downgrade HTTPS or target unsafe literal addresses, and applies archive entry, per-file, and total-expanded-size budgets. Installation is staged in a unique directory and activated with rollback preservation so an interrupted replacement cannot erase the last working release.

Generate and validate locally:

```bash
TAG=v0.9.0 ./scripts/generate-release-sbom.sh dist/release-sbom.spdx.json
TAG=v0.9.0 ./scripts/generate-release-manifest.sh dist dist/release-manifest.json
ATHENA_RELEASE_PRIVATE_KEY="$(cat /secure/ed25519.key)" go run ./cmd/release-manifest-sign -input dist/release-manifest.json -output dist/release-manifest.json
go run ./cmd/athena-launcher validate --manifest dist/release-manifest.json
```

Never commit private signing keys. Keep release keys outside the build workspace, rotate with an explicit trust-store release, and retain signed manifests/SBOMs for audit. A production release is complete only when the manifest, SBOM, artifact signatures, platform-signing evidence, and exact artifact sizes all validate together.
