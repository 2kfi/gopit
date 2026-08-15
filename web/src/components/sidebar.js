import { api } from '../api.js'
import { navigate, state } from '../main.js'
import { statusDot } from '../views/nodes.js'
import { esc } from '../util.js'

export function sidebar(user, nodes) {
  const el = document.createElement('aside')
  el.className = 'sidebar'
  el.innerHTML = `
    <a class="brand" href="#/nodes">gopit<span class="caret">▍</span></a>
    <div class="side-label">Nodes <span class="count" id="side-count"></span></div>
    <ul class="node-list" id="side-list"></ul>
    <div class="side-foot">
      <span class="mono dim" id="side-user"></span>
      <button class="btn btn-ghost btn-sm" id="logout">Sign out</button>
    </div>
  `

  const list = el.querySelector('#side-list')
  const count = el.querySelector('#side-count')
  el.querySelector('#side-user').textContent = user.username

  const render = () => {
    const live = state.nodes
    count.textContent = live.length
    list.innerHTML = ''
    if (live.length === 0) {
      list.innerHTML = `<li class="dim">No nodes discovered yet.</li>`
      return
    }
    for (const n of live) {
      const li = document.createElement('li')
      const active = location.hash.startsWith(`#/node/${n.id}/`)
      li.className = active ? 'active' : ''
      li.innerHTML = `${statusDot(n.status).outerHTML}<a href="#/node/${esc(n.id)}/dashboard" data-uuid="${esc(n.id)}">${esc(n.hostname || n.ip)}</a>`
      li.querySelector('a').addEventListener('click', () => navigate(li.querySelector('a').hash))
      list.appendChild(li)
    }
  }
  render()

  el.querySelector('#logout').addEventListener('click', async () => {
    try {
      await api.post('/api/logout')
    } catch {}
    state.setUser(null)
    navigate('#/login')
  })

  const poll = setInterval(async () => {
    try {
      state.setNodes(await api.get('/api/nodes'))
      render()
    } catch {}
  }, 5000)

  window.addEventListener('hashchange', render)

  return {
    el,
    unmount() {
      clearInterval(poll)
      window.removeEventListener('hashchange', render)
    },
  }
}
