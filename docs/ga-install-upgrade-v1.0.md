# Athena 1.0 Installation, Upgrade, and Recovery

[English](ga-install-upgrade-v1.0.md) | [简体中文](ga-install-upgrade-v1.0.zh-CN.md)

Launcher 1.0 is the desktop shell, package verifier, local service supervisor,
device runtime, browser runtime, backup coordinator, and upgrade boundary.

## Supported Modes

- **Local** installs verified PostgreSQL, Agent Runtime, Runtime Client, Agent
  Browser, and Agent UI packages under `~/.athena`.
- **Remote** keeps only the desktop/UI/device components locally and connects to
  an HTTPS Runtime Client. It does not stop or replace unrelated local services.

## GA Manifest

A production 1.0 manifest must pin protocol `1.0.0`, component versions,
platform artifact URLs and SHA-256 values, and the URL/SHA-256 of
`compatibility/v1.0.json`. Launcher rejects missing components, a mismatched
compatibility matrix, unsupported upgrade sources, or failed hashes before
service replacement.

The supported in-place source is `0.9.0`. Older installations require an
explicit migration plan rather than an implicit best-effort upgrade.

## Safe Upgrade

1. Run `athena-launcher readiness` and resolve failures.
2. Create and verify an encrypted backup in the Operations UI.
3. Download and verify the signed 1.0 manifest and compatibility matrix.
4. Download packages to versioned staging paths and verify every digest.
5. Stop only Launcher-managed processes after verification succeeds.
6. Atomically activate the new package set, run health/readiness checks, and
   retain the previous set for rollback.
7. If health fails, restore the previous package set. Restore data only from a
   separately verified backup when a data migration requires it.

PostgreSQL package replacement does not delete `~/.athena/data/postgres`.
Backups are encrypted independently of generated service configuration.

## Readiness CLI

```bash
athena-launcher readiness
```

The JSON report validates recovered installation identity, manifest and
compatibility pins, local or remote package completeness, backup identity and
inventory, platform signing evidence, and frontend-independent background
execution. A nonzero exit status means the release is not locally ready.

## Logs and Recovery

Inspect `~/.athena/logs/launcher.log`, `postgres.log`, `agent-runtime.log`, and
`agent-runtime-client.log`. Keep `state.json`, recovery secrets, backup keys,
and the PostgreSQL data directory protected. If `state.json` is removed, use
the protected recovery identity; do not generate a new database password over
an existing data directory.

## External Release Gates

Local tests cannot prove Apple notarization, Windows Authenticode reputation,
Linux distribution compatibility, public TLS, real provider/browser accounts,
or a sustained availability soak. These remain `EXTERNAL_REQUIRED` until the
release pipeline attaches actual evidence.
