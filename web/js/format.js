// Dates, durations and colours as Focus shows them.

import { h } from './dom.js';

export const DEFAULT_COLOR = '#eb6f92';
export const MONTH_NAMES = ['January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December'];
export const MONTH_SHORT = MONTH_NAMES.map((m) => m.slice(0, 3));
export const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];

const pad = (n) => String(n).padStart(2, '0');

export function formatDuration(seconds) {
  const s = Math.max(0, seconds || 0);
  return `${Math.floor(s / 3600)}h ${pad(Math.floor((s % 3600) / 60))}m`;
}

export function formatClockShort(seconds) {
  return `${Math.floor(seconds / 3600)}:${pad(Math.floor((seconds % 3600) / 60))}`;
}

export function formatHM(date) {
  return `${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

export function toDatetimeLocal(date) {
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

export function clockMinutes(date) {
  return date.getHours() * 60 + date.getMinutes() + date.getSeconds() / 60;
}

export function shiftMonth({ year, month }, delta) {
  const d = new Date(year, month - 1 + delta, 1);
  return { year: d.getFullYear(), month: d.getMonth() + 1 };
}

/** Dark text on light project colours, white text on dark ones. */
export function readableTextColor(hex) {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex || '');
  if (!m) return '#fff';
  const n = parseInt(m[1], 16);
  const [r, g, b] = [(n >> 16) & 255, (n >> 8) & 255, n & 255];
  return (0.299 * r + 0.587 * g + 0.114 * b) > 160 ? '#191724' : '#fff';
}

export function colorDot(color) {
  const dot = h('span', { class: 'color-dot' });
  dot.style.backgroundColor = color || DEFAULT_COLOR;
  return dot;
}

/** Project names are stored in lowercase. */
export function canonicalProjectName(name) {
  return name.trim().toLowerCase();
}
