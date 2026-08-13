// Node sub-view tabs: Dashboard | Docker | Terminal | Firewall.
import { esc } from '../util.js'

export function nodeTabs(uuid, active) {
  const tabs = [
    { id: 'dashboard', label: 'Dashboard', href: `#/node/${esc(uuid)}/dashboard` },
    { id: 'docker', label: 'Docker', href: `#/node/${esc(uuid)}/docker` },
    { id: 'terminal', label: 'Terminal', href: `#/node/${esc(uuid)}/terminal` },
    { id: 'firewall', label: 'Firewall', href: `#/node/${esc(uuid)}/firewall` },
  ]
  const nav = document.createElement('nav')
  nav.className = 'tabs'
  nav.innerHTML = tabs
    .map((t) => `<a class="tab ${t.id === active ? 'active' : ''}" href="${t.href}">${t.label}</a>`)
    .join('')
  return nav
}
