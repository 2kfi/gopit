import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { state } from '../main.js'
import { nodeTabs } from '../components/node-tabs.js'
import { esc } from '../util.js'

// Terminal view: one browser WS per session (each dials its own agent
// connection), raw binary frames for pty data, JSON text frames for control.
export function terminalView({ uuid }) {
  const node = state.nodes.find((n) => n.id === uuid) || {}
  const el = document.createElement('div')
  el.innerHTML = `
    <header class="page-head">
      <div>
        <h1 class="mono">${esc(node.hostname || uuid.slice(0, 8))}</h1>
        <p class="sub mono dim">terminal / shell access</p>
      </div>
    </header>
    ${nodeTabs(uuid, 'terminal').outerHTML}

    <div class="panel terminal term-panel">
      <div class="term-tabs" id="term-tabs">
        <button class="btn btn-sm btn-ghost" id="btn-new" title="New session">+ new session</button>
      </div>
      <div class="term-stage" id="term-stage">
        <p class="term-empty" id="term-empty">No sessions. Start one to open a shell on this node.</p>
      </div>
      <div class="term-status mono" id="term-status">idle</div>
    </div>

    <dialog id="login-dialog" class="dialog">
      <form class="dialog-body" id="login-form" autocomplete="off">
        <h2>Connect to shell</h2>
        <p class="sub dim" style="margin:0">Credentials are sent to the node agent once; never stored.</p>
        <label>Username
          <input name="user" required autocomplete="off" autocapitalize="off" spellcheck="false"
                 placeholder="enter node username (not root). For root access, use \`sudo\` inside.">
        </label>
        <label>Password
          <input name="pass" type="password" required autocomplete="off">
        </label>
        <p class="form-error" id="login-error"></p>
        <p class="hint" id="login-hint" hidden>The shell runs as your user. Need root? Type <code>su -l</code> and enter the root password, or use <code>sudo &lt;cmd&gt;</code> for single commands.</p>
        <div class="dialog-actions">
          <button class="btn" type="button" id="login-cancel">Cancel</button>
          <button class="btn btn-primary" id="login-go">Connect</button>
        </div>
      </form>
    </dialog>
  `

  const status = el.querySelector('#term-status')
  const stage = el.querySelector('#term-stage')
  const tabs = el.querySelector('#term-tabs')
  const empty = el.querySelector('#term-empty')
  const dialog = el.querySelector('#login-dialog')
  const loginForm = el.querySelector('#login-form')
  const loginError = el.querySelector('#login-error')

  const sessions = new Map() // id -> { title, term, fit, ws, ended }
  let nextId = 1
  let activeId = null
  let connectWS = null // ws of the session being opened (before term exists)
  const enc = new TextEncoder()

  const setStatus = (s) => (status.textContent = s)

  const updateEmpty = () => {
    empty.classList.toggle('hidden', sessions.size > 0)
  }

  const sendResize = (s) => {
    if (!s.ws || s.ws.readyState !== 1) return
    s.ws.send(JSON.stringify({ type: 'resize', cols: s.term.cols, rows: s.term.rows }))
  }

  const fitSession = (s) => {
    try {
      s.fit.fit()
      sendResize(s)
    } catch {
      /* hidden element; fits again on tab switch */
    }
  }

  const activate = (id) => {
    activeId = id
    for (const [sid, s] of sessions) {
      const on = sid === id
      s.holder.classList.toggle('hidden', !on)
      s.tab.classList.toggle('active', on)
      if (on) {
        s.term.focus()
        fitSession(s)
      }
    }
  }

  const closeSession = (id) => {
    const s = sessions.get(id)
    if (!s) return
    sessions.delete(id)
    if (connectWS === s.ws) connectWS = null
    if (s.ws && s.ws.readyState <= 1) {
      s.ws.send(JSON.stringify({ type: 'close' })) // tell the agent to kill the pty
      s.ws.close()
    }
    s.term.dispose()
    s.tab.remove()
    s.holder.remove()
    if (activeId === id) activeId = null
    const rest = [...sessions.keys()]
    if (rest.length > 0) activate(rest[0])
    updateEmpty()
    if (sessions.size === 0) setStatus('idle')
  }

  const buildSession = (id, user, ws) => {
    const term = new Terminal({ cursorBlink: true, fontSize: 13, fontFamily: 'ui-monospace, "SF Mono", Menlo, Consolas, monospace' })
    const fit = new FitAddon()
    term.loadAddon(fit)

    const tab = document.createElement('button')
    tab.className = 'term-tab'
    tab.innerHTML = `<span class="term-tab-title">${esc(user)}@${esc(node.hostname || node.ip || 'node')}</span><span class="term-tab-x" title="Close session">×</span>`
    const holder = document.createElement('div')
    holder.className = 'term-sess hidden'
    stage.appendChild(holder)
    term.open(holder)
    tab.querySelector('.term-tab-x').addEventListener('click', (e) => {
      e.stopPropagation()
      closeSession(id)
    })
    tab.addEventListener('click', () => activate(id))

    const s = { id, title: user, term, fit, ws, holder, tab, ended: false }
    sessions.set(id, s)
    tabs.insertBefore(tab, tabs.querySelector('#btn-new'))
    activate(id)
    setStatus('connected')
    updateEmpty()

    term.onData((d) => {
      if (s.ws && s.ws.readyState === 1) s.ws.send(enc.encode(d)) // binary frame = input
    })
    return s
  }

  const startSession = (user, pass) => {
    const id = nextId++
    const proto = location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(`${proto}://${location.host}/api/nodes/${uuid}/terminal`)
    ws.binaryType = 'arraybuffer'
    connectWS = ws
    setStatus('connecting…')
    ws.onopen = () => ws.send(JSON.stringify({ user, password: pass }))
    ws.onmessage = (ev) => {
      if (typeof ev.data === 'string') {
        let m
        try {
          m = JSON.parse(ev.data)
        } catch {
          return
        }
        if (m.type === 'error') {
          loginError.textContent = m.message || 'connection failed'
          ws.close()
          setStatus('idle')
          return
        }
        if (m.type === 'open') {
          loginError.textContent = ''
          connectWS = null // dialog.close() must not kill the session socket
          dialog.close()
          buildSession(id, user, ws)
          return
        }
        if (m.type === 'exit') {
          const s = sessions.get(id)
          if (s) {
            s.ended = true
            s.term.write('\r\n\x1b[33m[session ended]\x1b[0m\r\n')
            setStatus('session ended')
            s.ws.close()
          }
          return
        }
        return
      }
      const s = sessions.get(id)
      if (s) s.term.write(new Uint8Array(ev.data))
    }
    ws.onclose = () => {
      if (connectWS === ws) connectWS = null
      const s = sessions.get(id)
      if (s && !s.ended) {
        s.term.write('\r\n\x1b[31m[disconnected]\x1b[0m\r\n')
        setStatus('disconnected')
        s.ws = null // no more sends; keep the tab so output is readable
      }
    }
  }

  loginForm.addEventListener('submit', (e) => {
    e.preventDefault()
    const f = new FormData(loginForm)
    const user = f.get('user').trim()
    const pass = f.get('pass')
    if (!user || pass == null) return
    loginError.textContent = ''
    startSession(user, pass)
  })
  el.querySelector('#login-cancel').addEventListener('click', () => dialog.close())
  dialog.addEventListener('close', () => {
    if (connectWS) {
      connectWS.close()
      connectWS = null
      if (sessions.size === 0) setStatus('idle')
    }
  })
  // First dialog open: hint once per browser (su -l / sudo explanation).
  const hintKey = 'gopit_terminal_hint_shown'
  const showHint = () => {
    const hint = el.querySelector('#login-hint')
    let shown = false
    try {
      shown = localStorage.getItem(hintKey) === '1'
    } catch {}
    hint.hidden = shown
    if (!shown) {
      try {
        localStorage.setItem(hintKey, '1')
      } catch {}
    }
  }
  el.querySelector('#btn-new').addEventListener('click', () => {
    loginForm.reset()
    loginError.textContent = ''
    showHint()
    dialog.showModal()
    loginForm.elements.user.focus()
  })

  const onWinResize = () => {
    for (const s of sessions.values()) fitSession(s)
  }
  window.addEventListener('resize', onWinResize)

  return {
    el,
    unmount() {
      window.removeEventListener('resize', onWinResize)
      for (const id of [...sessions.keys()]) closeSession(id)
      if (connectWS) {
        connectWS.close()
        connectWS = null
      }
      dialog.close()
    },
  }
}