# Installation

One script installs **either** the server **or** the agent — one component at
a time, per machine:

```
install.sh server [--bin <path>] [--url <release-url>] [--port N]
install.sh agent   [--bin <path>] [--url <release-url>] [--port N] [--apply-ufw]
```

- no `--bin`/`--url` → builds from this repo (requires Go + `make`)
- `TOKEN=<x>` → presets the pairing token; if unset the server generates one
  and the agent prompts for it
- everything is idempotent: re-running updates the binary, restarts the
  service, and leaves config/certs/sudoers untouched

## Install the server (management machine)

```bash
sudo ./install.sh server
```

What it does:

1. creates the `gopit` system user
2. installs the binary to `/usr/local/bin/gopit`
3. writes `/etc/gopit/gopit.yaml` (skips if it exists —
   including the pairing token, which then must agree with what agents have)
4. creates `/var/lib/gopit/` for the SQLite DB
5. installs and starts `gopit.service`

You are prompted for the dashboard **admin username** (default `admin`) and
**password**, and the script prints the **pairing token** — copy it to the
node commands below.

> **First login:** the admin account is bootstrapped only when the DB is
> empty. Change the password from the dashboard (or delete/recreate the DB)
> after the first login.

## Install the agent (each node)

```bash
sudo TOKEN=<pairing-token> ./install.sh agent --apply-ufw
```

What it does:

1. creates the `gopitd` system user
2. installs the binary to `/usr/local/bin/gopitd`
3. writes `/etc/gopitd/gopitd.yaml` (skips if it exists; missing
   `tls_cert`/`tls_key` lines are appended so existing configs upgrade)
4. generates self-signed TLS certificates into `/etc/gopitd/tls/`
   (10 years, `CN=hostname`, SAN covers loopback + primary IP) — the server
   dials the agent over `wss://` automatically
5. writes `/etc/sudoers.d/gopit-node-agent` — see [Security](security.md)
   for the exact scope
6. installs and starts `gopitd.service`
7. `--apply-ufw` applies: `default deny incoming`, `default allow outgoing`,
   open `22/tcp` (SSH) and `<port>/tcp` (agent), then enables ufw

Without `--apply-ufw` nothing touches the firewall — open the agent port
yourself if needed. The agent **needs** an open TCP port for the server to
dial, and UDP 1221 for discovery.

Then, back on the server dashboard: **Nodes → Discover**, approve the node,
and the status flips to `online` within a couple of seconds.

## Network requirements (summary)

| Traffic | Source | Destination | Port |
|:--------|:-------|:------------|:-----|
| discovery probe | server | broadcast | 1221/UDP |
| announce reply | agent | server | 1221/UDP |
| control connection | server | agent | 1221/TCP |
| dashboard + API | browser / anything | server | 8080/TCP |

Discovery is a convenience — a node that misses it can be added manually by
IP. But agent → server traffic only flows after the server → agent control
connection exists, so TCP 1221 on every node is non-negotiable.

## Run without systemd (dev / containers)

```bash
make build-all
./bin/gopit -config configs/gopit.example.yaml
./bin/gopitd  -config configs/gopitd.example.yaml
```

Both binaries only need `-config <path>`; default config paths are the
`configs/*.example.yaml` files relative to the working directory.

## Uninstall

```bash
systemctl disable --now gopitd       # per node
rm /etc/systemd/system/gopitd.service
rm -r /etc/gopitd /usr/local/bin/gopitd /etc/sudoers.d/gopit-node-agent
userdel gopitd

systemctl disable --now gopit      # on the management machine
rm /etc/systemd/system/gopit.service
rm -r /etc/gopit /var/lib/gopit /usr/local/bin/gopit
userdel gopit
```