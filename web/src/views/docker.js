import { api } from '../api.js'
import { state } from '../main.js'
import { fmtBytes } from '../components/charts.js'
import { nodeTabs } from '../components/node-tabs.js'
import { esc } from '../util.js'

const badge = (s) => {
  const color = /^(running|up|running\()/i.test(s) ? 'ok' : /^(exited|dead|stopped)/i.test(s) ? 'bad' : 'warn'
  return `<span class="badge badge-${color}">${esc(s)}</span>`
}

const fmtPorts = (ports) =>
  (ports || [])
    .filter((p) => p.public_port)
    .map((p) => `${p.ip || ''}${p.ip ? ':' : ''}${p.public_port}->${p.private_port}/${p.type}`)
    .join(', ') || '—'

const fmtCreated = (c) => (c ? new Date(typeof c === 'number' ? c * 1000 : c).toLocaleString() : '—')

export function dockerView({ uuid }) {
  const node = state.nodes.find((n) => n.id === uuid) || {}
  const el = document.createElement('div')
  el.innerHTML = `
    <header class="page-head">
      <div>
        <h1 class="mono">${esc(node.hostname || uuid.slice(0, 8))}</h1>
        <p class="sub mono dim">docker / container management</p>
      </div>
    </header>
    ${nodeTabs(uuid, 'docker').outerHTML}
    <div class="flash" id="flash"></div>

    <div class="tabs">
      <button class="tab active" data-tab="containers">Containers</button>
      <button class="tab" data-tab="images">Images</button>
      <button class="tab" data-tab="volumes">Volumes</button>
      <button class="tab" data-tab="stacks">Stacks</button>
    </div>

    <section id="pane-containers">
      <div class="panel terminal">
        <div class="term-bar"><span></span><span></span><span></span>containers</div>
        <table class="table">
          <thead><tr><th>Name</th><th>Image</th><th>Status</th><th>Ports</th><th>Created</th><th></th></tr></thead>
          <tbody id="rows-containers"></tbody>
        </table>
      </div>
    </section>

    <section id="pane-images" class="hidden">
      <div class="panel terminal">
        <div class="term-bar"><span></span><span></span><span></span>images</div>
        <table class="table">
          <thead><tr><th>Repository / tag</th><th>ID</th><th>Size</th><th>Created</th><th>In use</th><th></th></tr></thead>
          <tbody id="rows-images"></tbody>
        </table>
      </div>
    </section>

    <section id="pane-volumes" class="hidden">
      <div class="panel terminal">
        <div class="term-bar"><span></span><span></span><span></span>volumes</div>
        <table class="table">
          <thead><tr><th>Name</th><th>Driver</th><th>Mountpoint</th><th>Created</th><th></th></tr></thead>
          <tbody id="rows-volumes"></tbody>
        </table>
      </div>
    </section>

    <section id="pane-stacks" class="hidden">
      <div class="panel terminal">
        <div class="term-bar"><span></span><span></span><span></span>stacks <span style="margin-left:auto"><button class="btn btn-primary btn-sm" id="btn-deploy">Deploy stack</button></span></div>
        <table class="table">
          <thead><tr><th>Name</th><th>Status</th><th>Config files</th><th></th></tr></thead>
          <tbody id="rows-stacks"></tbody>
        </table>
      </div>
    </section>

    <dialog id="log-dialog" class="dialog dialog-lg">
      <div class="dialog-body log-dialog-body">
        <h2 class="mono">logs <span class="dim" id="log-title"></span></h2>
        <pre class="log-out" id="log-out"></pre>
        <div class="dialog-actions">
          <span class="dim mono" id="log-state"></span>
          <button class="btn" id="log-clear">Clear</button>
          <button class="btn btn-primary" id="log-close">Close</button>
        </div>
      </div>
    </dialog>

    <dialog id="deploy-dialog" class="dialog dialog-lg">
      <form class="dialog-body">
        <h2>Deploy stack</h2>
        <label>Name <input name="name" required placeholder="my-stack" pattern="[a-z0-9][a-z0-9_-]*" title="lowercase letters, digits, - and _"></label>
        <label>Compose YAML <textarea name="yaml" required rows="10" spellcheck="false" placeholder="services:&#10;  web:&#10;    image: nginx:alpine"></textarea></label>
        <p class="form-error" id="deploy-error"></p>
        <pre class="log-out log-out-sm hidden" id="deploy-out"></pre>
        <div class="dialog-actions">
          <button class="btn" type="button" id="deploy-cancel">Cancel</button>
          <button class="btn" type="button" id="deploy-validate">Validate YAML</button>
          <button class="btn btn-primary" id="deploy-go">Deploy</button>
        </div>
      </form>
    </dialog>
  `

  const flashEl = el.querySelector('#flash')
  const flash = (msg) => {
    flashEl.textContent = msg
    flashEl.style.opacity = 1
    setTimeout(() => (flashEl.style.opacity = 0), 3500)
  }

  const tbody = (id) => el.querySelector(id)
  const action = async (fn, btn, refresh) => {
    btn.disabled = true
    try {
      await fn()
      await refresh()
    } catch (err) {
      flash(err.message)
    } finally {
      btn.disabled = false
    }
  }

  const loadContainers = async () => {
    const list = await api.get(`/api/nodes/${uuid}/containers`)
    const rows = tbody('#rows-containers')
    rows.innerHTML = ''
    if (list.length === 0) {
      rows.innerHTML = `<tr><td colspan="6" class="empty">No containers on this node.</td></tr>`
      return
    }
    for (const c of list) {
      const tr = document.createElement('tr')
      const running = c.state === 'running'
      tr.innerHTML = `
        <td><span class="node-link mono">${esc(c.name)}</span></td>
        <td class="mono dim">${esc(c.image)}</td>
        <td>${badge(c.status)}</td>
        <td class="mono dim">${esc(fmtPorts(c.ports))}</td>
        <td class="mono dim">${esc(fmtCreated(c.created))}</td>
        <td class="actions">
          <button class="btn btn-sm" data-act="start" ${running ? 'disabled' : ''}>Start</button>
          <button class="btn btn-sm" data-act="stop" ${running ? '' : 'disabled'}>Stop</button>
          <button class="btn btn-sm" data-act="logs">Logs</button>
          <button class="btn btn-sm" data-act="remove">Remove</button>
        </td>`
      tr.querySelector('[data-act="start"]').addEventListener('click', (e) =>
        action(() => api.post(`/api/nodes/${uuid}/containers/${c.id}/start`), e.currentTarget, loadContainers))
      tr.querySelector('[data-act="stop"]').addEventListener('click', (e) =>
        action(() => api.post(`/api/nodes/${uuid}/containers/${c.id}/stop`, { timeout: 10 }), e.currentTarget, loadContainers))
      tr.querySelector('[data-act="remove"]').addEventListener('click', (e) => {
        if (!confirm(`Remove container ${c.name}?`)) return
        action(() => api.del(`/api/nodes/${uuid}/containers/${c.id}`), e.currentTarget, loadContainers)
      })
      tr.querySelector('[data-act="logs"]').addEventListener('click', () => openLogs(c))
      rows.appendChild(tr)
    }
  }

  const loadImages = async () => {
    const list = await api.get(`/api/nodes/${uuid}/images`)
    const rows = tbody('#rows-images')
    rows.innerHTML = ''
    if (list.length === 0) {
      rows.innerHTML = `<tr><td colspan="6" class="empty">No images on this node.</td></tr>`
      return
    }
    for (const im of list) {
      const tr = document.createElement('tr')
      tr.innerHTML = `
        <td class="mono">${(im.repo_tags && im.repo_tags.length) ? esc(im.repo_tags.join(', ')) : '<span class="dim">&lt;none&gt;</span>'}</td>
        <td class="mono dim">${esc((im.id || '').slice(7, 19))}</td>
        <td class="mono dim">${esc(fmtBytes(im.size))}</td>
        <td class="mono dim">${esc(fmtCreated(im.created))}</td>
        <td class="mono dim">${esc(im.containers || 0)}</td>
        <td><button class="btn btn-sm" data-act="remove">Remove</button></td>`
      tr.querySelector('[data-act="remove"]').addEventListener('click', (e) => {
        if (!confirm(`Remove image ${(im.repo_tags && im.repo_tags[0]) || im.id}?`)) return
        action(() => api.del(`/api/nodes/${uuid}/images/${im.id}`), e.currentTarget, loadImages)
      })
      rows.appendChild(tr)
    }
  }

  const loadVolumes = async () => {
    const list = await api.get(`/api/nodes/${uuid}/volumes`)
    const rows = tbody('#rows-volumes')
    rows.innerHTML = ''
    if (list.length === 0) {
      rows.innerHTML = `<tr><td colspan="5" class="empty">No volumes on this node.</td></tr>`
      return
    }
    for (const v of list) {
      const tr = document.createElement('tr')
      tr.innerHTML = `
        <td class="mono">${esc(v.name)}</td>
        <td class="mono dim">${esc(v.driver)}</td>
        <td class="mono dim">${esc(v.mountpoint)}</td>
        <td class="mono dim">${esc(v.created_at || '—')}</td>
        <td><button class="btn btn-sm" data-act="remove">Remove</button></td>`
      tr.querySelector('[data-act="remove"]').addEventListener('click', (e) => {
        if (!confirm(`Remove volume ${v.name}?`)) return
        action(() => api.del(`/api/nodes/${uuid}/volumes/${v.name}`), e.currentTarget, loadVolumes)
      })
      rows.appendChild(tr)
    }
  }

  const loadStacks = async () => {
    const list = await api.get(`/api/nodes/${uuid}/compose`)
    const rows = tbody('#rows-stacks')
    rows.innerHTML = ''
    if (list.length === 0) {
      rows.innerHTML = `<tr><td colspan="4" class="empty">No compose stacks running. Deploy one to get started.</td></tr>`
      return
    }
    for (const st of list) {
      const tr = document.createElement('tr')
      tr.innerHTML = `
        <td class="mono">${esc(st.name)}</td>
        <td>${badge(st.status)}</td>
        <td class="mono dim">${esc(st.config_files)}</td>
        <td class="actions">
          <button class="btn btn-sm" data-act="ps">Services</button>
          <button class="btn btn-sm" data-act="down">Down</button>
        </td>`
      tr.querySelector('[data-act="ps"]').addEventListener('click', async (e) => {
        const btn = e.currentTarget
        btn.disabled = true
        try {
          const svcs = await api.get(`/api/nodes/${uuid}/compose/${st.name}/ps`)
          const detail = tr.nextElementSibling
          if (detail && detail.dataset.detail) detail.remove()
          if (!btn.dataset.open) {
            const d = document.createElement('tr')
            d.dataset.detail = '1'
            d.innerHTML = `<td colspan="4"><table class="table table-nested"><tbody>${svcs
              .map((s) => `<tr><td class="mono">${esc(s.name)}</td><td>${badge(s.state)}</td><td class="mono dim">${esc(s.image)}</td><td class="mono dim">${esc(s.ports || '—')}</td></tr>`)
              .join('')}</tbody></table></td>`
            tr.after(d)
            btn.dataset.open = '1'
            btn.textContent = 'Hide'
          } else {
            delete btn.dataset.open
            btn.textContent = 'Services'
          }
        } catch (err) {
          flash(err.message)
        } finally {
          btn.disabled = false
        }
      })
      tr.querySelector('[data-act="down"]').addEventListener('click', (e) => {
        if (!confirm(`Bring stack ${st.name} down?`)) return
        action(async () => {
          const res = await api.post(`/api/nodes/${uuid}/compose/${st.name}/down`)
          if (res.output) flash(res.output.trim().split('\n').pop())
        }, e.currentTarget, loadStacks)
      })
      rows.appendChild(tr)
    }
  }

  // ---- log streaming ----
  let logWS = null
  const logDialog = el.querySelector('#log-dialog')
  const logOut = el.querySelector('#log-out')
  const logState = el.querySelector('#log-state')

  const closeLogs = () => {
    if (logWS) {
      logWS.onclose = null
      logWS.close()
      logWS = null
    }
    logState.textContent = ''
    if (logDialog.open) logDialog.close()
  }

  const openLogs = (c) => {
    if (logWS) {
      logWS.onclose = null
      logWS.close()
      logWS = null
    }
    logOut.textContent = ''
    el.querySelector('#log-title').textContent = c.name
    logState.textContent = 'streaming…'
    logDialog.showModal()
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(`${proto}://${location.host}/api/nodes/${uuid}/containers/${c.id}/logs`)
    logWS = ws
    let ended = false
    ws.onmessage = (ev) => {
      let m
      try {
        m = JSON.parse(ev.data)
      } catch {
        return
      }
      if (m.type === 'response' && m.error) {
        logOut.textContent += `✖ ${m.error}\n`
        logState.textContent = 'error'
        return
      }
      if (m.method === 'docker.container.log') {
        logOut.textContent += m.payload.line + '\n'
        logOut.scrollTop = logOut.scrollHeight
      } else if (m.method === 'docker.container.logs.end') {
        ended = true
        if (m.payload.error) logOut.textContent += `✖ stream ended: ${m.payload.error}\n`
        logState.textContent = 'stream ended'
      }
    }
    ws.onerror = () => (logState.textContent = 'connection failed')
    ws.onclose = () => {
      // normal end arrives as logs.end before the server closes the socket
      if (!ended) logState.textContent = 'connection failed'
    }
  }
  logDialog.addEventListener('close', closeLogs)
  el.querySelector('#log-close').addEventListener('click', closeLogs)
  el.querySelector('#log-clear').addEventListener('click', () => (logOut.textContent = ''))

  // ---- deploy stack ----
  const deployDialog = el.querySelector('#deploy-dialog')
  const deployOut = el.querySelector('#deploy-out')
  const deployError = el.querySelector('#deploy-error')
  const deployForm = deployDialog.querySelector('form')
  deployDialog.querySelector('#deploy-cancel').addEventListener('click', () => deployDialog.close())
  el.querySelector('#btn-deploy').addEventListener('click', () => {
    deployOut.classList.add('hidden')
    deployOut.textContent = ''
    deployError.textContent = ''
    deployDialog.showModal()
  })
  const deployInputs = () => {
    const f = new FormData(deployForm)
    const name = f.get('name').trim()
    const yaml = f.get('yaml')
    if (!name || !yaml) {
      deployError.textContent = 'Name and YAML are required'
      return null
    }
    return { name, yaml }
  }
  deployDialog.querySelector('#deploy-validate').addEventListener('click', async (e) => {
    const input = deployInputs()
    if (!input) return
    const btn = e.currentTarget
    btn.disabled = true
    try {
      const res = await api.post(`/api/nodes/${uuid}/compose/validate`, input)
      const svcs = res.services || []
      const nets = res.networks || []
      const vols = res.volumes || []
      deployOut.classList.remove('hidden')
      deployOut.textContent = `✔ valid — ${svcs.length} service(s), ${nets.length} network(s), ${vols.length} volume(s)` + svcs
        .map((s) => `\n  ${s.name}${s.image ? ' (' + s.image + ')' : ''}${s.ports && s.ports.length ? ' — ' + s.ports.join(', ') : ''}`)
        .join('')
      if (nets.length) deployOut.textContent += `\nnetworks: ${nets.join(', ')}`
      if (vols.length) deployOut.textContent += `\nvolumes: ${vols.join(', ')}`
      deployError.textContent = ''
    } catch (err) {
      deployError.textContent = err.message
    } finally {
      btn.disabled = false
    }
  })
  deployForm.addEventListener('submit', async (e) => {
    e.preventDefault()
    const input = deployInputs()
    if (!input) return
    const go = el.querySelector('#deploy-go')
    go.disabled = true
    try {
      const res = await api.post(`/api/nodes/${uuid}/compose/deploy`, input)
      deployOut.classList.remove('hidden')
      deployOut.textContent = res.output || 'deployed'
      deployOut.scrollTop = deployOut.scrollHeight
      await loadStacks()
    } catch (err) {
      deployError.textContent = err.message
    } finally {
      go.disabled = false
    }
  })

  // ---- tabs ----
  const tabBtn = (id) => el.querySelector(`[data-tab="${id}"]`)
  const showTab = (id) => {
    for (const t of ['containers', 'images', 'volumes', 'stacks']) {
      el.querySelector(`#pane-${t}`).classList.toggle('hidden', t !== id)
      tabBtn(t).classList.toggle('active', t === id)
    }
  }
  for (const t of ['containers', 'images', 'volumes', 'stacks']) {
    tabBtn(t).addEventListener('click', () => showTab(t))
  }

  const loadAll = () => Promise.all([
    loadContainers().catch((e) => flash(e.message)),
    loadImages().catch((e) => flash(e.message)),
    loadVolumes().catch((e) => flash(e.message)),
    loadStacks().catch((e) => flash(e.message)),
  ])
  const timer = setInterval(() => {
    if (document.hidden) return // skip polling in background tabs
    loadContainers().catch(() => {})
  }, 8000)
  loadAll()

  return {
    el,
    unmount() {
      clearInterval(timer)
      closeLogs()
    },
  }
}
