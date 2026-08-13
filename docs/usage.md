# Usage

Log in at `http://<server>:8080`. Entered admin credentials are verified
against the sessions table; the session cookie is HttpOnly + SameSite.

## Nodes

The landing view lists every known node with its status:

| Status | Meaning |
|:-------|:--------|
| `pending` | announced itself (or was added manually) but not yet approved — or approved with a token mismatch |
| `approved` | approved by you; the server is dialing / will dial it |
| `online` | authenticated connection up, stats flowing |
| `offline` | the control connection dropped or didn't answer |

(The UI also knows a legacy `discovered` state for nodes in the store that
predate approval tracking; discovery itself inserts nodes as `pending`.)

- **Discover** — sends a UDP probe; matching agents announce within ~2s.
- **Add Node** — manual join for nodes that can't hear broadcasts
  (IP + agent port + token check).
- **Approve** — pairs the node with the server's `pairing_token`.
  *Deleting* a node forgets it entirely.
- **Refresh** — re-polls status.
- **Name it** — each node gets a display name; it's local to the server DB.

Click a node (status ≥ `approved`) to open its detail views.

## Node detail

### Dashboard
Live-updating cards streamed over the control connection (1s interval):
CPU, memory, swap, disk usage, per-interface network rates, load average,
uptime, and system temps when available. Cards show history sparklines —
leave the tab open a minute and watch the shape.

### Docker
- **Containers** — table of all containers (running + stopped), click for
  full `docker inspect` JSON. Start / Stop / Remove buttons per container.
  Logs opens a streaming view with a follow/freeze toggle.
- **Images** — image inventory and removal.
- **Volumes** — volume inventory and removal.
- **Compose** — per-project stack list, `docker compose ps`, deploy
  (path or config on the node), and down.

### Terminal
A working shell on the node in your browser (xterm.js). Works like any
terminal: resize, paste, history — nothing special to configure.

**Root is blocked, on purpose.** If a command needs root, the terminal runs
`su -l` itself and prompts for the root password *inside* the terminal, so
root credentials never cross the wire. Where root is genuinely needed
non-interactively (e.g. starting a root-owned service), use the node's
`ufw` access instead — but `allow_toggle` is off by default.

### Firewall
- Current default policy + rule list (parsed from `ufw status`).
- Add / delete `allow` and `deny` rules (port + optional protocol + comment).
- Toggle enable/disable — **only visible when the agent config sets
  `ufw.allow_toggle: true`**, matching the narrow sudoers scope.

## Tips

- Stats and terminal use the same control connection — a second browser tab
  opens a second connection; fine.
- The server dials agents only after approval, so nodes never talk to the
  server on their own (except the UDP announce).