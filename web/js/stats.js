// The Statistics screen: key figures, projects and bar charts for a month,
// a year or all time.

import { h, icon } from './dom.js';
import { MONTH_NAMES, MONTH_SHORT, WEEKDAYS, colorDot, formatDuration, shiftMonth } from './format.js';
import { attachTooltip } from './tooltip.js';

const $ = (id) => document.getElementById(id);
const SVG = 'http://www.w3.org/2000/svg';

let app = null;
let request = 0;
let last = null;

export async function loadStats() {
  const { period, year, month } = app.state.stats;
  document.querySelectorAll('#stats-periods .segmented-btn').forEach((b) => b.classList.toggle('active', b.dataset.period === period));
  $('stats-stepper').hidden = period === 'all';
  $('stats-label').textContent = period === 'year' ? String(year) : `${MONTH_NAMES[month - 1]} ${year}`;
  $('card-monthly').hidden = period !== 'month' && period !== 'year';
  $('card-yearly').hidden = period === 'month';
  $('chart-monthly-title').textContent = period === 'year' ? `Monthly volume in ${year}` : 'Monthly volume (last 12 months)';

  const mine = ++request;
  let stats;
  try {
    stats = await app.api(`/api/stats?period=${period}&year=${year}&month=${month}`);
  } catch (err) {
    app.toastError(`Cannot load statistics: ${err.message}`);
    return;
  }
  if (mine !== request) return;
  last = stats;
  renderStats(stats);
}

export function setPeriod(period) {
  app.state.stats.period = period;
  loadStats();
}

export function stepStats(step) {
  const s = app.state.stats;
  if (s.period === 'all') return;
  if (s.period === 'year') s.year += step;
  else Object.assign(s, shiftMonth(s, step));
  loadStats();
}

export function thisPeriod() {
  const today = new Date();
  Object.assign(app.state.stats, { year: today.getFullYear(), month: today.getMonth() + 1 });
  loadStats();
}

const plural = (n, word) => `${n} ${word}${n === 1 ? '' : 's'}`;

function renderStats(stats) {
  $('kpi-total').textContent = formatDuration(stats.total_seconds);
  $('kpi-total-sub').textContent = plural(stats.session_count, 'session');
  $('kpi-avg').textContent = formatDuration(stats.average_per_day_seconds);
  $('kpi-avg-sub').textContent = plural(stats.active_days_count, 'active day');
  $('kpi-pomodoros').textContent = stats.pomodoro_count;
  $('kpi-pomodoros-sub').textContent = plural(stats.free_count, 'stopwatch session');
  const top = stats.projects[0];
  $('kpi-top').textContent = top ? top.project_name : '—';
  $('kpi-top').title = top ? top.project_name : '';
  $('kpi-top-sub').textContent = top ? `${formatDuration(top.total_seconds)} · ${top.percentage.toFixed(1)}%` : '';

  const container = $('projects-breakdown');
  if (stats.projects.length === 0) {
    container.replaceChildren(h('div', { class: 'empty-state' }, 'No session in this period.'));
  } else {
    const bar = h('div', { class: 'stacked-bar' }, stats.projects.map((p) => {
      const seg = h('div');
      seg.style.width = `${p.percentage}%`;
      seg.style.backgroundColor = p.color;
      attachTooltip(seg, [h('b', null, p.project_name), ` · ${p.percentage.toFixed(1)}% · ${formatDuration(p.total_seconds)}`]);
      return seg;
    }));
    const table = h('table', { class: 'data-table' },
      h('thead', null, h('tr', null,
        h('th', null, 'Project'), h('th', { class: 'num' }, 'Time'), h('th', { class: 'num' }, 'Share'),
        h('th', { class: 'num optional' }, 'Sessions'), h('th', { class: 'num optional' }, 'Average'))),
      h('tbody', null, stats.projects.map((p) => h('tr', null,
        h('td', null, h('button', { type: 'button', class: 'project-cell', title: 'Rename everywhere', onclick: () => app.renameProject(p.project_name) },
          colorDot(p.color), h('span', { class: 'project-cell-name' }, p.project_name), icon('edit', 'edit-icon'))),
        h('td', { class: 'num strong' }, formatDuration(p.total_seconds)),
        h('td', { class: 'num' }, `${p.percentage.toFixed(1)}%`),
        h('td', { class: 'num optional' }, String(p.session_count)),
        h('td', { class: 'num optional' }, formatDuration(p.average_duration_seconds))))));
    container.replaceChildren(bar, table,
      h('p', { class: 'hint hint-inline' }, 'Click a project to rename it everywhere. Use the name of another project to merge them.'));
  }

  const { period, year, month } = app.state.stats;
  const selectedMonth = `${year}-${String(month).padStart(2, '0')}`;
  barChart($('chart-yearly'), stats.yearly_breakdown.map((y) => ({
    label: y.label, value: y.total_seconds, selected: period === 'year' && y.year === year,
    onClick: () => { Object.assign(app.state.stats, { period: 'year', year: y.year }); loadStats(); },
  })), 'var(--accent)');
  barChart($('chart-monthly'), stats.monthly_breakdown.map((m) => ({
    label: MONTH_SHORT[parseInt(m.label.slice(5), 10) - 1], value: m.total_seconds, title: m.label,
    selected: period === 'month' && m.label === selectedMonth,
  })), 'var(--accent)');
  barChart($('chart-weekday'), stats.weekday_distribution.map((v, i) => ({ label: WEEKDAYS[i], value: v })), 'var(--rp-foam)');
  barChart($('chart-hourly'), stats.hourly_distribution.map((v, hour) => ({
    label: hour % 3 === 0 ? `${hour}h` : '', value: v, title: `${hour}:00`,
  })), 'var(--rp-iris)');
}

/** An SVG bar chart drawn at the container's pixel width, so labels keep their size. */
function barChart(container, bars, color) {
  const width = Math.max(container.clientWidth, 240);
  const height = 160;
  const bottom = 22;
  const slot = width / bars.length;
  const barWidth = Math.min(slot * 0.62, 56);
  const max = Math.max(1, ...bars.map((b) => b.value));
  const el = (tag, attrs) => {
    const node = document.createElementNS(SVG, tag);
    for (const [k, v] of Object.entries(attrs)) node.setAttribute(k, v);
    return node;
  };
  const svg = el('svg', { viewBox: `0 0 ${width} ${height}`, class: 'bar-chart' });
  bars.forEach((b, i) => {
    const barH = b.value > 0 ? Math.max(3, (b.value / max) * (height - bottom - 10)) : 0;
    const x = i * slot + (slot - barWidth) / 2;
    const rect = el('rect', { x, y: height - bottom - barH, width: barWidth, height: barH, rx: 3, fill: b.selected ? 'var(--rp-gold)' : color });
    const title = el('title', {});
    title.textContent = `${b.title || b.label}: ${formatDuration(b.value)}`;
    rect.append(title);
    if (b.onClick) {
      rect.classList.add('clickable');
      rect.addEventListener('click', b.onClick);
    }
    const text = el('text', { x: x + barWidth / 2, y: height - 6, class: b.selected ? 'bar-label selected' : 'bar-label' });
    text.textContent = b.label;
    svg.append(rect, text);
  });
  container.replaceChildren(svg);
}

export function initStats(theApp) {
  app = theApp;
  document.querySelectorAll('#stats-periods .segmented-btn').forEach((btn) => {
    btn.addEventListener('click', () => setPeriod(btn.dataset.period));
  });
  document.querySelectorAll('#stats-stepper .stepper-arrow').forEach((btn) => {
    btn.addEventListener('click', () => stepStats(Number(btn.dataset.step)));
  });
  $('stats-label').addEventListener('click', thisPeriod);
  let timer = 0;
  window.addEventListener('resize', () => {
    clearTimeout(timer);
    timer = setTimeout(() => {
      if (app.state.activeTab === 'tab-stats' && last) renderStats(last);
    }, 150);
  });
}
