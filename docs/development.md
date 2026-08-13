# Development

## Layout

```
cmd/gopit/     entry point, config load, HTTP + WS wiring
cmd/gopitd/      entry point, config load, service wiring
internal/server/       store (sqlite), sessions, node manager, hub, api
internal/agent/        ws server, beacon, stats, docker, terminal, ufw
internal/agent/system/ gopsutil wrappers (stats) + AgentVersion ldflag
embed.go               embeds web/dist into the server binary
web/                   Vite + vanilla JS frontend (xterm.js)
configs/               example configs (defaults for both binaries)
install.sh             server/agent installer (one mode at a time)
```

## Build

```bash
make build-all       # web/dist + both binaries -> bin/
make build-agent     # agent only (no web needed)
make build-server    # server (requires web built: make web/dist)
```

Agent version is stamped via `-ldflags "AGENT_VERSION=..."`; default `dev`.
No CGO anywhere (pure-Go sqlite), so `CGO_ENABLED=0 GOOS=... go build` works
for cross-compiling.

## Tests

```bash
make test            # go test ./...
make vet
```

Tests cover the protocol envelope, RPC dispatch, terminal session
validation, and store operations. These run against real processes where
possible (a local agent on :1221 for the terminal tests).

## Dev loop

Two processes, like production but hot:

```bash
make dev-server      # go run ./cmd/gopit -config configs/gopit.yaml
make dev-web         # vite dev server, proxies /api + /ws to :8080
```

Open `http://localhost:5173` (Vite's port). Any change in `web/` hot-reloads;
server changes need a restart.

For a local agent + server on one machine:

```bash
cp configs/gopitd.example.yaml /tmp/agent.yaml
# token: change-me must match pairing_token: "" in the server config —
# simplest: leave server pairing_token empty for local dev, or set both to
# the same string. Run the agent's beacon on the same network; discovery
# needs a broadcastable interface (or add the node manually at 127.0.0.1).
./bin/gopitd -config /tmp/agent.yaml
```

## Adding a method to the agent

1. Add the method to the RPC `switch` in `internal/agent/ws.go` (or the
   file with the method table) with a `xxx/client` struct param and
   `(xxx, error)` reply — the envelope takes care of correlation/errors.
2. Expose it in `internal/server/api.go` as `/api/nodes/:uuid/rpc` with a
   client-side helper.
3. Add a button/card in `web/` and a connection handler in `web/src/`.
4. `make test && make build-all`.

## Docs

Live files: `docs/*.md`; the README links them. Update `docs/architecture.md`
when the wire protocol changes — the method catalog is the contract both
sides compile against.