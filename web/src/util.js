// esc escapes a string for safe interpolation into innerHTML templates.
// All agent-supplied values (hostnames, names, statuses, ufw/docker output)
// MUST go through this — a hostile agent equals a hostile browser otherwise.
export function esc(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  })[c])
}