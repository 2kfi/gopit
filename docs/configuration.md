# Configuration

Both binaries take `-config <path>` and default to the example files in
`configs/`.

## gopit (configs/gopit.example.yaml)

```yaml
listen_addr: ":8080"              # HTTP/WS bind: ":8080" or "127.0.0.1:8080"
db_path: gopit.db                # SQLite file; schema auto-created
jwt_secret: ""                    # session signing key. "" = auto-generate
                                  # into the settings table on first boot
admin_username: admin             # bootstrap admin (only used when the DB is empty)
admin_password: change-me         # ...and only then. Change after first login.
discovery_broadcast_addr: 255.255.255.255  # where the discovery probe is sent
discovery_port: 1221              # UDP port agents listen for it on
tls_skip_verify: true             # accept agents' self-signed certs
pairing_token: ""                 # must match the token on every agent
password_min_score: 2             # minimum password strength 0-4 (zxcvbn)
webhooks:
  - url: https://hooks.example.com/gopit
    secret: change-me
    events: [node.up, node.down]  # empty = all events
```

- `jwt_secret` — leave empty on first run; the server generates one and
  persists it in `settings`, so sessions survive restarts. Centralized
  multi-server setups can pin it.
- `pairing_token` — the default token assigned to nodes without one at
  approval time; agents installed with `TOKEN=<pairing_token>` match
  automatically.
- `password_min_score` — minimum zxcvbn strength (0-4, default 2) enforced on
  user creation and password changes. The login screen shows a matching
  strength meter.
- `webhooks` — optional signed HTTP callbacks. Each configured hook is a
  `url` + `secret` (shared HMAC key) + `events` allowlist (empty = all).
  Events: `node.up`, `node.down`, `firewall.changed`, `docker.deployed`,
  `user.login`, `user.created`, `user.deleted`, `password.changed`. Every
  delivery is a POST with body `{"event","timestamp","payload"}` and the hex
  HMAC-SHA256 of the raw body in `X-Gopit-Signature`; retried 3 times with
  linear backoff. Verify with:
  `test "$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" -hex | awk '{print $2}')" = "$SIGNATURE"`.
- Bootstrap admin: when the `admin_username`/`admin_password` fields are set
  and the DB is empty, that account is created at boot. Deleting `gopit.db`
  on a fresh setup re-bootstraps it from config. Passwords are hashed
  (bcrypt) in the DB.

## gopitd (configs/gopitd.example.yaml)

```yaml
listen_addr: 0.0.0.0              # IP the WS + UDP beacon bind to
port: 1221                        # TCP (WS) and UDP (beacon) port
token: change-me                  # pre-shared pairing token; must equal the
                                  # server's pairing_token
uuid_path: /etc/gopitd/node.id  # file the agent reads/creates its UUID in
stats_interval_seconds: 1         # how often system.stats is pushed
tls_cert: /etc/gopitd/tls/cert.pem   # set BOTH to serve wss://
tls_key:  /etc/gopitd/tls/key.pem
ufw:
  binary_path: /usr/sbin/ufw      # absolute path of the ufw binary
  allow_toggle: false             # permit ufw enable/disable via the API
terminal:
  record: false                   # write a ttyrec per terminal session
  recording_dir: /var/lib/gopitd/recordings
```

- `tls_cert`/`tls_key` — optional; with both set the agent announces
  `tls: true` and the server dials `wss://` (respecting `tls_skip_verify`
  on its side). Empty = plaintext `ws://`.
- `firewall` — `nftfw` (default): kernel-native via netlink, needs
  `CAP_NET_ADMIN`; `ufw`: legacy sudo integration. Both expose the same
  `ufw.*` methods.
- `ufw.allow_toggle` — `ufw.toggle` is refused unless `true`. Rule add/delete
  and status always work when ufw is installed and the sudoers entry exists
  (ufw mode only).
- `stats_interval_seconds` — the push interval; values <= 0 fall back to 1s.
- `terminal.record` — off by default. When on, every PTY session is written
  as a ttyrec file to `<recording_dir>/<node_id>/<UTC timestamp>.ttyrec`
  (default `/var/lib/gopitd/recordings`). Recordings contain everything the
  shell printed, including echoed input, and are readable with the standard
  tool `ttyplay` (Debian/Ubuntu: `apt install ttyrec`):

  ```sh
  ttyplay /var/lib/gopitd/recordings/<node_id>/20260814T103000Z.ttyrec
  ```

  If the directory cannot be created (e.g. a rootless agent), the agent logs
  a warning and sessions continue unrecorded.

## Environment / CLI

| Binary | Flag | Meaning |
|:-------|:-----|:--------|
| both | `-config <path>` | config file to load (default: `configs/*.example.yaml` in cwd) |

`bin/gopit* -config /nonexistent` prints the attempted path — handy as a
smoke test after installing.