// esc escapes a string for safe interpolation into innerHTML templates.
// All agent-supplied values (hostnames, names, statuses, ufw/docker output)
// MUST go through this — a hostile agent equals a hostile browser otherwise.
export function esc(s) {
  if (s == null) return ''
  return String(s).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[c])
}

// maskToken shows a token's head/tail with the middle hidden.
export function maskToken(t) {
  if (!t) return ''
  if (t.length <= 8) return '•'.repeat(t.length)
  return `${t.slice(0, 4)}${'•'.repeat(12)}${t.slice(-4)}`
}

// pwScore is a lightweight client-side strength estimate (0-4) mirroring the
// server's zxcvbn threshold so the meter guides before the API rejects.
export function pwScore(pw) {
  if (!pw) return 0
  let s = 0
  if (pw.length >= 10) s++
  if (pw.length >= 14) s++
  if (/[a-z]/.test(pw) && /[A-Z]/.test(pw) && /\d/.test(pw)) s++
  if (/[^a-zA-Z0-9]/.test(pw)) s++
  return Math.min(4, s)
}

export const PASSWORD_METER = ['bad', 'bad', 'warn', 'ok', 'ok']

// copyText copies with a clipboard fallback for non-secure (http) contexts.
export async function copyText(text) {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {}
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.select()
    const ok = document.execCommand('copy')
    ta.remove()
    return ok
  } catch {
    return false
  }
}

// copyButton returns a "Copy" button wired to copy `text` with feedback.
export function copyButton(text) {
  const b = document.createElement('button')
  b.className = 'btn btn-sm'
  b.textContent = 'Copy'
  b.addEventListener('click', async () => {
    b.textContent = (await copyText(text)) ? 'Copied' : 'Copy failed'
    setTimeout(() => (b.textContent = 'Copy'), 1500)
  })
  return b
}