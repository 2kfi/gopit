# Security

Short version: nothing is exposed to the world by default, approval gates
everything, root never crosses the wire.

## Trust model (defence in depth)

| Layer | Control |
|:------|:--------|
| discovery | announces carry no credentials; the agent never connects out |
| approval | a node is useless until an operator approves it — even with a valid token |
| pairing | every control connection must present the shared token (`auth` method) |
| transport | agent TLS (wss) made default by the installer; plain ws only if certs are omitted |
| sessions | dashboard sessions are JWT cookies — HttpOnly, SameSite |
| privilege | the agent runs as `gopitd`; sudo is only for ufw, and only the *exact* commands below |

## The token

A single 256-bit pairing token (installer: `openssl rand -hex 16`) is shared
between the server config (`pairing_token`) and every agent (`token`). It is
the cryptographic boundary: anyone holding it can talk to an agent directly
or register nodes on a server. Treat it like a root password:

- never in version control — the installer writes it 0600-owned (0640) into
  `/etc/*/gopit*.yaml`
- rotate by changing both sides (agents keep running with their old token
  until restarted)

> **Wire caveat:** with TLS disabled the token travels in the first
> WebSocket message as-is. Use the installer, which enables TLS by default.
> LAN-only deployments may accept `ws://`; internet-exposed agents should
> not.

## Certificate verification

Agent certs are self-signed, and the server defaults to
`tls_skip_verify: true` — that defends against *passive* sniffing, not
against a MITM who can intercept the dial and impersonate the agent with
their own cert. For LAN management this is the accepted trade; the knob
exists to tighten it (pin the agent cert on the server side).

## Firewall privilege model

The default backend is `nftfw`: the agent manages the firewall directly via
netlink and needs only `CAP_NET_ADMIN` (the systemd unit ships
`AmbientCapabilities=CAP_NET_ADMIN` + a bounded capability set) — no root,
no sudo, no daemon.

Legacy `ufw` mode (`firewall: ufw` in the agent config) shells out via
scoped sudo. `install.sh agent` writes `/etc/sudoers.d/gopit-node-agent`:

```
gopitd ALL=(root) NOPASSWD: /usr/sbin/ufw status*, /usr/sbin/ufw allow*,
    /usr/sbin/ufw deny*, /usr/sbin/ufw reject*, /usr/sbin/ufw --force delete*,
    /usr/sbin/ufw --force enable, /usr/sbin/ufw default*, /usr/sbin/ufw disable
```

- No blanket `ALL`. No shell access. No other binaries.
- `ufw enable`/`disable` are callable **only if the agent config sets
  `ufw.allow_toggle: true`** — even though sudoers permits the command, the
  agent refuses to run it by default.
- Every ufw invocation is `sudo -n` (no password, non-interactive), and the
  agent validates the binary path from config before exec.

## Terminal: root is blocked

- The agent runs as `gopitd`; a terminal opened from the dashboard gets
  that user's shell. `root` sessions are refused outright.
- Needing root in a terminal is handled by the agent: it starts `su -l`
  inside the PTY, so the root password is typed into the PTY on the node and
  never appears in the payloads the server/browser relays.
- No background shells are left behind: closing the connection tears the
  PTY process group down.

## Dashboard auth

- Bootstrap admin is created from `admin_username`/`admin_password` **only
  when the DB is empty**; passwords are bcrypt-hashed.
- Subsequent logins validate against the sessions table. Sessions are JWT
  (secret auto-generated into `settings`), set as HttpOnly + SameSite
  cookies, so day-to-day dashboard access never leaves the browser's cookie
  jar. The cookie gets the `Secure` flag automatically when the request is
  HTTPS or comes through an `X-Forwarded-Proto: https` proxy.
- Failed logins are throttled per IP (10 failures per 15 minutes → 429);
  successful logins don't count, so users behind a shared NAT don't lock
  each other out. The limiter is single-process — pin `jwt_secret` and
  front it with nginx/Redis rate limiting for multi-server setups.
- Every non-GET `/api` request (login, firewall, docker, users, …) is
  written to the server log: method, path, user, status, duration. Bodies
  and the pairing token are never logged; `terminal.open` is logged with
  the node and the *login user* — never the password.
- State-changing requests are additionally checked for a cross-site
  `Origin` header (CSRF): a browser `Origin` that doesn't match the server
  host is rejected with 403. Clients that send no `Origin` (curl, agents)
  pass — full protection would need a CSRF token.
- A deleted user's sessions die immediately: every request re-validates the
  JWT's user against the store, not just the signature.

## Keepalive and dead connections

- The server pings agents every 30s; the agent treats 90s of silence as a
  dead peer and tears down the connection (and any open terminal session).
- On the dashboard side, the browser pings the status stream every 25s; the
  server pings terminal and docker-log sockets (browsers auto-pong) so a
  half-open browser dies within ~90s even on sockets that carry no traffic.

## Known limits / honest caveats

1. Server exposes plain HTTP by default (your dashboard should sit behind a
   reverse proxy with TLS — only the *agent* side has TLS built in).
2. `tls_skip_verify: true` as discussed above.
3. The agent listens on `0.0.0.0` — peer identity is the token alone; keep
   agents behind your firewall / WG / LAN and out of the public internet.
4. UFW rules apply but no changes are made to `/etc/ufw` without the
   installer's `--apply-ufw` — the agent only *lists* and *manages* what's
   already configured.