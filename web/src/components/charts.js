// charts.js — SVG ring gauge and canvas sparkline, both zero-dependency.
import { state } from '../main.js'

export function gauge(placeholder, opts = {}) {
  const size = opts.size || 150
  const stroke = opts.stroke || 9
  const radius = (size - stroke) / 2
  const circ = 2 * Math.PI * radius

  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  svg.setAttribute('viewBox', `0 0 ${size} ${size}`)
  svg.setAttribute('role', 'img')
  svg.innerHTML = `
    <circle class="gauge-track" cx="${size / 2}" cy="${size / 2}" r="${radius}" fill="none" stroke-width="${stroke}"></circle>
    <circle class="gauge-arc" cx="${size / 2}" cy="${size / 2}" r="${radius}" fill="none" stroke-width="${stroke}"
      stroke-linecap="round" stroke-dasharray="${circ}" stroke-dashoffset="${circ}"></circle>
    <text class="gauge-value" x="50%" y="50%" text-anchor="middle" dominant-baseline="central"></text>
    <text class="gauge-label" x="50%" y="62%" text-anchor="middle"></text>
  `
  placeholder.innerHTML = ''
  placeholder.appendChild(svg)
  const arc = svg.querySelector('.gauge-arc')
  const value = svg.querySelector('.gauge-value')
  const label = svg.querySelector('.gauge-label')
  if (opts.label) label.textContent = opts.label

  const off = (pct) => circ * (1 - Math.max(0, Math.min(100, pct)) / 100)
  arc.style.strokeDashoffset = off(0)
  if (opts.value !== undefined) arc.style.strokeDashoffset = off(opts.value)

  const colorFor = (pct) => {
    const warn = opts.warnAt ?? 75
    const bad = opts.badAt ?? 90
    return pct >= bad ? 'var(--bad)' : pct >= warn ? 'var(--warn)' : 'var(--accent)'
  }

  return {
    set(pct, fmt) {
      const p = Math.max(0, Math.min(100, pct))
      arc.style.strokeDashoffset = off(p)
      arc.style.stroke = colorFor(p)
      value.textContent = fmt ? fmt(p) : `${Math.round(p)}%`
    },
  }
}

export function sparkline(placeholder, opts = {}) {
  const width = opts.width || 260
  const height = opts.height || 56
  const canvas = document.createElement('canvas')
  canvas.width = width * devicePixelRatio
  canvas.height = height * devicePixelRatio
  canvas.style.width = `${width}px`
  canvas.style.height = `${height}px`
  placeholder.appendChild(canvas)
  const ctx = canvas.getContext('2d')
  const data = new Array(opts.samples || 60).fill(0)

  const draw = () => {
    ctx.setTransform(devicePixelRatio, 0, 0, devicePixelRatio, 0, 0)
    ctx.clearRect(0, 0, width, height)
    const max = Math.max(...data, 1)
    ctx.strokeStyle = opts.color || 'var(--accent)'
    ctx.lineWidth = 1.5
    ctx.beginPath()
    data.forEach((v, i) => {
      const x = (i / (data.length - 1)) * width
      const y = height - (v / max) * (height - 6) - 3
      i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y)
    })
    ctx.stroke()
  }

  return {
    push(v) {
      data.push(v)
      data.shift()
      draw()
    },
    draw,
  }
}

export function fmtBytes(n) {
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = Number(n) || 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(v >= 100 || i === 0 ? 0 : 1)} ${units[i]}`
}

export function fmtRate(n) {
  return `${fmtBytes(n)}/s`
}

export function nodeOnline(uuid) {
  return (state.nodes.find((n) => n.id === uuid) || {}).status === 'online'
}
