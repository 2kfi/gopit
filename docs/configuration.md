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
```

- `jwt_secret` — leave empty on first run; the server generates one and
  persists it in `settings`, so sessions survive restarts. Centralized
  multi-server setups can pin it.
- `pairing_token` — set it here *and* on every agent, or let the installer
  do it for you.
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
```

- `tls_cert`/`tls_key` — optional; with both set the agent announces
  `tls: true` and the server dials `wss://` (respecting `tls_skip_verify`
  on its side). Empty = plaintext `ws://`.
- `ufw.allow_toggle` — `ufw.toggle` is refused unless `true`. Rule add/delete
  and status always work when ufw is installed and the sudoers entry exists.
- `stats_interval_seconds` — 0 or 1 disables the periodic push in favour of
  on-request stats only.

## Environment / CLI

| Binary | Flag | Meaning |
|:-------|:-----|:--------|
| both | `-config <path>` | config file to load (default: `configs/*.example.yaml` in cwd) |

`bin/gopit* -config /nonexistent` prints the attempted path — handy as a
smoke test after installing.