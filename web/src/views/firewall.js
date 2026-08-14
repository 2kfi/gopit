import { api } from '../api.js'
import { state } from '../main.js'
import { nodeTabs } from '../components/node-tabs.js'
import { esc } from '../util.js'

const actionColor = { ALLOW: 'ok', DENY: 'bad', REJECT: 'warn', LIMIT: 'warn' }

const CONFIG_DOCS_URL = 'https://github.com/2kfi/gopit/blob/main/docs/configuration.md'

export function firewallView({ uuid }) {
  const node = state.nodes.find((n) => n.id === uuid) || {}
  const el = document.createElement('div')
  el.innerHTML = `
    <header class="page-head">
      <div>
        <h1 class="mono">${esc(node.hostname || uuid.slice(0, 8))}</h1>
        <p class="sub mono dim">firewall / ufw rule management</p>
      </div>
    </header>
    ${nodeTabs(uuid, 'firewall').outerHTML}
    <div class="flash" id="flash"></div>

    <section>
      <div class="panel terminal">
        <div class="term-bar"><span></span><span></span><span></span>status</div>
        <div class="fw-bar" id="fw-bar"></div>
        <div class="term-bar"><span></span><span></span><span></span>rules</div>
        <table class="table">
          <thead><tr><th>#</th><th>To</th><th>Action</th><th>From</th><th>Dir</th><th>Iface</th><th></th></tr></thead>
          <tbody id="rows-rules"></tbody>
        </table>
      </div>
    </section>

    <section>
      <div class="panel terminal">
        <div class="term-bar"><span></span><span></span><span></span>add rule</div>
        <form class="fw-form" id="rule-form">
          <label>Protocol
            <select name="protocol">
              <option value="tcp" selected>TCP</option>
              <option value="udp">UDP</option>
              <option value="both">Both</option>
            </select></label>
          <label>Port
            <input name="port" type="number" min="1" max="65535" required placeholder="3999"></label>
          <label>Action
            <select name="action">
              <option value="allow" selected>ALLOW</option>
              <option value="deny">DENY</option>
              <option value="reject">REJECT</option>
            </select></label>
          <label>From
            <input name="from" value="any" spellcheck="false"></label>
          <label>Interface (optional)
            <input name="interface" placeholder="eth0" spellcheck="false"></label>
          <button class="btn" type="button" id="rule-preview">Preview</button>
          <button class="btn btn-primary" id="rule-add">Add rule</button>
        </form>
        <p class="form-error" id="rule-error"></p>
        <div class="hidden" id="preview-wrap">
          <div class="term-bar"><span></span><span></span><span></span>preview: state after applying this rule</div>
          <table class="table">
            <thead><tr><th>#</th><th>To</th><th>Action</th><th>From</th><th>Dir</th><th>Iface</th></tr></thead>
            <tbody id="rows-preview"></tbody>
          </table>
        </div>
      </div>
    </section>
  `

  const flashEl = el.querySelector('#flash')
  const flash = (msg) => {
    flashEl.textContent = msg
    flashEl.style.opacity = 1
    setTimeout(() => (flashEl.style.opacity = 0), 3500)
  }

  const load = async () => {
    const rows = el.querySelector('#rows-rules')
    let st
    try {
      st = await api.get(`/api/nodes/${uuid}/firewall`)
    } catch (err) {
      renderStatus({ error: err.message })
      rows.innerHTML = `<tr><td colspan="7" class="empty">${esc(err.message)}</td></tr>`
      return
    }
    renderStatus(st)
    rows.innerHTML = ''
    if (!st.rules || st.rules.length === 0) {
      rows.innerHTML = `<tr><td colspan="7" class="empty">No firewall rules.</td></tr>`
      return
    }
    for (const r of st.rules) {
      const tr = document.createElement('tr')
      tr.innerHTML = `
        <td class="mono dim">${esc(r.number)}</td>
        <td class="mono">${esc(r.to)}</td>
        <td><span class="badge badge-${actionColor[r.action] || 'dim'}">${esc(r.action)}</span></td>
        <td class="mono dim">${esc(r.from)}</td>
        <td class="mono dim">${esc(r.direction)}</td>
        <td class="mono dim">${esc(r.interface || '—')}</td>
        <td class="actions"><button class="btn btn-sm" data-act="del">Delete</button></td>`
      tr.querySelector('[data-act="del"]').addEventListener('click', (e) => {
        if (!confirm(`Delete rule ${r.number} (${r.to} ${r.action} ${r.direction})?`)) return
        const btn = e.currentTarget
        btn.disabled = true
        api.del(`/api/nodes/${uuid}/firewall/rules/${r.number}`)
          .then(load)
          .catch((err) => flash(err.message))
          .finally(() => (btn.disabled = false))
      })
      rows.appendChild(tr)
    }
  }

  const renderStatus = (st) => {
    const bar = el.querySelector('#fw-bar')
    if (st.error) {
      bar.innerHTML = `
        <span class="badge badge-warn">unavailable</span>
        <span class="mono dim">${esc(st.error)}</span>`
      return
    }
    const on = st.enabled
    bar.innerHTML = `
      <span class="badge ${on ? 'badge-ok' : 'badge-bad'}">${on ? 'active' : 'inactive'}</span>
      <span class="mono dim">default in <span class="mono" style="color:var(--ink)">${esc(st.default_in || '—')}</span></span>
      <span class="mono dim">default out <span class="mono" style="color:var(--ink)">${esc(st.default_out || '—')}</span></span>
      <span style="flex:1"></span>
      <button class="btn btn-sm" id="btn-toggle">${on ? 'Disable firewall' : 'Enable firewall'}</button>`
    bar.querySelector('#btn-toggle').addEventListener('click', async (e) => {
      const target = !on
      if (!confirm(`${target ? 'Enable' : 'Disable'} the firewall on this node?`)) return
      const btn = e.currentTarget
      btn.disabled = true
      try {
        await api.post(`/api/nodes/${uuid}/firewall/toggle`, { enabled: target })
        await load()
      } catch (err) {
        if (err.message.includes('toggle disabled')) {
          flashEl.innerHTML = `${esc(err.message)} — enable <code>ufw.allow_toggle</code> in the agent config (<code>/etc/gopitd/gopitd.yaml</code>) →
            <a href="${CONFIG_DOCS_URL}" target="_blank" rel="noopener">docs</a>`
          flashEl.style.opacity = 1
          setTimeout(() => (flashEl.style.opacity = 0), 6000)
        } else {
          flash(err.message)
        }
      } finally {
        btn.disabled = false
      }
    })
  }

  const form = el.querySelector('#rule-form')
  const errEl = el.querySelector('#rule-error')
  const previewWrap = el.querySelector('#preview-wrap')
  const formData = () => {
    const f = new FormData(form)
    const port = Number(f.get('port'))
    if (!port || port < 1 || port > 65535) {
      errEl.textContent = 'Port must be a number between 1 and 65535.'
      return null
    }
    return {
      protocol: f.get('protocol'),
      port,
      action: f.get('action'),
      from: f.get('from').trim() || 'any',
      interface: f.get('interface').trim(),
    }
  }
  const renderPreview = (st) => {
    const rows = el.querySelector('#rows-preview')
    rows.innerHTML = ''
    if (!st.rules || st.rules.length === 0) {
      rows.innerHTML = `<tr><td colspan="6" class="empty">No rules.</td></tr>`
      return
    }
    for (const [i, r] of st.rules.entries()) {
      const tr = document.createElement('tr')
      const isNew = i === st.rules.length - 1
      tr.innerHTML = `
        <td class="mono dim">${esc(r.number)}</td>
        <td class="mono">${esc(r.to)}</td>
        <td><span class="badge badge-${actionColor[r.action] || 'dim'}">${esc(r.action)}</span></td>
        <td class="mono dim">${esc(r.from)}</td>
        <td class="mono dim">${esc(r.direction)}</td>
        <td class="mono dim">${esc(r.interface || '—')}${isNew ? ' <span class="badge badge-ok">new</span>' : ''}</td>`
      rows.appendChild(tr)
    }
    previewWrap.classList.remove('hidden')
  }
  el.querySelector('#rule-preview').addEventListener('click', async () => {
    const body = formData()
    if (!body) return
    errEl.textContent = ''
    const btn = el.querySelector('#rule-preview')
    btn.disabled = true
    try {
      renderPreview(await api.post(`/api/nodes/${uuid}/firewall/preview`, body))
    } catch (err) {
      errEl.textContent = err.message
    } finally {
      btn.disabled = false
    }
  })
  form.addEventListener('submit', async (e) => {
    e.preventDefault()
    errEl.textContent = ''
    const body = formData()
    if (!body) return
    const addBtn = el.querySelector('#rule-add')
    addBtn.disabled = true
    try {
      await api.post(`/api/nodes/${uuid}/firewall/rules`, body)
      form.reset()
      form.elements.from.value = 'any'
      previewWrap.classList.add('hidden')
      await load()
    } catch (err) {
      errEl.textContent = err.message
    } finally {
      addBtn.disabled = false
    }
  })

  load()

  return { el }
}