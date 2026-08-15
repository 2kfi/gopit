import { api } from '../api.js'
import { navigate, state } from '../main.js'

// pwScore is a lightweight client-side strength estimate (0-4) mirroring the
// server's zxcvbn threshold so the meter guides before the API rejects.
// ponytail: heuristic, not zxcvbn; the server still enforces the real score.
const pwScore = (pw) => {
  if (!pw) return 0
  let s = 0
  if (pw.length >= 10) s++
  if (pw.length >= 14) s++
  if (/[a-z]/.test(pw) && /[A-Z]/.test(pw) && /\d/.test(pw)) s++
  if (/[^a-zA-Z0-9]/.test(pw)) s++
  return Math.min(4, s)
}

const METER = ['bad', 'bad', 'warn', 'ok', 'ok']

export function loginView() {
  const el = document.createElement('div')
  el.className = 'login-wrap'
  el.innerHTML = `
    <div class="login-card panel">
      <div class="brand">gopit<span class="caret">▍</span></div>
      <p class="sub">Multi-node Linux control plane</p>
      <form id="login-form" autocomplete="off">
        <label>Username <input name="username" required autofocus></label>
        <label>Password <input name="password" type="password" required></label>
        <div class="pw-meter hidden" id="pw-meter">
          <div class="pw-meter-fill" id="pw-meter-fill"></div>
        </div>
        <p class="form-error" id="login-error"></p>
        <button class="btn btn-primary btn-block" type="submit">Sign in</button>
      </form>
    </div>
  `
  const errEl = el.querySelector('#login-error')
  const meter = el.querySelector('#pw-meter')
  const fill = el.querySelector('#pw-meter-fill')
  el.querySelector('input[name="password"]').addEventListener('input', (e) => {
    const score = pwScore(e.target.value)
    if (!e.target.value) {
      meter.classList.add('hidden')
      return
    }
    meter.classList.remove('hidden')
    fill.style.width = `${(score / 4) * 100}%`
    fill.style.background = `var(--${METER[score]})`
  })
  el.querySelector('form').addEventListener('submit', async (e) => {
    e.preventDefault()
    const f = new FormData(e.currentTarget)
    errEl.textContent = ''
    try {
      const data = await api.post('/api/login', { username: f.get('username'), password: f.get('password') })
      api.setCSRF(data.csrf_token)
      state.setUser({ username: f.get('username') })
      navigate('#/nodes')
    } catch (err) {
      errEl.textContent = err.message
    }
  })
  return { el }
}