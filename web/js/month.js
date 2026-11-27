// The Month screen: one line per day, each session a block on a 24-hour
// track; the projects of the month above, renamed for that month by a click.

import { h } from './dom.js';
import { MONTH_NAMES, clockMinutes, colorDot, formatClockShort, formatDuration, formatHM, readableTextColor, shiftMonth, toDatetimeLocal } from './format.js';
import { attachTooltip } from './tooltip.js';

const $ = (id) => document.getElementById(id);

let app = null;
let request = 0;

export async function loadMonth() {
  const { year, month } = app.state.month;
  $('month-label').textContent = `${MONTH_NAMES[month - 1]} ${year}`;
  const mine = ++request;
  let data;
  try {
    data = await app.api(`/api/month?year=${year}&month=${month}`);
  } catch (err) {
    app.toastError(`Cannot load the month: ${err.message}`);
    return;
  }
  if (mine === request) renderMonth(data); // ignore out-of-order answers
}

export function stepMonth(step) {
  app.state.month = shiftMonth(app.state.month, step);
  loadMonth();
}

export function thisMonth() {
  const today = new Date();
  app.state.month = { year: today.getFullYear(), month: today.getMonth() + 1 };
  loadMonth();
}

function renderMonth(data) {
  $('month-total').textContent = formatDuration(data.total_seconds);
  $('month-legend').replaceChildren(...(data.projects.length
    ? data.projects.map((p) => h('button', {
      type: 'button', class: 'legend-item', title: `Rename “${p.name}” for ${data.month_name}`,
      onclick: () => app.renameProject(p.name, data.year, data.month),
    }, colorDot(p.color), h('span', null, p.name)))
    : [h('span', { class: 'legend-empty' }, 'No activity this month')]));

  const todayKey = toDatetimeLocal(new Date()).slice(0, 10);
  $('month-body').replaceChildren(...data.days.map((day) => {
    const track = h('div', { class: 'track' });
    for (const seg of day.sessions) {
      const segStart = new Date(seg.segment_start);
      const segEnd = new Date(seg.segment_end);
      // Wall-clock positions stay right on 23 h / 25 h daylight-saving days.
      const startMin = clockMinutes(segStart);
      const endMin = toDatetimeLocal(segEnd).slice(0, 10) === day.date ? clockMinutes(segEnd) : 1440;
      const block = h('button', {
        type: 'button', class: 'session-block',
        attrs: { 'aria-label': `${seg.project_name}, ${formatHM(segStart)} to ${formatHM(segEnd)}` },
        onclick: () => app.editSession(seg),
      }, h('span', null, seg.project_name));
      Object.assign(block.style, {
        left: `${(startMin / 1440) * 100}%`,
        width: `max(${(Math.max(endMin - startMin, 0) / 1440) * 100}%, 3px)`,
        backgroundColor: seg.project_color,
        color: readableTextColor(seg.project_color),
      });
      const start = new Date(seg.start_time);
      const end = new Date(seg.end_time);
      attachTooltip(block, [h('b', null, seg.project_name), h('br'), `${formatHM(start)} – ${formatHM(end)} · ${formatDuration(seg.duration_seconds)}`]);
      track.append(block);
    }
    const weekStart = day.day_of_week === 1 || day.day_number === 1;
    return h('tr', {
      class: ['day-row', (day.day_of_week === 0 || day.day_of_week === 6) && 'weekend', day.date === todayKey && 'today', day.date > todayKey && 'future'],
    },
    h('td', { class: 'col-date' },
      h('span', { class: 'day-full' }, weekStart ? h('span', { class: 'week-num' }, `W${day.week_number}`) : null, `${day.day_name} ${day.day_number}`),
      h('span', { class: 'day-short' }, String(day.day_number))),
    h('td', { class: 'col-track' }, track),
    h('td', { class: 'col-total' }, day.total_seconds > 0 ? formatClockShort(day.total_seconds) : ''));
  }));
}

export function initMonth(theApp) {
  app = theApp;
  document.querySelectorAll('#month-stepper .stepper-arrow').forEach((btn) => {
    btn.addEventListener('click', () => stepMonth(Number(btn.dataset.step)));
  });
  $('month-label').addEventListener('click', thisMonth);
  $('btn-add-activity').addEventListener('click', () => app.editSession(null));
  $('hour-scale').replaceChildren(...Array.from({ length: 24 }, (_, hour) => {
    const label = h('span', { class: ['hour-label', hour % 3 === 0 && 'major'] }, String(hour));
    label.style.left = `${((hour + 0.5) / 24) * 100}%`;
    return label;
  }));
}
