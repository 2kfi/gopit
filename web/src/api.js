async function request(method, path, body) {
  const res = await fetch(path, {
    method,
    credentials: 'same-origin',
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  })
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    const err = new Error(data.error || res.statusText || `HTTP ${res.status}`)
    err.status = res.status
    if (res.status === 401 && location.hash !== '#/login') {
      location.hash = '#/login'
      location.reload() // re-boot: boot() re-probes /nodes and lands on login
    }
    throw err
  }
  // a 200 that isn't JSON means we hit the SPA fallback, not the API
  if (Object.keys(data).length === 0 && (res.headers.get('content-type') || '').includes('text/html')) {
    throw new Error(`API not found: ${path}`)
  }
  return data
}

export const api = {
  get: (path) => request('GET', path),
  post: (path, body) => request('POST', path, body),
  put: (path, body) => request('PUT', path, body),
  del: (path) => request('DELETE', path),
}

// isUnauthorized marks 401s for the router to bounce to login.
export function isUnauthorized(err) {
  return err && err.status === 401
}
