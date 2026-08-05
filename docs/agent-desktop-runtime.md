# Agent Desktop Runtime

Athena Launcher is the device-side executor for [Athena Agent Architecture v2](https://github.com/good-fish-man/agent-runtime/blob/main/doc/architecture-v2.md). It maintains an authenticated outbound connection to the Runtime Client control plane, advertises local capabilities, executes typed actions, and returns observations.

The desktop runtime owns browser sessions, application sessions, file authorizations, screenshots, keyboard/pointer adapters, permission prompts, idempotency records, and local execution logs. It does not interpret user goals or model text.

Execution rules:

- Reject expired, duplicated, out-of-order, unsupported, or unauthorized actions.
- Resolve only capability-level arguments; never execute a command embedded in an app name, URL, selector, or text field.
- Observe and report postconditions after every action.
- Prefer accessibility and browser snapshot references over coordinates.
- Require approval for medium/high-risk actions and block actions outside the declared policy.
- Keep credentials in the local Auth Vault; observations must redact secrets.

## Connection lifecycle

- Launcher initiates the outbound `athena.agent.v2` WebSocket and advertises its platform and capabilities in `HELLO`.
- It sends a heartbeat every 15 seconds. Runtime Client treats a connection without traffic for 45 seconds as offline.
- Disconnects use exponential backoff up to 30 seconds; a stable connection resets the delay for fast recovery.
- The device is bound to the first authenticated Athena user who explicitly binds or routes an action to it. Runtime Client rejects cross-user routing.
- Action results are returned directly to Runtime Client as typed Observations. The browser frontend is not part of this connection or execution path.
