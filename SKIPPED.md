# Skipped Phases

## Phase 5.6: Web UI E2E (Playwright)
**Message:** skipped — no e2e infra exists and none can be installed cleanly.

The repo's `web/` has no e2e harness (package.json ships only vite + xterm), no
CI pipeline to run browsers, and `web/dist` is a committed build artifact that
the Go embed path serves. Installing Playwright would pull a browser download
into a repo with no runner for it (Phase 4 CI is itself skipped). Browser
flows are instead covered by the Go WS/integration tests in
`internal/server/` (status stream, terminal proxy, docker logs) at the wire
level, which is where the actual proxy logic lives. Revisit when Phase 4 lands
a CI pipeline.

## Phase 4: Tooling, CI/CD & Release Engineering
**Message:** not yet

| Task | Scope |
|------|-------|
| **4.1 GitHub Actions Pipeline** | `go test ./...`, `go vet`, `gosec`, `govulncheck`, `npm audit`, `vite build`, cross-compile matrix (linux/amd64, arm64). |
| **4.2 Dependency Automation** | Dependabot/Renovate config for Go (`go.mod`) + npm (`package.json`). Auto-PR with security labels. |
| **4.3 Release Process** | Semantic versioning (`vX.Y.Z`). `goreleaser` config: build binaries, sign (cosign), generate SBOM (Syft), create GitHub Release with checksums. |
| **4.4 Install Script Sync** | Update `install.sh` for new defaults (`tls_skip_verify: false`), add `--uninstall` mode (remove users, systemd, sudoers, configs, certs, firewall rules), validate config at install. |
| **4.5 Config Examples Updated** | `configs/gopit.example.yaml`, `gopitd.example.yaml` include all new fields (rate limits, log format, secrets paths, resource limits). |
| **4.6 Uninstall Mode** | `./install.sh uninstall server|agent` — clean removal, optional data purge flag. |

---

## Phase 6.6: Multi-Server JWT Sync (Design)
**Message:** not it's time yet

| Task | Scope |
|------|-------|
| **6.6 Multi-Server JWT Sync (Design)** | Redis-backed session store + rate limiter. Document leader election, shared DB, agent failover. Implementation in Phase 7. |

---

## Phase 7: Production-Grade Platform (Enterprise/Scale)
**Message:** not yet it's time

| Task | Scope |
|------|-------|
| **7.1 HA/Clustering Implementation** | Multi-server: Redis for sessions/rate-limit, shared SQLite (or PostgreSQL migration), agent connects to any server (round-robin DNS or load balancer). Leader election for discovery broadcaster. |
| **7.2 SELinux/AppArmor Profiles** | `gopit` profile: network bind 8080, DB read/write, cert read. `gopitd` profile: network bind 1221, netlink (CAP_NET_ADMIN), docker socket, pty, su/getent exec. |
| **7.3 Podman/Rootless Docker Support** | Detect `podman` or rootless `docker` socket (`$XDG_RUNTIME_DIR/docker.sock`). No `docker` group needed. |
| **7.4 IPv6 Validation** | Explicit test matrix: agent on IPv6-only, dual-stack, firewall rules apply to both families (nftfw `inet` table covers this). |
| **7.5 Distributed Tracing** | OpenTelemetry SDK: trace HTTP requests, WS proxy hops, agent method calls. Export to Jaeger/OTLP. |
| **7.6 Alerting Rules** | PrometheusRule CRDs: node offline > 2m, disk > 90%, CPU > 95%, agent reconnect storm, auth failure spike. |
| **7.7 Log Sampling** | Structured log sampling (tail-based) for high-volume paths (stats stream). Keep errors always. |