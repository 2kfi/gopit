import { api } from '../api.js'
import { state, navigate } from '../main.js'
import { esc, maskToken, copyButton } from '../util.js'
import { statusDot } from './nodes.js'

const POLL_MS = 3000

// First-run wizard: shown once until a node comes online (or the user skips).
// Steps: pairing token, node install one-liner, discover + approve.
export function wizardView() {
  const el = document.createElement('div')
  el.innerHTML = `
    <div class="wizard-wrap">
      <div class="wizard-card panel">
        <div class="brand">gopit<span class="caret">▍</span></div>
        <p class="sub">Welcome — let's get your first node connected.</p>
        <div class="flash" id="wiz-flash"></div>

        <h3>1 · Pairing token</h3>
        <div class="pair-row">
          <code class="tok" id="wiz-token">…</code>
          <button class="btn btn-sm" id="wiz-show" hidden>Show</button>
        </div>
        <p class="hint" id="wiz-token-hint"></p>

        <h3>2 · Install the agent on a node</h3>
        <pre class="code-block" id="wiz-cmd"></pre>
        <div class="code-block-actions"></div>

        <h3>3 · Find &amp; approve your node</h3>
        <p class="hint" id="wiz-bcast-hint" hidden></p>
        <div class="row">
          <button class="btn btn-primary" id="wiz-discover">Discover</button>
        </div>
        <table class="table wiz-table" hidden>
          <thead><tr><th>Node</th><th>Address</th><th>Status</th><th></th></tr></thead>
          <tbody id="wiz-rows"></tbody>
        </table>
        <p class="hint dim">This wizard closes automatically once a node comes online.</p>

        <p class="form-error" id="wiz-error"></p>
        <div class="dialog-actions">
          <button class="btn btn-ghost btn-sm" id="wiz-skip">Skip for now</button>
        </div>
      </div>
    </div>
  `

  const flash = (msg) => {
    const f = el.querySelector('#wiz-flash')
    f.textContent = msg
    f.style.opacity = 1
    setTimeout(() => (f.style.opacity = 0), 3500)
  }
  const errEl = el.querySelector('#wiz-error')

  let token = ''
  let broadcast = true

  const renderRows = (nodes) => {
    const tbody = el.querySelector('#wiz-rows')
    const table = el.querySelector('.wiz-table')
    tbody.innerHTML = ''
    // Only pending/discovered nodes need the wizard's approve flow; online,
    // approved or offline nodes are handled on the Nodes page.
    const actionable = nodes.filter((n) => n.status === 'pending' || n.status === 'discovered')
    table.hidden = actionable.length === 0
    for (const n of actionable) {
      const tr = document.createElement('tr')
      tr.innerHTML = `
        <td>${statusDot(n.status).outerHTML} ${esc(n.hostname || n.ip)}</td>
        <td class="mono dim">${esc(n.ip)}:${n.port}</td>
        <td><span class="badge badge-dim">${esc(n.status)}</span></td>
        <td class="actions"><button class="btn btn-primary btn-sm" data-act="approve">Approve</button></td>`
      tr.querySelector('[data-act="approve"]').addEventListener('click', () => approve(n, tr))
      tbody.appendChild(tr)
    }
  }

  const approve = async (n, tr) => {
    const td = tr.querySelector('td.actions')
    try {
      await api.post(`/api/nodes/${n.id}/approve`)
      await loadNodes()
    } catch (err) {
      if (err.status === 400) {
        // tokenless discovered node: ask for the agent token inline
        td.innerHTML = `<input class="tok-input" placeholder="agent token" spellcheck="false">
          <button class="btn btn-sm btn-primary" id="wiz-token-go">Save</button>`
        td.querySelector('#wiz-token-go').addEventListener('click', async () => {
          const t = td.querySelector('input').value.trim()
          if (!t) return
          try {
            await api.post(`/api/nodes/${n.id}/token`, { token: t })
            await api.post(`/api/nodes/${n.id}/approve`)
            await loadNodes()
          } catch (e) {
            errEl.textContent = e.message
          }
        })
      } else {
        errEl.textContent = err.message
      }
    }
  }

  const loadNodes = async () => {
    const nodes = await api.get('/api/nodes')
    state.setNodes(nodes)
    renderRows(nodes)
  }

  const loadWizard = async () => {
    const w = await api.get('/api/wizard')
    if (w.done) {
      state.setWizardDone(true)
      navigate('#/nodes')
      return
    }
    token = w.token || ''
    broadcast = !!w.broadcast
    const tok = el.querySelector('#wiz-token')
    tok.textContent = maskToken(token)
    const hint = el.querySelector('#wiz-token-hint')
    if (!token) {
      hint.innerHTML = `<span class="badge badge-warn">no pairing token configured</span> Set <code>pairing_token</code> in the server config (<code>/etc/gopit/gopit.yaml</code>), then restart.`
    } else {
      hint.textContent = 'Also in /etc/gopitd/gopitd.yaml on the node.'
    }
    const show = el.querySelector('#wiz-show')
    show.hidden = !token
    show.textContent = 'Show'
    show.onclick = () => {
      const shown = tok.textContent !== token
      tok.textContent = shown ? maskToken(token) : token
      show.textContent = shown ? 'Show' : 'Hide'
    }
    const cmd = el.querySelector('#wiz-cmd')
    cmd.textContent = w.install_cmd || ''
    const actions = el.querySelector('.code-block-actions')
    actions.innerHTML = ''
    if (w.install_cmd) actions.appendChild(copyButton(w.install_cmd))
    const bh = el.querySelector('#wiz-bcast-hint')
    bh.hidden = broadcast
    bh.textContent = 'No non-loopback interface detected on this host — UDP broadcast discovery will not reach other machines. Use Manual Add on the Nodes page for remote nodes.'
    await loadNodes()
  }

  el.querySelector('#wiz-discover').addEventListener('click', async (e) => {
    const btn = e.currentTarget
    btn.disabled = true
    btn.textContent = 'Scanning…'
    try {
      const res = await api.post('/api/nodes/discover')
      flash(`Found ${res.found} node(s)`)
      await loadNodes()
    } catch (err) {
      errEl.textContent = err.message
    } finally {
      btn.disabled = false
      btn.textContent = 'Discover'
    }
  })

  el.querySelector('#wiz-skip').addEventListener('click', async () => {
    try {
      await api.post('/api/wizard/done')
    } catch {}
    state.setWizardDone(true)
  })

  loadWizard().catch((err) => (errEl.textContent = err.message))
  const timer = setInterval(() => loadWizard().catch(() => {}), POLL_MS)

  return {
    el,
    unmount() {
      clearInterval(timer)
    },
  }
}