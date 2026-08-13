import { api } from '../api.js'
import { navigate, state } from '../main.js'

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
        <p class="form-error" id="login-error"></p>
        <button class="btn btn-primary btn-block" type="submit">Sign in</button>
      </form>
    </div>
  `
  const errEl = el.querySelector('#login-error')
  el.querySelector('form').addEventListener('submit', async (e) => {
    e.preventDefault()
    const f = new FormData(e.currentTarget)
    errEl.textContent = ''
    try {
      await api.post('/login', { username: f.get('username'), password: f.get('password') })
      state.setUser({ username: f.get('username') })
      navigate('#/nodes')
    } catch (err) {
      errEl.textContent = err.message
    }
  })
  return { el }
}
