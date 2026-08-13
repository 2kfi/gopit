#!/usr/bin/env bash
# install.sh — install the Gopit SERVER (gopit) or the Gopit AGENT (gopitd) on this machine.
#
# Exactly ONE mode at a time:
#   ./install.sh server [--bin <path>] [--url <release-url>] [--port N]
#   ./install.sh agent   [--bin <path>] [--url <release-url>] [--port N] [--apply-ufw]
#
# Binary source: --bin <file> | --url <download> | build from this repo (run from
# a checkout with Go installed). TOKEN=secret exports a pairing token; server
# and agent must share it (or the server generates one at install time).
#
# Everything is idempotent: re-running re-installs the binary, restarts the
# service, and skips anything already in place (user, config, certs, sudoers).
set -euo pipefail

SERVICE_USER=
ETC_DIR=
VAR_DIR=
CONFIG=
SERVICE=
BIN_NAME=
BIN_PATH=
UFW_APPLY=0
PORT=0

log()  { printf '\033[1;32m[install]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[install]\033[0m %s\n' "$*"; }

usage() {
  sed -n '2,10p' "$0" | sed 's/^# \{0,1\}//'
  exit 1
}

if [[ $EUID -ne 0 ]]; then
  echo "error: run as root (sudo)" >&2
  exit 1
fi

MODE=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    server|agent)
      [[ -n "$MODE" ]] && { echo "error: pick ONE mode (server or agent)" >&2; usage; }
      MODE="$1"; shift ;;
    --bin) BIN_SRC="${2:-}"; shift 2 ;;
    --url) URL_SRC="${2:-}"; shift 2 ;;
    --port) PORT="${2:-}"; shift 2 ;;
    --apply-ufw) UFW_APPLY=1; shift ;;
    -h|--help) usage ;;
    *) echo "error: unknown argument: $1" >&2; usage ;;
  esac
done
if [[ -z "$MODE" ]]; then
  echo "error: pass a mode: server or agent (one at a time)" >&2
  usage
fi

if [[ -n "${BIN_SRC:-}" && -n "${URL_SRC:-}" ]]; then
  echo "error: pass --bin OR --url, not both" >&2
  exit 1
fi

# ---------------------------------------------------------------------------
# stage the binary: --bin, --url, or a local build from this checkout
# ---------------------------------------------------------------------------
stage_binary() {
  local tmp
  tmp=$(mktemp)
  trap 'rm -f "$tmp"' EXIT
  if [[ -n "${BIN_SRC:-}" ]]; then
    [[ -f "$BIN_SRC" ]] || { echo "error: --bin file not found: $BIN_SRC" >&2; exit 1; }
    install -m 0755 "$BIN_SRC" "$tmp"
  elif [[ -n "${URL_SRC:-}" ]]; then
    if ! curl -fsSL -o "$tmp" "$URL_SRC"; then
      echo "error: failed to download $URL_SRC" >&2
      exit 1
    fi
    chmod 0755 "$tmp"
  else
    if [[ ! -f Makefile || -z "$(command -v go)" ]]; then
      echo "error: no --bin/--url given, and this is not a buildable repo (Makefile + go required)" >&2
      exit 1
    fi
    make "build-$1" >/dev/null
    install -m 0755 "bin/$1" "$tmp"
  fi
  install -m 0755 "$tmp" "$BIN_PATH"
  log "installed binary -> $BIN_PATH"
}

# ---------------------------------------------------------------------------
# MODE: SERVER
# ---------------------------------------------------------------------------
install_server() {
  SERVICE_USER=gopit
  ETC_DIR=/etc/gopit
  VAR_DIR=/var/lib/gopit
  CONFIG=$ETC_DIR/gopit.yaml
  SERVICE=gopit.service
  BIN_NAME=gopit
  BIN_PATH=/usr/local/bin/gopit
  [[ "$PORT" -eq 0 ]] && PORT=8080

  if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
    log "created system user $SERVICE_USER"
  else
    log "system user $SERVICE_USER exists"
  fi
  install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$ETC_DIR"
  install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$VAR_DIR"
  stage_binary gopit

  # --- config: write only when missing; never clobber operator edits -------
  if [[ ! -f "$CONFIG" ]]; then
    local PAIRING_TOKEN="${TOKEN:-}"
    if [[ -z "$PAIRING_TOKEN" ]]; then
      PAIRING_TOKEN=$(openssl rand -hex 16 2>/dev/null || cat /proc/sys/kernel/random/uuid)
    fi
    local ADMIN_USER ADMIN_PASS
    read -r -p "Dashboard admin username [admin]: " ADMIN_USER
    ADMIN_USER=${ADMIN_USER:-admin}
    while true; do
      read -rsp "Dashboard admin password (min 6 chars): " ADMIN_PASS; echo
      [[ ${#ADMIN_PASS} -ge 6 ]] && break
      echo "error: password too short" >&2
    done
    # single-quote YAML quoting: embedded single quotes become '' (YAML escape)
    cat >"$CONFIG" <<EOF
listen_addr: ":$PORT"
db_path: $VAR_DIR/gopit.db
jwt_secret: ""                # auto-generated and persisted on first run
admin_username: '${ADMIN_USER//\'/\'\'}'   # bootstrap admin, created only when the DB is empty
admin_password: '${ADMIN_PASS//\'/\'\'}'   # change after first login
discovery_broadcast_addr: 255.255.255.255
discovery_port: 1221
tls_skip_verify: true         # accept agent self-signed certs
pairing_token: $PAIRING_TOKEN # share with agents: TOKEN=$PAIRING_TOKEN ./install.sh agent
EOF
    chown "$SERVICE_USER":"$SERVICE_USER" "$CONFIG"
    chmod 0640 "$CONFIG"
    log "wrote $CONFIG"
    warn "pairing token: $PAIRING_TOKEN (set TOKEN=... when installing agents)"
  else
    warn "existing $CONFIG kept"
  fi

  # --- systemd -------------------------------------------------------------
  if [[ ! -f "/etc/systemd/system/$SERVICE" ]]; then
    cat >"/etc/systemd/system/$SERVICE" <<EOF
[Unit]
Description=Gopit server (web dashboard)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
ExecStart=$BIN_PATH -config $CONFIG
Restart=always
RestartSec=5
ProtectSystem=strict
ReadWritePaths=$VAR_DIR
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    log "installed $SERVICE"
  fi

  systemctl enable "$SERVICE" >/dev/null 2>&1
  systemctl restart "$SERVICE"
  log "gopit.service enabled + running"
  sleep 1
  if ! systemctl is-active --quiet "$SERVICE"; then
    warn "service failed to start; inspect: journalctl -u $SERVICE"
  fi

  log "done. open http://$(hostname -I 2>/dev/null | awk '{print $1}'):$PORT and log in with the admin credentials above."
}

# ---------------------------------------------------------------------------
# MODE: AGENT
# ---------------------------------------------------------------------------
install_agent() {
  SERVICE_USER=gopitd
  ETC_DIR=/etc/gopitd
  TLS_DIR=$ETC_DIR/tls
  CONFIG=$ETC_DIR/gopitd.yaml
  SERVICE=gopitd.service
  SUDOERS=/etc/sudoers.d/gopit-node-agent
  BIN_NAME=gopitd
  BIN_PATH=/usr/local/bin/gopitd
  [[ "$PORT" -eq 0 ]] && PORT=1221

  if ! id "$SERVICE_USER" >/dev/null 2>&1; then
    useradd --system --no-create-home --shell /usr/sbin/nologin "$SERVICE_USER"
    log "created system user $SERVICE_USER"
  else
    log "system user $SERVICE_USER exists"
  fi

  install -d -m 0755 -o "$SERVICE_USER" -g "$SERVICE_USER" "$ETC_DIR"
  install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" "$TLS_DIR"
  stage_binary gopitd

  # --- pairing token -------------------------------------------------------
  local TOKEN_VALUE=""
  if [[ -n "${TOKEN:-}" ]]; then
    TOKEN_VALUE="${TOKEN}"
  elif [[ -f "$CONFIG" ]] && grep -q '^token:' "$CONFIG"; then
    TOKEN_VALUE=$(sed -n 's/^token:[[:space:]]*//p' "$CONFIG" | head -1)
    warn "reusing token from existing $CONFIG"
  else
    read -rsp "Pairing token (shared with the gopit server): " TOKEN_VALUE </dev/tty
    echo
    [[ -n "$TOKEN_VALUE" ]] || { echo "error: token must not be empty" >&2; exit 1; }
  fi

  # --- TLS certificates (3650 days, CN=hostname, SAN=loopback+primary IP) ---
  local HOSTNAME PRIMARY_IP
  HOSTNAME=$(hostname)
  PRIMARY_IP=$(ip route get 1.1.1.1 2>/dev/null | sed -n 's/.* src \([0-9.]*\) .*/\1/p')
  [[ -z "$PRIMARY_IP" ]] && PRIMARY_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
  [[ -z "$PRIMARY_IP" ]] && PRIMARY_IP=127.0.0.1

  if [[ -f "$TLS_DIR/cert.pem" && -f "$TLS_DIR/key.pem" ]]; then
    log "TLS certs exist; keeping $TLS_DIR/{cert.pem,key.pem}"
  else
    openssl req -x509 -newkey rsa:2048 -nodes -days 3650 \
      -keyout "$TLS_DIR/key.pem" -out "$TLS_DIR/cert.pem" \
      -subj "/CN=$HOSTNAME" \
      -addext "subjectAltName=IP:127.0.0.1,IP:$PRIMARY_IP" \
      >/dev/null 2>&1
    chown -R root:"$SERVICE_USER" "$TLS_DIR"
    chmod 0644 "$TLS_DIR/cert.pem"
    chmod 0640 "$TLS_DIR/key.pem"
    log "generated self-signed TLS certs (CN=$HOSTNAME, SAN=IP:127.0.0.1,IP:$PRIMARY_IP)"
  fi

  # --- config: write template if missing, else merge the TLS references ----
  if [[ ! -f "$CONFIG" ]]; then
    cat >"$CONFIG" <<EOF
listen_addr: 0.0.0.0
port: $PORT
token: $TOKEN_VALUE
uuid_path: $ETC_DIR/node.id
stats_interval_seconds: 1
tls_cert: $TLS_DIR/cert.pem
tls_key: $TLS_DIR/key.pem
ufw:
  binary_path: /usr/sbin/ufw
  allow_toggle: false
EOF
    chown "$SERVICE_USER":"$SERVICE_USER" "$CONFIG"
    chmod 0640 "$CONFIG"
    log "wrote $CONFIG"
  else
    [[ -f "$CONFIG" ]] && ! grep -q '^tls_cert:' "$CONFIG" \
      && printf 'tls_cert: %s\ntls_key: %s\n' "$TLS_DIR/cert.pem" "$TLS_DIR/key.pem" >>"$CONFIG"
    chown "$SERVICE_USER":"$SERVICE_USER" "$CONFIG" 2>/dev/null || true
    chmod 0640 "$CONFIG" 2>/dev/null || true
    warn "existing $CONFIG kept; appended tls_cert/tls_key if they were missing"
  fi

  # --- sudoers: scoped ufw access, no blanket NOPASSWD ---------------------
  if [[ -f "$SUDOERS" ]]; then
    log "sudoers $SUDOERS already present"
  else
    cat >"$SUDOERS" <<EOF
# gopit agent: ufw operations only (no blanket root)
$SERVICE_USER ALL=(root) NOPASSWD: /usr/sbin/ufw status*, /usr/sbin/ufw allow*, /usr/sbin/ufw delete*, /usr/sbin/ufw default*, /usr/sbin/ufw enable, /usr/sbin/ufw disable
EOF
    chmod 0440 "$SUDOERS"
    visudo -cf "$SUDOERS" >/dev/null
    log "installed $SUDOERS"
  fi

  # --- systemd -------------------------------------------------------------
  if [[ ! -f "/etc/systemd/system/$SERVICE" ]]; then
    cat >"/etc/systemd/system/$SERVICE" <<EOF
[Unit]
Description=Gopit node agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
ExecStart=$BIN_PATH -config $CONFIG
Restart=always
RestartSec=5
# NoNewPrivileges=yes is intentionally NOT set: ufw integration runs sudo -n.
ProtectSystem=strict
ReadWritePaths=$ETC_DIR
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    log "installed $SERVICE"
  fi

  # --- default firewall policy (only with --apply-ufw) ---------------------
  if [[ "$UFW_APPLY" == "1" ]]; then
    if command -v ufw >/dev/null 2>&1 || [[ -x /usr/sbin/ufw ]]; then
      ufw default deny incoming
      ufw default allow outgoing
      ufw allow 22/tcp comment "SSH"
      ufw allow "$PORT"/tcp comment "Gopit Agent"
      ufw --force enable
      log "applied default ufw policy (deny incoming, allow outgoing, 22 + $PORT/tcp)"
    else
      warn "ufw not installed; skipping firewall policy"
    fi
  else
    warn "not touching the firewall; pass --apply-ufw to apply the default policy"
  fi

  systemctl enable "$SERVICE" >/dev/null 2>&1
  systemctl restart "$SERVICE"
  log "gopitd.service enabled + running"
  sleep 1
  if ! systemctl is-active --quiet "$SERVICE"; then
    warn "service failed to start; inspect: journalctl -u $SERVICE"
  fi

  log "done. on the gopit server: Nodes -> Discover -> Approve this node."
}

case "$MODE" in
  server) install_server ;;
  agent) install_agent ;;
esac
