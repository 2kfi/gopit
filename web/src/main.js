import { api } from './api.js'
import './styles.css'

import { loginView } from './views/login.js'
import { setupView } from './views/setup.js'
import { nodesView } from './views/nodes.js'
import { wizardView } from './views/wizard.js'
import { dashboardView } from './views/dashboard.js'
import { dockerView } from './views/docker.js'
import { firewallView } from './views/firewall.js'
import { terminalView } from './views/terminal.js'
import { sidebar } from './components/sidebar.js'

const routes = [
  { re: /^\/login$/, view: loginView },
  { re: /^\/wizard$/, view: wizardView },
  { re: /^\/nodes$/, view: nodesView },
  { re: /^\/node\/([^/]+)\/dashboard$/, view: dashboardView, params: ['uuid'] },
  { re: /^\/node\/([^/]+)\/docker$/, view: dockerView, params: ['uuid'] },
  { re: /^\/node\/([^/]+)\/firewall$/, view: firewallView, params: ['uuid'] },
  { re: /^\/node\/([^/]+)\/terminal$/, view: terminalView, params: ['uuid'] },
]

let current = null
let currentSidebar = null

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
  if (currentSidebar) {
    currentSidebar.unmount()
    currentSidebar = null
  }
  const app = document.getElementById('app')
  app.innerHTML = ''

  if (!state.user) {
    const view = state.setupNeeded ? setupView() : loginView()
    current = view
    app.appendChild(view.el)
    view.mount && view.mount()
    return
  }

  const route = matchRoute(location.hash)
  if (!route) {
    navigate('#/nodes')
    return
  }
  // First-run wizard: no sidebar, full-screen onboarding.
  const isWizard = location.hash === '#/wizard'
  if (state.wizardDone && isWizard) {
    navigate('#/nodes')
    return
  }
  if (!state.wizardDone && !isWizard && location.hash !== '#/login') {
    navigate('#/wizard')
    return
  }
  if (!isWizard) {
    const sb = sidebar(state.user, state.nodes)
    app.appendChild(sb.el)
    currentSidebar = sb
  }
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
  wizardDone: false,
  setupNeeded: false,
  setUser(u) {
    state.user = u
    render()
  },
  setNodes(n) {
    state.nodes = n
  },
  setWizardDone(d) {
    state.wizardDone = d
    render()
  },
}

async function boot() {
  window.addEventListener('hashchange', render)
  try {
    const me = await api.get('/api/me')
    api.setCSRF(me.csrf_token)
    state.setUser(me)
    try {
      state.setNodes(await api.get('/api/nodes'))
    } catch {
      // transient node-list failure — sidebar polls refresh it; not a logout
    }
    try {
      state.wizardDone = !!(await api.get('/api/wizard')).done
    } catch {
      // wizard status can't be read; the wizard view polls and self-heals
    }
  } catch (err) {
    // Not authenticated: on a fresh server (no users) show first-run setup
    // instead of login, so the admin account can be created.
    try {
      const s = await api.get('/api/setup/status')
      state.setupNeeded = !s.configured
    } catch {
      state.setupNeeded = false
    }
    state.setUser(null)
    if (location.hash !== '#/login') navigate('#/login')
  }
  render()
}

boot()
