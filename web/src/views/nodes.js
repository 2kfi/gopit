import { api } from '../api.js'
import { state, navigate } from '../main.js'
import { esc } from '../util.js'

const STATUS_COLORS = {
  online: 'ok',
  offline: 'bad',
  approved: 'accent',
  pending: 'warn',
  discovered: 'dim',
}

export function statusDot(status) {
  const dot = document.createElement('span')
  dot.className = `status-dot ${STATUS_COLORS[status] || 'dim'}`
  dot.title = status
  return dot
}

function nodeRow(n) {
  const row = document.createElement('tr')
  row.dataset.uuid = n.id
  row.innerHTML = `
    <td>
      <a href="#/node/${esc(n.id)}/dashboard" class="node-link">
        ${esc(n.hostname || n.ip)}<span class="node-id">${esc(n.id.slice(0, 8))}</span>
      </a>
    </td>
    <td class="mono">${esc(n.ip)}:${n.port}</td>
    <td class="mono dim">${esc(n.os)} / ${esc(n.arch)}</td>
    <td class="mono dim">${esc(n.agent_version || '—')}</td>
    <td><span class="badge badge-${STATUS_COLORS[n.status] || 'dim'}">${esc(n.status)}</span></td>
    <td class="actions">${actionsFor(n)}</td>
  `
  row.querySelectorAll('[data-act]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      btn.disabled = true
      try {
        await api.post(`/nodes/${n.id}/approve`)
        await refresh()
      } catch (err) {
        if (err.status === 400) openTokenDialog(n.id, err.message)
        else flash(err.message)
      }
    })
  })
  return row
}

function actionsFor(n) {
  if (n.status === 'pending' || n.status === 'discovered') {
    return `<button class="btn btn-primary btn-sm" data-act="approve">Approve</button>`
  }
  return ''
}

let tableBody
let flashEl

// Pairing dialog: discovered nodes carry no token (the server no longer hands
// the fleet token to UDP announcers), so the operator pastes the node's agent
// token here before approval can dial out.
let tokenDialog = null
let tokenTarget = null
let tokenReason = null

function openTokenDialog(id, reason) {
  tokenTarget = id
  tokenReason = reason || 'This node was discovered without a token.'
  tokenDialog.querySelector('#token-reason').textContent = tokenReason
  tokenDialog.querySelector('#token-error').textContent = ''
  tokenDialog.querySelector('input').value = ''
  tokenDialog.showModal()
  tokenDialog.querySelector('input').focus()
}

function flash(msg) {
  if (flashEl) {
    flashEl.textContent = msg
    flashEl.style.opacity = 1
    setTimeout(() => (flashEl.style.opacity = 0), 3000)
  }
}

export async function refresh() {
  if (!tableBody) return
  const nodes = await api.get('/nodes')
  state.setNodes(nodes)
  tableBody.innerHTML = ''
  if (nodes.length === 0) {
    tableBody.innerHTML = `
      <tr><td colspan="6" class="empty">
        No nodes yet. Hit <strong>Discover</strong> to scan the network, or add one manually.
      </td></tr>`
    return
  }
  nodes.forEach((n) => tableBody.appendChild(nodeRow(n)))
}

export function nodesView() {
  const el = document.createElement('div')
  el.innerHTML = `
    <header class="page-head">
      <div>
        <h1>Nodes</h1>
        <p class="sub">Machines running gopitd. Approve them to start streaming metrics.</p>
      </div>
      <div class="head-actions">
        <button class="btn" id="add-node">Add node</button>
        <button class="btn btn-primary" id="discover">Discover</button>
      </div>
    </header>
    <div class="flash" id="flash"></div>
    <div class="panel terminal">
      <div class="term-bar"><span></span><span></span><span></span>gopit / nodes</div>
      <table class="table">
        <thead><tr>
          <th>Node</th><th>Address</th><th>Platform</th><th>Agent</th><th>Status</th><th></th>
        </tr></thead>
        <tbody id="rows"></tbody>
      </table>
    </div>
    <dialog id="add-dialog" class="dialog">
      <form method="dialog" class="dialog-body">
        <h2>Add node</h2>
        <label>IP <input name="ip" required placeholder="10.0.0.5"></label>
        <label>Port <input name="port" type="number" value="1221" required></label>
        <label>Hostname <input name="hostname" placeholder="optional"></label>
        <label>Token <input name="token" required placeholder="agent pairing token"></label>
        <div class="dialog-actions">
          <button class="btn" value="cancel">Cancel</button>
          <button class="btn btn-primary" value="add">Add</button>
        </div>
      </form>
    </dialog>
    <dialog id="token-dialog" class="dialog">
      <form class="dialog-body">
        <h2>Pair node</h2>
        <p class="sub dim" style="margin:0" id="token-reason"></p>
        <label>Agent token
          <input name="token" required placeholder="token from the node's gopitd config" spellcheck="false">
        </label>
        <p class="form-error" id="token-error"></p>
        <div class="dialog-actions">
          <button class="btn" type="button" id="token-cancel">Cancel</button>
          <button class="btn btn-primary" id="token-go">Save &amp; approve</button>
        </div>
      </form>
    </dialog>
  `
  tableBody = el.querySelector('#rows')
  flashEl = el.querySelector('#flash')

  const dialog = el.querySelector('#add-dialog')
  tokenDialog = el.querySelector('#token-dialog')
  tokenDialog.querySelector('#token-cancel').addEventListener('click', () => tokenDialog.close())
  tokenDialog.querySelector('form').addEventListener('submit', async (e) => {
    e.preventDefault()
    const f = new FormData(e.currentTarget)
    const token = f.get('token').trim()
    if (!token) return
    const go = tokenDialog.querySelector('#token-go')
    const errEl = tokenDialog.querySelector('#token-error')
    go.disabled = true
    errEl.textContent = ''
    try {
      await api.post(`/nodes/${tokenTarget}/token`, { token })
      await api.post(`/nodes/${tokenTarget}/approve`)
      tokenDialog.close()
      await refresh()
    } catch (err) {
      errEl.textContent = err.message
    } finally {
      go.disabled = false
    }
  })
  tokenDialog.addEventListener('close', () => {
    tokenTarget = null
    tokenReason = null
  })
  el.querySelector('#discover').addEventListener('click', async (e) => {
    const btn = e.currentTarget
    btn.disabled = true
    btn.textContent = 'Scanning…'
    try {
      const res = await api.post('/nodes/discover')
      flash(`Found ${res.found} node(s)`)
      await refresh()
    } catch (err) {
      flash(err.message)
    } finally {
      btn.disabled = false
      btn.textContent = 'Discover'
    }
  })
  el.querySelector('#add-node').addEventListener('click', () => dialog.showModal())
  dialog.querySelector('form').addEventListener('submit', async (e) => {
    const f = new FormData(e.currentTarget)
    const body = {
      ip: f.get('ip'),
      port: Number(f.get('port')),
      hostname: f.get('hostname') || f.get('ip'),
      token: f.get('token'),
    }
    if (!body.ip || !body.port || !body.token) {
      flash('ip, port and token are required')
      return
    }
    try {
      await api.post('/nodes', body)
      dialog.close()
      await refresh()
    } catch (err) {
      flash(err.message)
    }
  })

  const timer = setInterval(() => refresh().catch(() => {}), 5000)
  refresh().catch((err) => flash(err.message))

  return {
    el,
    mount() {
      el.querySelectorAll('.node-link').forEach((a) => {
        a.addEventListener('click', () => navigate(a.hash))
      })
    },
    unmount() {
      clearInterval(timer)
      tableBody = null
    },
  }
}
