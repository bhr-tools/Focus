// The Timer screen: mode, project, durations, the ring and its controls,
// and today's sessions. The server owns the timer; this renders its
// snapshots and sends actions.

import { h, icon } from './dom.js';
import { confirmDialog } from '/kit/js/overlay.js';
import { DEFAULT_COLOR, canonicalProjectName, colorDot, formatDuration, formatHM } from './format.js';
import { createProjectPicker } from './picker.js';
import { IS_IOS, playSessionAudio, stopSessionAudio, unlockAudio } from './audio.js';

const $ = (id) => document.getElementById(id);
const RING_CIRCUMFERENCE = 2 * Math.PI * 140;
const DURATION_PRESETS = { work: 60, break: 10 };

let app = null;
let timerPicker = null;

export function renderTimer(snap) {
  const active = snap.status !== 'idle';
  const isFree = snap.mode === 'free';
  const projectColor = snap.project_color || DEFAULT_COLOR;

  $('timer-display').textContent = snap.time_str;
  $('phase-badge').textContent = isFree ? 'Stopwatch' : snap.phase === 'break' ? 'Break' : 'Work session';
  $('timer-project-name').textContent = snap.project;
  $('timer-project-dot').style.backgroundColor = projectColor;

  const ring = $('progress-ring-bar');
  const progress = isFree ? (active ? 1 : 0) : (snap.total_seconds > 0 ? snap.elapsed_seconds / snap.total_seconds : 0);
  ring.style.strokeDashoffset = RING_CIRCUMFERENCE * (1 - Math.min(progress, 1));
  ring.style.stroke = snap.color || projectColor;
  ring.style.opacity = progress > 0 ? 1 : 0; // a round cap would draw a dot at 0%

  const input = $('project-input');
  input.placeholder = snap.project;
  input.disabled = active;
  if (!input.value.trim()) $('project-color-dot').style.backgroundColor = projectColor;
  $('btn-project-dropdown').disabled = active;
  $('project-select').disabled = active;
  if (active) timerPicker?.close();

  const running = snap.status === 'running';
  $('btn-toggle').classList.toggle('running', running);
  $('label-toggle').textContent = running ? 'Pause' : snap.status === 'paused' ? 'Resume' : 'Start';
  $('icon-toggle').replaceChildren(icon(running ? 'pause' : 'play'));

  document.querySelectorAll('.mode-selector .segmented-btn').forEach((btn) => {
    const current = btn.dataset.mode === snap.mode;
    btn.classList.toggle('active', current);
    btn.disabled = active && !current;
    btn.title = btn.disabled ? 'Finish or cancel the current session to switch modes' : '';
  });
  $('durations').hidden = isFree;
  $('btn-next').hidden = isFree;
  $('btn-stop').hidden = !isFree;
  $('btn-cancel').disabled = !active;
  renderDurations(snap);

  document.title = running ? `${snap.time_str} · ${snap.project} — Focus`
    : snap.status === 'paused' ? `Paused · ${snap.project} — Focus` : 'Focus';
}

function renderDurations(snap) {
  const active = snap.status !== 'idle';
  $('durations').classList.toggle('disabled', active);
  for (const type of ['work', 'break']) {
    const minutes = type === 'work' ? snap.work_duration_min : snap.break_duration_min;
    const input = document.querySelector(`.custom-input input[data-type="${type}"]`);
    const box = input.closest('.custom-input');
    const editing = document.activeElement === input;
    const custom = editing || box.dataset.forced === 'true' || minutes !== DURATION_PRESETS[type];
    document.querySelectorAll(`.preset-btn[data-type="${type}"]`).forEach((btn) => {
      btn.classList.toggle('active', (btn.dataset.preset === 'custom') === custom);
      btn.disabled = active;
    });
    box.hidden = !custom;
    input.disabled = active;
    if (!editing) input.value = minutes;
  }
}

function currentProject() {
  return $('project-input').value.trim() || app.state.timer?.project || '';
}

/** Space: start, pause or resume. */
export function toggleTimer() {
  const snap = app.state.timer;
  if (!snap) return;
  unlockAudio();
  if (snap.status === 'idle') {
    if (IS_IOS) playSessionAudio();
    app.sendAction('start', { project: currentProject(), mode: snap.mode });
  } else {
    if (IS_IOS) (snap.status === 'running' ? stopSessionAudio() : playSessionAudio());
    app.sendAction('toggle');
  }
}

/** n: next phase (Pomodoro). */
export function nextPhase() {
  if (app.state.timer?.mode !== 'pomodoro') return;
  stopSessionAudio();
  app.sendAction('next');
}

/** s: finish and record (stopwatch). */
export function finishSession() {
  const snap = app.state.timer;
  if (snap?.mode !== 'free' || snap.status === 'idle') return;
  stopSessionAudio();
  app.sendAction('stop');
}

export async function cancelSession() {
  if (!app.state.timer || app.state.timer.status === 'idle') return;
  const ok = await confirmDialog('Cancel session?', 'The elapsed time will be lost and not recorded in your history.',
    { okText: 'Cancel session', cancelText: 'Keep going' });
  if (!ok) return;
  stopSessionAudio();
  app.sendAction('cancel');
}

function selectProject(name) {
  if (app.state.timer?.status !== 'idle' || !name.trim()) return;
  const input = $('project-input');
  input.value = canonicalProjectName(name);
  $('project-color-dot').style.backgroundColor = timerPicker.find(input.value)?.color || 'var(--text-dim)';
  app.sendAction('set_project', { project: input.value });
}

/** The native picker of touch screens lists this month's projects. */
export function renderProjectSelect() {
  const select = $('project-select');
  const first = new Option('Projects this month…', '', true, true);
  first.disabled = true;
  select.replaceChildren(first, ...app.state.monthProjects.map((p) => new Option(p.name, p.name)));
}

export function renderToday(sessions) {
  const list = $('today-list');
  let total = 0;
  const dayStart = new Date();
  dayStart.setHours(0, 0, 0, 0);
  const dayEnd = new Date(dayStart.getTime() + 86400000);
  const rows = sessions.map((s) => {
    const start = new Date(s.start_time);
    const end = new Date(s.end_time);
    // Only the part of a session falling today counts towards today's total.
    total += Math.max(0, (Math.min(end, dayEnd) - Math.max(start, dayStart)) / 1000);
    return h('button', { type: 'button', class: 'session-item', title: 'Edit or delete', onclick: () => app.editSession(s) },
      h('span', { class: 'session-project' }, colorDot(s.project_color), h('span', { class: 'session-project-name' }, s.project_name)),
      h('span', { class: 'session-range' }, `${formatHM(start)} – ${formatHM(end)}`),
      h('span', { class: 'session-duration' }, formatDuration(s.duration_seconds)));
  });
  list.replaceChildren(...(rows.length ? rows : [h('div', { class: 'empty-state' }, 'No session recorded today yet.')]));
  $('today-total').textContent = formatDuration(total);
}

export function initTimer(theApp) {
  app = theApp;
  document.querySelectorAll('.mode-selector .segmented-btn').forEach((btn) => {
    btn.addEventListener('click', () => {
      if (btn.disabled || app.state.timer?.status !== 'idle') return;
      app.sendAction('set_mode', { mode: btn.dataset.mode, project: currentProject() });
    });
  });
  $('btn-toggle').addEventListener('click', toggleTimer);
  $('btn-next').addEventListener('click', nextPhase);
  $('btn-stop').addEventListener('click', finishSession);
  $('btn-cancel').addEventListener('click', cancelSession);

  // Durations: presets apply at once, custom values on change (blur, Enter).
  document.querySelectorAll('.preset-btn').forEach((btn) => {
    btn.addEventListener('click', () => {
      const { type, preset } = btn.dataset;
      const input = document.querySelector(`.custom-input input[data-type="${type}"]`);
      const box = input.closest('.custom-input');
      if (preset === 'custom') {
        box.dataset.forced = 'true';
        box.hidden = false;
        input.focus();
        input.select();
        renderDurations(app.state.timer);
        return;
      }
      box.dataset.forced = 'false';
      app.sendAction('set_durations', { [`${type}_duration`]: Number(preset) });
    });
  });
  document.querySelectorAll('.custom-input input').forEach((input) => {
    input.addEventListener('change', () => {
      const minutes = parseInt(input.value, 10);
      if (minutes > 0 && minutes <= 1440) app.sendAction('set_durations', { [`${input.dataset.type}_duration`]: minutes });
      else if (app.state.timer) renderDurations(app.state.timer);
    });
    input.addEventListener('keydown', (e) => { if (e.key === 'Enter') input.blur(); });
    input.addEventListener('blur', () => {
      const box = input.closest('.custom-input');
      if (app.state.timer && parseInt(input.value, 10) === DURATION_PRESETS[input.dataset.type]) box.dataset.forced = 'false';
      if (app.state.timer) setTimeout(() => renderDurations(app.state.timer));
    });
  });

  // The project field.
  const input = $('project-input');
  timerPicker = createProjectPicker(input, {
    projects: () => ({ all: app.state.allProjects, month: app.state.monthProjects }),
    canOpen: () => app.state.timer?.status === 'idle',
    onChoose: (name) => {
      selectProject(name);
      input.blur();
    },
    onToggle: (open) => $('btn-project-dropdown').classList.toggle('open', open),
    onEnter: (e) => {
      e.preventDefault();
      input.blur();
    },
  });
  $('btn-project-dropdown').addEventListener('click', (e) => {
    e.stopPropagation();
    if (timerPicker.isOpen()) timerPicker.close();
    else timerPicker.open('');
  });
  $('project-select').addEventListener('change', (e) => {
    if (e.target.value) selectProject(e.target.value);
  });
  input.addEventListener('input', () => {
    const color = input.value.trim() ? timerPicker.find(input.value)?.color : app.state.timer?.project_color;
    $('project-color-dot').style.backgroundColor = color || 'var(--text-dim)';
  });
  // Leaving the field keeps the typed name, mapped onto an existing project.
  input.addEventListener('change', () => {
    const name = input.value.trim();
    if (name && app.state.timer && canonicalProjectName(name) !== app.state.timer.project) selectProject(name);
  });
}
