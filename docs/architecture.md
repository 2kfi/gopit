# Architecture

## Components

**gopitd** (per node) — a WebSocket server + UDP beacon responder.
Authenticates control connections with a shared token, then answers method
calls and pushes `system.stats` every second.

**gopit** (one per environment) — HTTP API + embedded SQLite +
WebSocket hub + discovery broadcaster. Serves the embedded web UI, holds
credentials (JWT sessions), and is the *only* thing that ever dials agents.

**Web UI** — vanilla JS + [xterm.js](https://xtermjs.org/), embedded in the
server binary via `embed.go`, built by Vite.

## Ports and protocols

| Side | Port | Protocol | Purpose |
|:-----|:-----|:---------|:--------|
| agent | 1221/TCP | WebSocket | authenticated control channel |
| agent | 1221/UDP | datagram | answers discovery broadcasts |
| server | 8080/TCP | HTTP + WS | dashboard, `/api/*`, `/ws` |

## Discovery and pairing

1. **Discover** — the server (per config `discovery_broadcast_addr:
   255.255.255.255`, `discovery_port: 1221`) broadcasts a probe every few
   seconds on a loop. Every agent hears it and replies with an announce
   packet: node UUID, hostname, IP, platform, arch, agent version, and
   `tls: true` when certificates are configured.
2. **Approve** — the announce is only an *introduction*. The node is listed
   as `discovered` and does nothing until an operator hits **Approve**. A
   manual **Add Node** path (by IP + port) exists for nodes that don't hear
   broadcasts.
3. **Pair** — approval assigns the server's `pairing_token` to the node. The
   agent's own `token` must match; every control connection is authenticated
   against it. Approving a node with the wrong token leaves it `pending`
   until the agent's token is fixed.
4. **Connect** — the server dials the agent. If the agent announced
   `tls: true` it connects via `wss://`, otherwise `ws://` (skipping
   certificate verification by default, see `tls_skip_verify`).

Node lifecycle: an announce (or manual add) inserts the node as
`pending`; approval moves it to `approved`; a successful authenticated
connection flips it to `online`; losing the connection returns it to
`offline`. A node stuck `pending` after approval usually means its `token`
doesn't match the server's `pairing_token` (the announce never carries
secrets — the server assigns the token on its side, and the agent must hold
the same one).

## Wire protocol

A single JSON envelope over WebSocket, both directions:

```json
{ "type": "request", "id": 1, "method": "docker.containers.list", "payload": { } }
{ "type": "response", "id": 1, "payload": { "containers": [ ... ] } }
{ "type": "event",    "id": 2, "method": "system.stats", "payload": { "cpu": 12.3 } }
{ "type": "response", "id": 3, "error": "unknown method: ..." }
```

- Requests are correlated by `id`; events carry `method` instead.
- The server proxies client API calls `/api/nodes/:uuid/rpc` into requests
  and fans out events to the connected browser session.
- Errors come back as `error` strings — no custom error codes.

### Method catalog

| Method | Direction | Description |
|:-------|:----------|:------------|
| `auth` | client → agent | first message on a control connection; carries the token |
| `system.info` | request | hostname, OS, platform, arch, kernel, CPUs, uptime, agent version, boot time |
| `system.stats` | event (1/s) | CPU percent, memory, disk, network, load, temps, uptime |
| `docker.containers.list` | request | ID, name, image, state, status, ports, created |
| `docker.container.inspect` | request | full container JSON |
| `docker.container.start` / `.stop` / `.remove` | request | lifecycle control |
| `docker.container.logs` | request (stream) | tail/stream logs; streamed as events with a `.logs.end` terminator |
| `docker.container.logs.stop` | request | ends a log stream |
| `docker.images.list` / `docker.image.remove` | request | image inventory + removal |
| `docker.volumes.list` / `docker.volume.remove` | request | volume inventory + removal |
| `docker.compose.list` / `.deploy` / `.down` / `.ps` | request | per-project compose ops |
| `terminal.open` | request | starts a PTY; `su -l` sessions validated inside the PTY (see Security) |
| `terminal.resize` | request | PTY size change |
| `terminal.close` | request | ends the session |
| `terminal.exit` | event | PTY exited; carries the exit code |
| `ufw.status` | request | parsed ufw status (rules, default policy, active) |
| `ufw.rule.add` / `ufw.rule.delete` | request | allow/deny rule management |
| `ufw.toggle` | request | enable/disable — only if `allow_toggle: true` |

### Terminal framing

On a terminal connection, TextMessage frames are JSON (control), BinaryMessage
frames are raw PTY bytes:

- client → agent (text): `{"op":"resize","cols":N,"rows":N}`, `{"op":"close"}`
- agent → client (text): `{"op":"exit","code":N}`

## Data (SQLite, `modernc.org/sqlite` — no CGO)

- `nodes` — discovered/approved agents and their identity
- `sessions` — JWTs / dashboard logins
- `settings` — key/value (JWT secret is generated here on first boot)

Schema is created automatically on first run; `db_path` defaults to
`gopit.db` in the working directory.