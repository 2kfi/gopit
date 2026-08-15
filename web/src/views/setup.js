import { api } from '../api.js'
import { navigate, state } from '../main.js'
import { pwScore, PASSWORD_METER } from '../util.js'

// First-run admin setup: shown instead of login while the server has no
// users. Creating this account makes it the admin (user id 1).
export function setupView() {
  const el = document.createElement('div')
  el.className = 'login-wrap'
  el.innerHTML = `
    <div class="login-card panel">
      <div class="brand">gopit<span class="caret">▍</span></div>
      <p class="sub">Welcome — create your admin account</p>
      <form id="setup-form" autocomplete="off">
        <label>Username <input name="username" required autofocus></label>
        <label>Password <input name="password" type="password" required></label>
        <div class="pw-meter hidden" id="pw-meter">
          <div class="pw-meter-fill" id="pw-meter-fill"></div>
        </div>
        <label>Confirm <input name="confirm" type="password" required></label>
        <p class="form-error" id="setup-error"></p>
        <button class="btn btn-primary btn-block" type="submit">Create account</button>
      </form>
    </div>
  `
  const errEl = el.querySelector('#setup-error')
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
    fill.style.background = `var(--${PASSWORD_METER[score]})`
  })
  el.querySelector('form').addEventListener('submit', async (e) => {
    e.preventDefault()
    const f = new FormData(e.currentTarget)
    errEl.textContent = ''
    const username = f.get('username').trim()
    const password = f.get('password')
    if (password !== f.get('confirm')) {
      errEl.textContent = 'Passwords do not match'
      return
    }
    try {
      await api.post('/api/setup', { username, password })
      const data = await api.post('/api/login', { username, password })
      api.setCSRF(data.csrf_token)
      state.setupNeeded = false
      state.setUser({ username })
      navigate('#/wizard')
    } catch (err) {
      errEl.textContent = err.message
    }
  })
  return { el }
}
