import { api } from './api.js'
import './styles.css'

import { loginView } from './views/login.js'
import { nodesView } from './views/nodes.js'
import { dashboardView } from './views/dashboard.js'
import { dockerView } from './views/docker.js'
import { firewallView } from './views/firewall.js'
import { terminalView } from './views/terminal.js'
import { sidebar } from './components/sidebar.js'

const routes = [
  { re: /^\/login$/, view: loginView },
  { re: /^\/nodes$/, view: nodesView },
  { re: /^\/node\/([^/]+)\/dashboard$/, view: dashboardView, params: ['uuid'] },
  { re: /^\/node\/([^/]+)\/docker$/, view: dockerView, params: ['uuid'] },
  { re: /^\/node\/([^/]+)\/firewall$/, view: firewallView, params: ['uuid'] },
  { re: /^\/node\/([^/]+)\/terminal$/, view: terminalView, params: ['uuid'] },
]

let current = null

export function navigate(hash) {
  location.hash = hash
}

function matchRoute(hash) {
  for (const r of routes) {
    const m = hash.match(r.re)
    if (!m) continue
    const params = {}
    ;(r.params || []).forEach((p, i) => (params[p] = m[i + 1]))
    return { view: r.view, params }
  }
  return null
}

async function render() {
  if (current && current.unmount) current.unmount()
  const app = document.getElementById('app')
  app.innerHTML = ''

  if (!state.user) {
    const login = loginView()
    current = login
    app.appendChild(login.el)
    login.mount && login.mount()
    return
  }

  const route = matchRoute(location.hash)
  if (!route) {
    navigate('#/nodes')
    return
  }
  const sb = sidebar(state.user, state.nodes)
  app.appendChild(sb.el)
  const main = document.createElement('main')
  main.className = 'content'
  app.appendChild(main)
  const view = route.view(route.params || {})
  current = view
  main.appendChild(view.el)
  view.mount && view.mount(main)
}

// Global state: current user + node list (refreshed by sidebar polling).
export const state = {
  user: null,
  nodes: [],
  setUser(u) {
    state.user = u
    render()
  },
  setNodes(n) {
    state.nodes = n
  },
}

async function boot() {
  window.addEventListener('hashchange', render)
  try {
    const nodes = await api.get('/nodes')
    state.setUser({ username: 'admin' })
    state.setNodes(nodes)
  } catch (err) {
    state.setUser(null)
    if (location.hash !== '#/login') navigate('#/login')
  }
  render()
}

boot()
