import { on, connect, disconnect } from '../ws.js'
import { gauge, sparkline, fmtBytes, fmtRate } from '../components/charts.js'
import { nodeTabs } from '../components/node-tabs.js'
import { state } from '../main.js'
import { esc } from '../util.js'

export function dashboardView({ uuid }) {
  const node = state.nodes.find((n) => n.id === uuid) || {}
  const el = document.createElement('div')
  el.innerHTML = `
    <header class="page-head">
      <div>
        <h1 class="mono">${esc(node.hostname || uuid.slice(0, 8))}</h1>
        <p class="sub mono">${uuid} <span class="dim">·</span> <span id="node-state" class="dim">waiting for stream…</span></p>
      </div>
    </header>
    ${nodeTabs(uuid, 'dashboard').outerHTML}
    <section class="grid-gauges">
      <div class="panel gauge-panel" id="cpu-panel">
        <div class="gauge-host"><div id="cpu"></div></div>
        <h3 class="gauge-title">CPU</h3>
        <p class="sub gauge-sub mono" id="cpu-sub">— cores</p>
      </div>
      <div class="panel gauge-panel" id="mem-panel">
        <div class="gauge-host"><div id="mem"></div></div>
        <h3 class="gauge-title">Memory</h3>
        <p class="sub gauge-sub mono" id="mem-sub">—</p>
      </div>
    </section>
    <section class="panel">
      <h3>Disk</h3>
      <div id="disks" class="disk-list"></div>
    </section>
    <section class="grid-net">
      <div class="panel net-panel">
        <h3>Network in</h3>
        <div id="rx"></div>
        <p class="sub mono" id="rx-rate">—</p>
      </div>
      <div class="panel net-panel">
        <h3>Network out</h3>
        <div id="tx"></div>
        <p class="sub mono" id="tx-rate">—</p>
      </div>
    </section>
  `

  const cpuGauge = gauge(el.querySelector('#cpu'), { label: '%', warnAt: 75, badAt: 90 })
  const memGauge = gauge(el.querySelector('#mem'), { label: '%', warnAt: 80, badAt: 92 })
  cpuGauge.set(0)
  memGauge.set(0)
  const cpuSub = el.querySelector('#cpu-sub')
  const memSub = el.querySelector('#mem-sub')
  const disksEl = el.querySelector('#disks')
  const rxSpark = sparkline(el.querySelector('#rx'), { color: 'var(--accent)' })
  const txSpark = sparkline(el.querySelector('#tx'), { color: 'var(--warn)' })
  const rxRate = el.querySelector('#rx-rate')
  const txRate = el.querySelector('#tx-rate')
  const stateEl = el.querySelector('#node-state')

  let lastSeen = Date.now()
  const staleTimer = setInterval(() => {
    if (Date.now() - lastSeen > 3500) stateEl.textContent = 'offline — no stream'
  }, 1000)

  const off = on('system.stats', (s) => {
    lastSeen = Date.now()
    stateEl.textContent = 'live'
    cpuGauge.set(s.cpu.percent)
    cpuSub.textContent = `${Math.round(s.cpu.percent)}% · ${s.cpu.cores} cores`
    memGauge.set(s.mem.percent)
    memSub.textContent = `${fmtBytes(s.mem.used)} / ${fmtBytes(s.mem.total)}`
    disksEl.innerHTML = ''
    for (const d of s.disk) {
      const bar = document.createElement('div')
      bar.className = 'disk-bar'
      bar.innerHTML = `
        <div class="disk-meta"><span class="mono">${esc(d.mount)}</span>
          <span class="mono dim">${fmtBytes(d.used)} / ${fmtBytes(d.total)}</span></div>
        <div class="bar"><div class="fill" style="width:${Math.min(100, Math.max(0, Number(d.percent) || 0))}%"></div></div>`
      disksEl.appendChild(bar)
    }
    rxSpark.push(s.net.rx_per_sec)
    txSpark.push(s.net.tx_per_sec)
    rxRate.textContent = fmtRate(s.net.rx_per_sec)
    txRate.textContent = fmtRate(s.net.tx_per_sec)
  })

  connect(uuid)

  return {
    el,
    unmount() {
      off()
      clearInterval(staleTimer)
      disconnect()
    },
  }
}
