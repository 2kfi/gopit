// WS manager: connects to a node's status stream, auto-reconnects with
// exponential backoff, dispatches messages by envelope method.
const handlers = new Map() // method -> Set<fn>

export function on(method, fn) {
  if (!handlers.has(method)) handlers.set(method, new Set())
  handlers.get(method).add(fn)
  return () => handlers.get(method).delete(fn)
}

let ws = null
let uuid = null
let retries = 0
let opened = false // did onopen ever fire for the current socket
let closed = false
let retryTimer = null
let pingTimer = null

export function connect(nodeUuid) {
  if (ws && uuid === nodeUuid) return
  disconnect()
  uuid = nodeUuid
  closed = false
  open()
}

export function disconnect() {
  closed = true
  retries = 0
  uuid = null
  if (retryTimer) {
    clearTimeout(retryTimer)
    retryTimer = null
  }
  if (pingTimer) {
    clearInterval(pingTimer)
    pingTimer = null
  }
  if (ws) {
    ws.onclose = null
    ws.close()
    ws = null
  }
}

function open() {
  if (!uuid || closed) return
  opened = false
  const sock = new WebSocket(`${proto()}${location.host}/api/nodes/${uuid}/status`)
  ws = sock
  sock.onmessage = (ev) => {
    let msg
    try {
      msg = JSON.parse(ev.data)
    } catch {
      return
    }
    const fns = handlers.get(msg.method)
    if (fns) for (const fn of fns) fn(msg.payload, msg)
  }
  sock.onopen = () => {
    opened = true
    retries = 0
    // the server treats 60s of silence as a dead browser; ping so idle
    // dashboards stay live
    pingTimer = setInterval(() => {
      if (ws && ws.readyState === 1) ws.send('{}')
    }, 25000)
  }
  sock.onclose = () => {
    if (pingTimer) {
      clearInterval(pingTimer)
      pingTimer = null
    }
    if (closed || !uuid || ws !== sock) return
    if (!opened && retries >= 3) {
      ws = null
      console.warn(`status stream for ${uuid}: giving up after failed handshake`)
      return
    }
    const delay = Math.min(1000 * 2 ** retries, 30000)
    retries++
    retryTimer = setTimeout(open, delay)
    if (ws === sock) ws = null
  }
}

function proto() {
  return location.protocol === 'https:' ? 'wss://' : 'ws://'
}
