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
let closed = false

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
  if (ws) {
    ws.onclose = null
    ws.close()
    ws = null
  }
}

function open() {
  if (!uuid || closed) return
  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  ws = new WebSocket(`${proto}://${location.host}/api/nodes/${uuid}/status`)
  ws.onmessage = (ev) => {
    let msg
    try {
      msg = JSON.parse(ev.data)
    } catch {
      return
    }
    const fns = handlers.get(msg.method)
    if (fns) for (const fn of fns) fn(msg.payload, msg)
  }
  ws.onopen = () => (retries = 0)
  ws.onclose = () => {
    ws = null
    if (closed || !uuid) return
    const delay = Math.min(1000 * 2 ** retries, 30000)
    retries++
    setTimeout(open, delay)
  }
}
