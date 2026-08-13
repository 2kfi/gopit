# Manage

Multi-node Linux management platform in Go. A single **server** binary serves a
web dashboard that manages many **agent** nodes — system metrics, Docker,
terminal access, and UFW firewall — over WebSocket, no agents-of-agents, no
cloud, no account.

```
┌──────────────┐   HTTP + WS    ┌───────────────────────┐   WS + UDP    ┌─────────────────┐
│   Browser    │ ─────────────► │  gopit        │ ────────────► │ gopitd    │
│  dashboard   │   :8080        │  (SQLite, JWT, hub)   │   :1221/TCP   │ (on each node)  │
└──────────────┘                │                      │   :1221/UDP   │ docker, ufw,    │
                                └───────────────────────┘               │ pty, gopsutil   │
                                                                        └─────────────────┘
```

- **Discovery** — the server broadcasts a UDP probe; agents announce themselves. Discovered nodes need approval before the server connects.
- **Live by default** — system stats stream over WebSocket at 1s intervals; no polling.
- **Single binary per side** — the web UI is embedded in the server binary.
- **No CGO** — pure-Go SQLite (`modernc.org/sqlite`), statically linkable, cross-compiles cleanly.

## Quick start

Both sides install with one script — **one mode at a time**:

```bash
# on the management machine
sudo ./install.sh server          # prompts admin creds, prints a pairing token

# on each node to manage
sudo TOKEN=<pairing-token> ./install.sh agent --apply-ufw
```

Then open `http://<server-ip>:8080`, log in, **Nodes → Discover**, and
**Approve** the nodes that appear. Wait ~2s and their status flips to
`online`; click through to Dashboard, Docker, Terminal, or Firewall.

Prefer to run it manually (no systemd)? Both binaries take `-config`, and
example configs live in `configs/`:

```bash
make build-all                       # built to bin/
./bin/gopit -config configs/gopit.example.yaml
./bin/gopitd  -config configs/gopitd.example.yaml
```

## Documentation

| Doc | What it covers |
|:---|:---|
| [docs/install.md](docs/install.md) | Installing server and agents, network requirements, firewall |
| [docs/configuration.md](docs/configuration.md) | Every config field for both binaries |
| [docs/usage.md](docs/usage.md) | The dashboard, tab by tab |
| [docs/architecture.md](docs/architecture.md) | Components, wire protocol, method catalog |
| [docs/security.md](docs/security.md) | Tokens, TLS, sudoers scoping, threat model |
| [docs/development.md](docs/development.md) | Building, dev loop, testing |

## Layout

```
cmd/gopit    server entry point        internal/server/   HTTP API, store, node manager
cmd/gopitd     agent entry point         internal/agent/    ws, beacon, stats, docker, terminal, ufw
web/                 Vite frontend (vanilla JS, xterm.js bundled)
configs/             example YAMLs             install.sh         one-mode-at-a-time installer
```