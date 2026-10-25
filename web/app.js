/**
 * Focus — web client.
 *
 * The server owns the timer: this page renders the snapshots it streams over
 * Server-Sent Events and sends actions. Any change to recorded sessions bumps
 * `last_recorded_at` in the snapshot, which triggers a data refresh.
 */
'use strict';

// ==================== CONSTANTS & STATE ====================
const RING_CIRCUMFERENCE = 2 * Math.PI * 140;
const DEFAULT_COLOR = '#eb6f92';
const MONTH_NAMES = ['January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December'];
const MONTH_SHORT = MONTH_NAMES.map(m => m.slice(0, 3));
const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
const DURATION_PRESETS = { work: 60, break: 10 };
const IS_LOCALHOST = ['localhost', '127.0.0.1', '[::1]'].includes(location.hostname);
const IS_IOS = /iPad|iPhone|iPod/.test(navigator.userAgent) ||
  (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1);

const now = new Date();
const state = {
  activeTab: 'tab-timer',
  timer: null,               // latest snapshot from the server
  lastRecordedAt: null,
  lastAlertId: null,
  allProjects: [],           // every project (autocomplete)
  monthProjects: [],         // projects of the current month (picker without query)
  todaySessions: [],
  month: { year: now.getFullYear(), month: now.getMonth() + 1 },
  stats: { period: 'month', year: now.getFullYear(), month: now.getMonth() + 1 },
  editing: null,             // session being edited, or { isNew: true }
  renaming: null,            // { year, month, name }
};

const $ = (id) => document.getElementById(id);

function clientId() {
  try {
    let id = localStorage.getItem('focus_client_id');
    if (!id) {
      id = 'client-' + (crypto.randomUUID ? crypto.randomUUID() : String(Math.random()).slice(2));
      localStorage.setItem('focus_client_id', id);
    }
    return id;
  } catch {
    return 'client-ephemeral';
  }
}
const CLIENT_ID = clientId();

// ==================== UTILITIES ====================
function escapeHtml(str) {
  return String(str ?? '').replace(/[&<>"']/g, (c) => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;',
  }[c]));
}

function formatDuration(seconds) {
  const s = Math.max(0, seconds || 0);
  return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`;
}

function formatClockShort(seconds) {
  return `${Math.floor(seconds / 3600)}:${String(Math.floor((seconds % 3600) / 60)).padStart(2, '0')}`;
}

function formatHM(date) {
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
}

function toDatetimeLocal(date) {
  const pad = (n) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function clockMinutes(date) {
  return date.getHours() * 60 + date.getMinutes() + date.getSeconds() / 60;
}

function shiftMonth({ year, month }, delta) {
  const d = new Date(year, month - 1 + delta, 1);
  return { year: d.getFullYear(), month: d.getMonth() + 1 };
}

// Dark text on light project colors, white text on dark ones.
function readableTextColor(hex) {
  const m = /^#?([0-9a-f]{6})$/i.exec(hex || '');
  if (!m) return '#fff';
  const n = parseInt(m[1], 16);
  const [r, g, b] = [(n >> 16) & 255, (n >> 8) & 255, n & 255];
  return (0.299 * r + 0.587 * g + 0.114 * b) > 160 ? '#191724' : '#fff';
}

function colorDot(color) {
  const dot = document.createElement('span');
  dot.className = 'color-dot';
  dot.style.backgroundColor = color || DEFAULT_COLOR;
  return dot;
}

let toastTimer = null;
function toast(message, kind = 'info') {
  const el = $('toast');
  el.textContent = message;
  el.className = `toast toast-${kind}`;
  el.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.hidden = true; }, kind === 'error' ? 6000 : 3500);
}

function showError(el, message) {
  el.textContent = message;
  el.hidden = !message;
}

// api() performs a request and returns the parsed JSON body. It throws an
// Error carrying the server's message on failure, and shows the lock screen
// on 401.
async function api(path, { method = 'GET', body, raw = false } = {}) {
  const opts = { method, headers: {} };
  if (body instanceof FormData) {
    opts.body = body;
  } else if (body !== undefined) {
    opts.headers['Content-Type'] = 'application/json';
    opts.body = JSON.stringify(body);
  }
  const res = await fetch(path, opts);
  if (res.status === 401 && !path.startsWith('/api/auth/')) {
    checkAuth();
  }
  if (!res.ok) {
    const text = (await res.text()).trim();
    throw new Error(text || `${res.status} ${res.statusText}`);
  }
  return raw ? res : res.json();
}

// ==================== THEME ====================
function systemTheme() {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function applyTheme(theme) {
  document.documentElement.dataset.theme = theme;
  $('meta-theme-color').setAttribute('content', theme === 'dark' ? '#191724' : '#faf4ed');
}

// The daemon's theme (~/.config/theme_state) wins when the page is opened on
// the same machine; otherwise the browser preference is used.
function syncTheme() {
  const serverTheme = IS_LOCALHOST && state.timer && state.timer.theme;
  applyTheme(serverTheme || systemTheme());
}

// ==================== TABS ====================
function switchTab(tabId) {
  state.activeTab = tabId;
  document.querySelectorAll('.nav-tab').forEach((t) => {
    const active = t.dataset.tab === tabId;
    t.classList.toggle('active', active);
    t.setAttribute('aria-selected', String(active));
  });
  document.querySelectorAll('.tab-pane').forEach((p) => p.classList.toggle('active', p.id === tabId));
  window.scrollTo(0, 0);
  if (tabId === 'tab-month') loadMonth();
  if (tabId === 'tab-stats') loadStats();
}

// ==================== DATA REFRESH ====================
let refreshTimer = null;
// refreshData reloads everything derived from sessions; calls are coalesced.
function refreshData() {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => {
    loadTodaySessions();
    loadProjects();
    if (state.activeTab === 'tab-month') loadMonth();
    if (state.activeTab === 'tab-stats') loadStats();
  }, 100);
}

// ==================== LIVE TIMER (SSE) ====================
let eventSource = null;

function connectEvents() {
  if (eventSource) eventSource.close();
  eventSource = new EventSource('/api/events');
  eventSource.onmessage = (e) => {
    try {
      renderTimer(JSON.parse(e.data));
    } catch (err) {
      console.error('Invalid event', err);
    }
  };
  // EventSource retries network errors by itself but gives up on HTTP errors;
  // a poll keeps the UI fresh meanwhile.
  eventSource.onerror = () => {
    setTimeout(() => api('/api/state').then(renderTimer).catch(() => {}), 1500);
    if (eventSource.readyState === EventSource.CLOSED) {
      setTimeout(() => { if ($('auth-overlay').hidden) connectEvents(); }, 5000);
    }
  };
}

async function sendAction(action, payload = {}) {
  try {
    const snap = await api('/api/action', { method: 'POST', body: { action, client_id: CLIENT_ID, ...payload } });
    renderTimer(snap);
    return snap;
  } catch (err) {
    toast(`Action failed: ${err.message}`, 'error');
    return null;
  }
}

function renderTimer(snap) {
  const previous = state.timer;
  state.timer = snap;
  syncTheme();

  if (state.lastRecordedAt !== null && snap.last_recorded_at > state.lastRecordedAt) {
    refreshData();
  }
  state.lastRecordedAt = snap.last_recorded_at;

  // A new alert means a countdown just reached zero.
  if (state.lastAlertId !== null && snap.alert_id && snap.alert_id !== state.lastAlertId) {
    onPhaseCompleted(snap);
  }
  state.lastAlertId = snap.alert_id || 0;

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
  if (active) closeProjectMenu();

  const toggle = $('btn-toggle');
  toggle.classList.toggle('running', snap.status === 'running');
  $('label-toggle').textContent = snap.status === 'running' ? 'Pause' : snap.status === 'paused' ? 'Resume' : 'Start';
  $('icon-toggle').innerHTML = snap.status === 'running'
    ? '<rect x="6" y="4" width="4" height="16"></rect><rect x="14" y="4" width="4" height="16"></rect>'
    : '<polygon points="5 3 19 12 5 21 5 3"></polygon>';

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

  document.title = snap.status === 'running' ? `${snap.time_str} · ${snap.project} — Focus`
    : snap.status === 'paused' ? `Paused · ${snap.project} — Focus` : 'Focus';

  updateMediaSession(snap, previous);
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

function onPhaseCompleted(snap) {
  const fromThisDevice = !snap.origin_client_id || snap.origin_client_id === CLIENT_ID;
  if (fromThisDevice) playAlarm(snap.alert_type);
}

// ==================== TIMER CONTROLS ====================
function currentProjectInput() {
  return $('project-input').value.trim() || (state.timer && state.timer.project) || '';
}

function initTimerControls() {
  document.querySelectorAll('.mode-selector .segmented-btn').forEach((btn) => {
    btn.addEventListener('click', () => {
      if (btn.disabled || !state.timer || state.timer.status !== 'idle') return;
      sendAction('set_mode', { mode: btn.dataset.mode, project: currentProjectInput() });
    });
  });

  $('btn-toggle').addEventListener('click', toggleTimer);
  $('btn-next').addEventListener('click', () => {
    stopSessionAudio();
    sendAction('next');
  });
  $('btn-stop').addEventListener('click', () => {
    stopSessionAudio();
    sendAction('stop');
  });
  $('btn-cancel').addEventListener('click', () => {
    if (state.timer && state.timer.status !== 'idle') openDialog('cancel-dialog');
  });
  $('btn-cancel-confirm').addEventListener('click', () => {
    closeDialog('cancel-dialog');
    stopSessionAudio();
    sendAction('cancel');
  });
  $('btn-sound-test').addEventListener('click', () => playAlarm(null, true));

  // Durations: presets apply at once, custom values on change (blur / Enter).
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
        renderDurations(state.timer);
        return;
      }
      box.dataset.forced = 'false';
      sendAction('set_durations', { [`${type}_duration`]: Number(preset) });
    });
  });
  document.querySelectorAll('.custom-input input').forEach((input) => {
    input.addEventListener('change', () => {
      const minutes = parseInt(input.value, 10);
      if (minutes > 0 && minutes <= 1440) {
        sendAction('set_durations', { [`${input.dataset.type}_duration`]: minutes });
      } else if (state.timer) {
        renderDurations(state.timer);
      }
    });
    input.addEventListener('keydown', (e) => { if (e.key === 'Enter') input.blur(); });
    input.addEventListener('blur', () => {
      const box = input.closest('.custom-input');
      if (state.timer && parseInt(input.value, 10) === DURATION_PRESETS[input.dataset.type]) box.dataset.forced = 'false';
      if (state.timer) setTimeout(() => renderDurations(state.timer));
    });
  });
}

function toggleTimer() {
  if (!state.timer) return;
  unlockAudio();
  if (state.timer.status === 'idle') {
    if (IS_IOS) playSessionAudio();
    sendAction('start', { project: currentProjectInput(), mode: state.timer.mode });
  } else {
    if (IS_IOS) state.timer.status === 'running' ? stopSessionAudio() : playSessionAudio();
    sendAction('toggle');
  }
}

// ==================== PROJECT PICKER ====================
// Typing in the project field opens a panel of matching projects, so that an
// existing project is reused instead of being created again under a variant.
const picker = { items: [], active: -1 };

function findProject(name) {
  const key = (name || '').trim().toLowerCase();
  return state.allProjects.find((p) => p.name.toLowerCase() === key) || null;
}

function projectColor(name) {
  const found = findProject(name);
  return found ? found.color : null;
}

// canonicalProjectName returns the stored spelling of an existing project.
function canonicalProjectName(name) {
  const found = findProject(name);
  return found ? found.name : name.trim();
}

async function loadProjects() {
  const month = `year=${now.getFullYear()}&month=${now.getMonth() + 1}`;
  try {
    [state.allProjects, state.monthProjects] = await Promise.all([api('/api/projects'), api(`/api/projects/month?${month}`)]);
  } catch {
    return;
  }
  const select = $('project-select');
  select.replaceChildren(new Option('Projects this month…', '', true, true));
  select.options[0].disabled = true;
  for (const p of state.monthProjects) select.append(new Option(p.name, p.name));

  const options = $('project-options');
  options.replaceChildren(...state.allProjects.map((p) => new Option(p.name)));
  if (!$('project-menu').hidden) openProjectMenu($('project-input').value);
}

// projectMatches ranks projects containing the query: prefix matches first.
function projectMatches(query) {
  const q = query.trim().toLowerCase();
  if (!q) return state.monthProjects.length > 0 ? state.monthProjects : state.allProjects;
  const rank = (p) => (p.name.toLowerCase().startsWith(q) ? 0 : 1);
  return state.allProjects
    .filter((p) => p.name.toLowerCase().includes(q))
    .sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
}

function openProjectMenu(query) {
  if (!state.timer || state.timer.status !== 'idle') return;
  const menu = $('project-menu');
  const typed = query.trim();
  const matches = projectMatches(query);
  picker.items = matches.map((p) => ({ name: p.name, color: p.color }));
  if (typed && !findProject(typed)) picker.items.push({ name: typed, create: true });
  picker.active = typed && picker.items.length > 0 ? 0 : -1;

  const header = document.createElement('div');
  header.className = 'menu-header';
  header.textContent = typed ? 'Matching projects' : state.monthProjects.length > 0 ? 'This month' : 'Projects';
  menu.replaceChildren(header);
  if (picker.items.length === 0) {
    const empty = document.createElement('div');
    empty.className = 'menu-empty';
    empty.textContent = 'No project yet: type a name';
    menu.append(empty);
  }
  picker.items.forEach((entry, i) => {
    const item = document.createElement('button');
    item.type = 'button';
    item.className = entry.create ? 'menu-item create' : 'menu-item';
    item.tabIndex = -1;
    const label = document.createElement('span');
    label.textContent = entry.create ? `Create “${entry.name}”` : entry.name;
    item.append(entry.create ? colorDot('var(--text-dim)') : colorDot(entry.color), label);
    // Keep the focus in the field: the selection happens on click.
    item.addEventListener('pointerdown', (e) => e.preventDefault());
    item.addEventListener('click', () => choosePickerItem(i));
    menu.append(item);
  });
  menu.hidden = false;
  $('btn-project-dropdown').classList.add('open');
  highlightPickerItem(picker.active);
}

function highlightPickerItem(index) {
  picker.active = index;
  $('project-menu').querySelectorAll('.menu-item').forEach((el, i) => {
    el.classList.toggle('active', i === index);
    if (i === index) el.scrollIntoView({ block: 'nearest' });
  });
}

function choosePickerItem(index) {
  const entry = picker.items[index];
  if (!entry) return;
  closeProjectMenu();
  selectProject(entry.name);
  $('project-input').blur();
}

function selectProject(name) {
  if (!state.timer || state.timer.status !== 'idle' || !name.trim()) return;
  const input = $('project-input');
  input.value = canonicalProjectName(name);
  $('project-color-dot').style.backgroundColor = projectColor(input.value) || 'var(--text-dim)';
  sendAction('set_project', { project: input.value });
}

function closeProjectMenu() {
  $('project-menu').hidden = true;
  $('btn-project-dropdown').classList.remove('open');
  picker.active = -1;
}

function initProjectPicker() {
  const input = $('project-input');
  $('btn-project-dropdown').addEventListener('click', (e) => {
    e.stopPropagation();
    if ($('project-menu').hidden) openProjectMenu('');
    else closeProjectMenu();
  });
  $('project-select').addEventListener('change', (e) => {
    if (e.target.value) selectProject(e.target.value);
  });
  input.addEventListener('input', () => {
    const color = input.value.trim() ? projectColor(input.value) : state.timer && state.timer.project_color;
    $('project-color-dot').style.backgroundColor = color || 'var(--text-dim)';
    if (input.value.trim()) openProjectMenu(input.value);
    else closeProjectMenu();
  });
  // Leaving the field keeps the typed name, mapped onto an existing project.
  input.addEventListener('change', () => {
    const name = input.value.trim();
    if (name && state.timer && canonicalProjectName(name) !== state.timer.project) selectProject(name);
  });
  input.addEventListener('blur', () => setTimeout(closeProjectMenu, 0));
  input.addEventListener('keydown', (e) => {
    const open = !$('project-menu').hidden;
    const count = picker.items.length;
    switch (e.key) {
      case 'ArrowDown':
      case 'ArrowUp':
        e.preventDefault();
        if (!open) {
          openProjectMenu(input.value);
        } else if (count > 0) {
          const step = e.key === 'ArrowDown' ? 1 : -1;
          highlightPickerItem((picker.active + step + count) % count);
        }
        break;
      case 'Enter':
        e.preventDefault();
        if (open && picker.active >= 0) choosePickerItem(picker.active);
        else input.blur();
        break;
      case 'Escape':
        closeProjectMenu();
        break;
    }
  });
  document.addEventListener('click', (e) => {
    if (!e.target.closest('.project-picker')) closeProjectMenu();
  });
}

// ==================== TODAY ====================
async function loadTodaySessions() {
  try {
    state.todaySessions = await api('/api/sessions/today');
  } catch {
    return;
  }
  const list = $('today-list');
  list.replaceChildren();
  let total = 0;
  const dayStart = new Date();
  dayStart.setHours(0, 0, 0, 0);
  const dayEnd = new Date(dayStart.getTime() + 86400000);

  for (const s of state.todaySessions) {
    const start = new Date(s.start_time);
    const end = new Date(s.end_time);
    // Only the part of a session falling today counts towards today's total.
    total += Math.max(0, (Math.min(end, dayEnd) - Math.max(start, dayStart)) / 1000);

    const item = document.createElement('button');
    item.type = 'button';
    item.className = 'session-item';
    item.title = 'Edit or delete';
    item.innerHTML = `
      <span class="session-project"></span>
      <span class="session-range">${formatHM(start)} – ${formatHM(end)}</span>
      <span class="session-duration">${formatDuration(s.duration_seconds)}</span>`;
    const name = document.createElement('span');
    name.className = 'session-project-name';
    name.textContent = s.project_name;
    item.querySelector('.session-project').append(colorDot(s.project_color), name);
    item.addEventListener('click', () => openSessionDialog(s));
    list.append(item);
  }
  if (state.todaySessions.length === 0) {
    list.innerHTML = '<div class="empty-state">No session recorded today yet.</div>';
  }
  $('today-total').textContent = formatDuration(total);
}

// ==================== MONTH VIEW ====================
function initMonthView() {
  document.querySelectorAll('#month-stepper .stepper-arrow').forEach((btn) => {
    btn.addEventListener('click', () => {
      state.month = shiftMonth(state.month, Number(btn.dataset.step));
      loadMonth();
    });
  });
  $('month-label').addEventListener('click', () => {
    const today = new Date();
    state.month = { year: today.getFullYear(), month: today.getMonth() + 1 };
    loadMonth();
  });
  $('btn-add-activity').addEventListener('click', () => openSessionDialog(null));

  const scale = $('hour-scale');
  for (let h = 0; h < 24; h++) {
    const label = document.createElement('span');
    label.className = 'hour-label' + (h % 3 === 0 ? ' major' : '');
    label.style.left = `${((h + 0.5) / 24) * 100}%`;
    label.textContent = h;
    scale.append(label);
  }
}

let monthRequest = 0;
async function loadMonth() {
  const { year, month } = state.month;
  $('month-label').textContent = `${MONTH_NAMES[month - 1]} ${year}`;
  const request = ++monthRequest;
  let data;
  try {
    data = await api(`/api/month?year=${year}&month=${month}`);
  } catch (err) {
    toast(`Cannot load the month: ${err.message}`, 'error');
    return;
  }
  if (request === monthRequest) renderMonth(data); // ignore out-of-order responses
}

function renderMonth(data) {
  $('month-total').textContent = formatDuration(data.total_seconds);

  const legend = $('month-legend');
  legend.replaceChildren();
  if (data.projects.length === 0) {
    legend.innerHTML = '<span class="legend-empty">No activity this month</span>';
  }
  for (const p of data.projects) {
    const chip = document.createElement('button');
    chip.type = 'button';
    chip.className = 'legend-item';
    chip.title = `Rename “${p.name}” for ${data.month_name}`;
    const label = document.createElement('span');
    label.textContent = p.name;
    chip.append(colorDot(p.color), label);
    chip.addEventListener('click', () => openRenameDialog(p.name, data.year, data.month));
    legend.append(chip);
  }

  const todayKey = toDatetimeLocal(new Date()).slice(0, 10);
  const body = $('month-body');
  body.replaceChildren();
  for (const day of data.days) {
    const row = document.createElement('tr');
    row.className = 'day-row';
    if (day.day_of_week === 0 || day.day_of_week === 6) row.classList.add('weekend');
    if (day.date === todayKey) row.classList.add('today');
    if (day.date > todayKey) row.classList.add('future');

    const week = day.day_of_week === 1 || day.day_number === 1 ? `<span class="week-num">W${day.week_number}</span>` : '';
    row.innerHTML = `
      <td class="col-date"><span class="day-full">${week}${day.day_name} ${day.day_number}</span><span class="day-short">${day.day_number}</span></td>
      <td class="col-track"><div class="track"></div></td>
      <td class="col-total">${day.total_seconds > 0 ? formatClockShort(day.total_seconds) : ''}</td>`;

    const track = row.querySelector('.track');
    for (const seg of day.sessions) {
      const segStart = new Date(seg.segment_start);
      const segEnd = new Date(seg.segment_end);
      // Wall-clock positions stay right on 23 h / 25 h daylight-saving days.
      const startMin = clockMinutes(segStart);
      const endMin = toDatetimeLocal(segEnd).slice(0, 10) === day.date ? clockMinutes(segEnd) : 1440;
      const left = (startMin / 1440) * 100;
      const width = (Math.max(endMin - startMin, 0) / 1440) * 100;

      const block = document.createElement('button');
      block.type = 'button';
      block.className = 'session-block';
      block.style.left = `${left}%`;
      block.style.width = `max(${width}%, 3px)`;
      block.style.backgroundColor = seg.project_color;
      block.style.color = readableTextColor(seg.project_color);
      block.setAttribute('aria-label', `${seg.project_name}, ${formatHM(segStart)} to ${formatHM(segEnd)}`);
      const label = document.createElement('span');
      label.textContent = seg.project_name;
      block.append(label);
      block.addEventListener('click', () => openSessionDialog(seg));
      const start = new Date(seg.start_time);
      const end = new Date(seg.end_time);
      attachTooltip(block, `<b>${escapeHtml(seg.project_name)}</b><br>${formatHM(start)} – ${formatHM(end)} · ${formatDuration(seg.duration_seconds)}`);
      track.append(block);
    }
    body.append(row);
  }
}

// ==================== TOOLTIP ====================
const canHover = window.matchMedia('(hover: hover) and (pointer: fine)').matches;

function attachTooltip(el, html) {
  if (!canHover) return;
  const tip = $('tooltip');
  const move = (e) => {
    const x = Math.min(e.clientX + 14, window.innerWidth - tip.offsetWidth - 8);
    const y = Math.min(e.clientY + 14, window.innerHeight - tip.offsetHeight - 8);
    tip.style.transform = `translate(${x}px, ${y}px)`;
  };
  el.addEventListener('mouseenter', (e) => {
    tip.innerHTML = html;
    tip.hidden = false;
    move(e);
  });
  el.addEventListener('mousemove', move);
  el.addEventListener('mouseleave', () => { tip.hidden = true; });
}

function hideTooltip() {
  $('tooltip').hidden = true;
}

// ==================== DIALOGS ====================
function openDialog(id) {
  hideTooltip();
  const dialog = $(id);
  if (!dialog.open) dialog.showModal();
}

function closeDialog(id) {
  const dialog = $(id);
  if (dialog.open) dialog.close();
}

function initDialogs() {
  document.querySelectorAll('dialog').forEach((dialog) => {
    dialog.querySelectorAll('[data-close]').forEach((btn) => btn.addEventListener('click', () => dialog.close()));
    // A click on the backdrop lands on the <dialog> element itself.
    dialog.addEventListener('click', (e) => { if (e.target === dialog) dialog.close(); });
  });

  // Session create / edit / delete
  $('session-dialog').addEventListener('close', () => { state.editing = null; });
  $('btn-session-delete').addEventListener('click', () => setDeleteConfirm(true));
  $('btn-session-delete-abort').addEventListener('click', () => setDeleteConfirm(false));
  $('btn-session-delete-confirm').addEventListener('click', deleteEditedSession);
  $('session-form').addEventListener('submit', saveEditedSession);

  // Rename a project for one month
  $('rename-form').addEventListener('submit', renameProject);

  // Password
  $('password-form').addEventListener('submit', changePassword);
  $('btn-logout').addEventListener('click', async () => {
    try {
      await api('/api/auth/logout', { method: 'POST' });
    } finally {
      location.reload();
    }
  });
}

function setDeleteConfirm(visible) {
  $('session-actions').hidden = visible;
  $('session-delete-confirm').hidden = !visible;
}

// openSessionDialog edits a session (or a month segment of one); null adds one.
function openSessionDialog(session) {
  const isNew = !session;
  state.editing = isNew ? { isNew: true } : session;
  $('session-dialog-title').textContent = isNew ? 'Add activity' : 'Edit activity';
  $('btn-session-save').textContent = isNew ? 'Add' : 'Save';
  $('btn-session-delete').hidden = isNew;
  setDeleteConfirm(false);
  showError($('session-error'), '');

  let start;
  let end;
  if (isNew) {
    end = new Date();
    end.setSeconds(0, 0);
    start = new Date(end.getTime() - 3600000);
  } else {
    start = new Date(session.start_time);
    end = new Date(session.end_time);
  }
  $('session-project').value = isNew ? (state.timer ? state.timer.project : '') : session.project_name;
  $('session-start').value = toDatetimeLocal(start);
  $('session-end').value = toDatetimeLocal(end);
  openDialog('session-dialog');
  if (isNew) $('session-project').select();
}

async function saveEditedSession(e) {
  e.preventDefault();
  const editing = state.editing;
  if (!editing) return;
  const start = new Date($('session-start').value);
  const end = new Date($('session-end').value);
  if (isNaN(start) || isNaN(end)) {
    showError($('session-error'), 'Please enter valid dates.');
    return;
  }
  if (end <= start) {
    showError($('session-error'), 'The end must be after the start.');
    return;
  }
  const body = {
    project_name: $('session-project').value.trim(),
    start_time: start.toISOString(),
    end_time: end.toISOString(),
  };
  try {
    if (editing.isNew) {
      await api('/api/sessions', { method: 'POST', body: { ...body, mode: 'pomodoro' } });
    } else {
      await api(`/api/sessions/${editing.id}`, { method: 'PUT', body });
    }
    closeDialog('session-dialog');
    refreshData();
  } catch (err) {
    showError($('session-error'), `Could not save: ${err.message}`);
  }
}

async function deleteEditedSession() {
  const editing = state.editing;
  if (!editing || editing.isNew) return;
  try {
    await api(`/api/sessions/${editing.id}`, { method: 'DELETE' });
    closeDialog('session-dialog');
    refreshData();
  } catch (err) {
    setDeleteConfirm(false);
    showError($('session-error'), `Could not delete: ${err.message}`);
  }
}

// openRenameDialog renames a project everywhere, or only within one month
// when year and month are given (month view).
function openRenameDialog(name, year, month) {
  const monthOnly = Boolean(year && month);
  state.renaming = { name, year, month, monthOnly };
  $('rename-dialog-title').textContent = monthOnly
    ? `Rename “${name}” in ${MONTH_NAMES[month - 1]} ${year}`
    : `Rename “${name}” everywhere`;
  $('rename-note').textContent = monthOnly
    ? 'Only the sessions of this month are renamed.'
    : 'Every session of this project is renamed. Choose an existing project to merge both.';
  $('rename-input').value = name;
  showError($('rename-error'), '');
  openDialog('rename-dialog');
  $('rename-input').select();
}

async function renameProject(e) {
  e.preventDefault();
  const { name, year, month, monthOnly } = state.renaming || {};
  const typed = $('rename-input').value.trim();
  // Merging reuses the other project's spelling; a change of case is kept as typed.
  const other = findProject(typed);
  const newName = other && other.name.toLowerCase() !== (name || '').toLowerCase() ? other.name : typed;
  if (!name || !newName || newName === name) {
    closeDialog('rename-dialog');
    return;
  }
  try {
    if (monthOnly) {
      await api('/api/projects/rename-month', { method: 'POST', body: { year, month, old_name: name, new_name: newName } });
    } else {
      await api('/api/projects/rename', { method: 'POST', body: { old_name: name, new_name: newName } });
    }
    closeDialog('rename-dialog');
    refreshData();
  } catch (err) {
    showError($('rename-error'), `Could not rename: ${err.message}`);
  }
}

async function changePassword(e) {
  e.preventDefault();
  const error = $('password-error');
  if ($('pwd-new').value !== $('pwd-confirm').value) {
    showError(error, 'The new passwords do not match.');
    return;
  }
  try {
    await api('/api/auth/change-password', {
      method: 'POST',
      body: { old_password: $('pwd-current').value, new_password: $('pwd-new').value },
    });
    closeDialog('password-dialog');
    toast('Password updated.', 'success');
  } catch (err) {
    showError(error, err.message);
  }
}

// ==================== STATISTICS ====================
function initStats() {
  document.querySelectorAll('#stats-periods .segmented-btn').forEach((btn) => {
    btn.addEventListener('click', () => {
      state.stats.period = btn.dataset.period;
      loadStats();
    });
  });
  document.querySelectorAll('#stats-stepper .stepper-arrow').forEach((btn) => {
    btn.addEventListener('click', () => {
      const step = Number(btn.dataset.step);
      if (state.stats.period === 'year') {
        state.stats.year += step;
      } else {
        Object.assign(state.stats, shiftMonth(state.stats, step));
      }
      loadStats();
    });
  });
  $('stats-label').addEventListener('click', () => {
    const today = new Date();
    Object.assign(state.stats, { year: today.getFullYear(), month: today.getMonth() + 1 });
    loadStats();
  });
}

let statsRequest = 0;
async function loadStats() {
  const { period, year, month } = state.stats;
  document.querySelectorAll('#stats-periods .segmented-btn').forEach((b) => b.classList.toggle('active', b.dataset.period === period));
  $('stats-stepper').hidden = period === 'all';
  $('stats-label').textContent = period === 'year' ? String(year) : `${MONTH_NAMES[month - 1]} ${year}`;
  $('card-monthly').hidden = period !== 'month' && period !== 'year';
  $('card-yearly').hidden = period === 'month';
  $('chart-monthly-title').textContent = period === 'year' ? `Monthly volume in ${year}` : 'Monthly volume (last 12 months)';

  const request = ++statsRequest;
  let stats;
  try {
    stats = await api(`/api/stats?period=${period}&year=${year}&month=${month}`);
  } catch (err) {
    toast(`Cannot load statistics: ${err.message}`, 'error');
    return;
  }
  if (request === statsRequest) {
    state.lastStats = stats;
    renderStats(stats);
  }
}

let resizeTimer = null;
window.addEventListener('resize', () => {
  clearTimeout(resizeTimer);
  resizeTimer = setTimeout(() => {
    if (state.activeTab === 'tab-stats' && state.lastStats) renderStats(state.lastStats);
  }, 150);
});

function renderStats(stats) {
  $('kpi-total').textContent = formatDuration(stats.total_seconds);
  $('kpi-total-sub').textContent = `${stats.session_count} session${stats.session_count === 1 ? '' : 's'}`;
  $('kpi-avg').textContent = formatDuration(stats.average_per_day_seconds);
  $('kpi-avg-sub').textContent = `${stats.active_days_count} active day${stats.active_days_count === 1 ? '' : 's'}`;
  $('kpi-pomodoros').textContent = stats.pomodoro_count;
  $('kpi-pomodoros-sub').textContent = `${stats.free_count} stopwatch session${stats.free_count === 1 ? '' : 's'}`;
  const top = stats.projects[0];
  $('kpi-top').textContent = top ? top.project_name : '—';
  $('kpi-top').title = top ? top.project_name : '';
  $('kpi-top-sub').textContent = top ? `${formatDuration(top.total_seconds)} · ${top.percentage.toFixed(1)}%` : '';

  const container = $('projects-breakdown');
  if (stats.projects.length === 0) {
    container.innerHTML = '<div class="empty-state">No session in this period.</div>';
  } else {
    const bar = document.createElement('div');
    bar.className = 'stacked-bar';
    for (const p of stats.projects) {
      const seg = document.createElement('div');
      seg.style.width = `${p.percentage}%`;
      seg.style.backgroundColor = p.color;
      attachTooltip(seg, `<b>${escapeHtml(p.project_name)}</b> · ${p.percentage.toFixed(1)}% · ${formatDuration(p.total_seconds)}`);
      bar.append(seg);
    }
    const table = document.createElement('table');
    table.className = 'data-table';
    table.innerHTML = `
      <thead><tr><th>Project</th><th class="num">Time</th><th class="num">Share</th><th class="num optional">Sessions</th><th class="num optional">Average</th></tr></thead>
      <tbody>${stats.projects.map((p, i) => `
        <tr>
          <td><button type="button" class="project-cell" data-index="${i}" title="Rename everywhere">
            <span class="color-dot" style="background-color:${escapeHtml(p.color)}"></span>
            <span class="project-cell-name">${escapeHtml(p.project_name)}</span>
            <svg class="edit-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true"><path d="M12 20h9"></path><path d="M16.5 3.5a2.12 2.12 0 0 1 3 3L7 19l-4 1 1-4Z"></path></svg>
          </button></td>
          <td class="num strong">${formatDuration(p.total_seconds)}</td>
          <td class="num">${p.percentage.toFixed(1)}%</td>
          <td class="num optional">${p.session_count}</td>
          <td class="num optional">${formatDuration(p.average_duration_seconds)}</td>
        </tr>`).join('')}
      </tbody>`;
    table.querySelectorAll('.project-cell').forEach((btn) => {
      btn.addEventListener('click', () => openRenameDialog(stats.projects[Number(btn.dataset.index)].project_name));
    });
    const hint = document.createElement('p');
    hint.className = 'hint hint-inline';
    hint.textContent = 'Click a project to rename it everywhere. Use the name of another project to merge them.';
    container.replaceChildren(bar, table, hint);
  }

  const { period, year, month } = state.stats;
  const selectedMonth = `${year}-${String(month).padStart(2, '0')}`;
  renderBarChart($('chart-yearly'), stats.yearly_breakdown.map((y) => ({
    label: y.label, value: y.total_seconds, selected: period === 'year' && y.year === year,
    onClick: () => { Object.assign(state.stats, { period: 'year', year: y.year }); loadStats(); },
  })), 'var(--rp-pine)');
  renderBarChart($('chart-monthly'), stats.monthly_breakdown.map((m) => ({
    label: MONTH_SHORT[parseInt(m.label.slice(5), 10) - 1], value: m.total_seconds, title: m.label,
    selected: period === 'month' && m.label === selectedMonth,
  })), 'var(--rp-pine)');
  renderBarChart($('chart-weekday'), stats.weekday_distribution.map((v, i) => ({ label: WEEKDAYS[i], value: v })), 'var(--rp-foam)');
  renderBarChart($('chart-hourly'), stats.hourly_distribution.map((v, h) => ({
    label: h % 3 === 0 ? `${h}h` : '', value: v, title: `${h}:00`,
  })), 'var(--rp-iris)');
}

// renderBarChart draws an SVG bar chart at the container's pixel width, so
// labels keep their size whatever the screen.
function renderBarChart(container, bars, color) {
  const width = Math.max(container.clientWidth, 240);
  const height = 160;
  const bottom = 22;
  const slot = width / bars.length;
  const barWidth = Math.min(slot * 0.62, 56);
  const max = Math.max(1, ...bars.map((b) => b.value));
  const svgNS = 'http://www.w3.org/2000/svg';
  const svg = document.createElementNS(svgNS, 'svg');
  svg.setAttribute('viewBox', `0 0 ${width} ${height}`);
  svg.setAttribute('class', 'bar-chart');

  bars.forEach((b, i) => {
    const h = b.value > 0 ? Math.max(3, (b.value / max) * (height - bottom - 10)) : 0;
    const x = i * slot + (slot - barWidth) / 2;
    const rect = document.createElementNS(svgNS, 'rect');
    rect.setAttribute('x', x);
    rect.setAttribute('y', height - bottom - h);
    rect.setAttribute('width', barWidth);
    rect.setAttribute('height', h);
    rect.setAttribute('rx', 3);
    rect.setAttribute('fill', b.selected ? 'var(--rp-gold)' : color);
    const title = document.createElementNS(svgNS, 'title');
    title.textContent = `${b.title || b.label}: ${formatDuration(b.value)}`;
    rect.append(title);
    if (b.onClick) {
      rect.classList.add('clickable');
      rect.addEventListener('click', b.onClick);
    }
    const text = document.createElementNS(svgNS, 'text');
    text.setAttribute('x', x + barWidth / 2);
    text.setAttribute('y', height - 6);
    text.setAttribute('class', b.selected ? 'bar-label selected' : 'bar-label');
    text.textContent = b.label;
    svg.append(rect, text);
  });
  container.replaceChildren(svg);
}

// ==================== HEADER MENU, IMPORT & EXPORT ====================
function initMenu() {
  const button = $('btn-more-menu');
  const menu = $('more-menu');
  const setOpen = (open) => {
    menu.hidden = !open;
    button.setAttribute('aria-expanded', String(open));
  };
  button.addEventListener('click', (e) => {
    e.stopPropagation();
    setOpen(menu.hidden);
  });
  document.addEventListener('click', (e) => {
    if (!menu.hidden && !menu.contains(e.target)) setOpen(false);
  });
  menu.querySelectorAll('[data-export]').forEach((item) => {
    item.addEventListener('click', () => {
      setOpen(false);
      exportData(item.dataset.export);
    });
  });
  $('menu-import').addEventListener('click', () => {
    setOpen(false);
    $('import-input').click();
  });
  $('menu-security').addEventListener('click', () => {
    setOpen(false);
    $('password-form').reset();
    showError($('password-error'), '');
    openDialog('password-dialog');
  });
  $('import-input').addEventListener('change', async (e) => {
    const file = e.target.files[0];
    e.target.value = '';
    if (file) await importFile(file);
  });
}

async function exportData(format) {
  try {
    const res = await api(`/api/export?format=${format}`, { raw: true });
    const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') || '');
    const url = URL.createObjectURL(await res.blob());
    const a = document.createElement('a');
    a.href = url;
    a.download = name ? name[1] : `focus-export.${format}`;
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  } catch (err) {
    toast(`Export failed: ${err.message}`, 'error');
  }
}

async function importFile(file) {
  const form = new FormData();
  form.append('file', file);
  toast('Importing…');
  try {
    const res = await api('/api/import', { method: 'POST', body: form });
    toast(`Imported ${res.imported} session${res.imported === 1 ? '' : 's'}, skipped ${res.skipped} (duplicates or invalid).`, 'success');
    refreshData();
  } catch (err) {
    toast(`Import failed: ${err.message}`, 'error');
  }
}

// ==================== KEYBOARD ====================
function initKeyboard() {
  window.addEventListener('keydown', (e) => {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    if (e.target.closest('input, textarea, select')) return;
    if (e.target.closest('button') && (e.key === ' ' || e.key === 'Enter')) return; // native activation
    if (document.querySelector('dialog[open]') || !$('auth-overlay').hidden) return;
    const mode = state.timer && state.timer.mode;
    switch (e.key) {
      case ' ':
        e.preventDefault();
        toggleTimer();
        break;
      case 'n':
      case 'N':
        if (mode === 'pomodoro') sendAction('next');
        break;
      case 's':
      case 'S':
        if (mode === 'free' && state.timer.status !== 'idle') sendAction('stop');
        break;
      case 'Escape':
        closeProjectMenu();
        break;
      case '1': switchTab('tab-timer'); break;
      case '2': switchTab('tab-month'); break;
      case '3': switchTab('tab-stats'); break;
    }
  });
}

// ==================== AUTHENTICATION ====================
let authMode = 'login';

async function checkAuth() {
  let status;
  try {
    status = await api('/api/auth/status');
  } catch {
    setTimeout(checkAuth, 3000); // server unreachable: retry
    return;
  }
  if (status.authenticated && status.has_password) {
    $('auth-overlay').hidden = true;
    document.body.classList.remove('locked');
    startApp();
    return;
  }
  authMode = status.has_password ? 'login' : 'setup';
  $('auth-subtitle').textContent = authMode === 'setup'
    ? 'Welcome! Choose a password to protect your data.'
    : 'Enter your password to unlock Focus.';
  $('auth-confirm-group').hidden = authMode !== 'setup';
  $('auth-confirm').required = authMode === 'setup';
  $('auth-password').autocomplete = authMode === 'setup' ? 'new-password' : 'current-password';
  $('auth-submit').textContent = authMode === 'setup' ? 'Set password' : 'Unlock';
  $('auth-overlay').hidden = false;
  document.body.classList.add('locked');
  $('auth-password').focus();
}

async function submitAuth(e) {
  e.preventDefault();
  const error = $('auth-error');
  const password = $('auth-password').value;
  showError(error, '');
  if (authMode === 'setup' && password !== $('auth-confirm').value) {
    showError(error, 'The passwords do not match.');
    return;
  }
  $('auth-submit').disabled = true;
  try {
    await api(`/api/auth/${authMode === 'setup' ? 'setup' : 'login'}`, { method: 'POST', body: { password } });
    $('auth-form').reset();
    await checkAuth();
  } catch (err) {
    showError(error, authMode === 'setup' ? err.message : 'Incorrect password.');
  } finally {
    $('auth-submit').disabled = false;
  }
}

// startApp (re)connects the live stream and loads the data; it also runs
// after a re-login.
function startApp() {
  connectEvents();
  refreshData();
}

// ==================== AUDIO ====================
// The alarm is decoded once into a Web Audio buffer (reliable on mobile once
// unlocked by a user gesture); the <audio> element is the fallback.
let audioCtx = null;
let alarmBuffer = null;
let lastAlarmAt = 0;

function unlockAudio() {
  const Ctx = window.AudioContext || window.webkitAudioContext;
  if (!Ctx) return;
  if (!audioCtx) audioCtx = new Ctx();
  if (audioCtx.state === 'suspended') audioCtx.resume().catch(() => {});
  if (!alarmBuffer) {
    alarmBuffer = 'loading';
    fetch('alarm.mp3')
      .then((res) => res.arrayBuffer())
      .then((data) => audioCtx.decodeAudioData(data))
      .then((buffer) => { alarmBuffer = buffer; })
      .catch(() => { alarmBuffer = null; });
  }
}

function playAlarm(alertType, force = false) {
  const t = Date.now();
  if (!force && t - lastAlarmAt < 4000) return; // the iOS stream may already ring
  lastAlarmAt = t;
  stopSessionAudio();
  unlockAudio();

  if (audioCtx && alarmBuffer instanceof AudioBuffer) {
    const source = audioCtx.createBufferSource();
    source.buffer = alarmBuffer;
    source.connect(audioCtx.destination);
    source.start();
  } else {
    const el = $('alarm-audio');
    el.currentTime = 0;
    el.play().catch(() => {});
  }

  const button = $('btn-sound-test');
  button.classList.add('ringing');
  setTimeout(() => button.classList.remove('ringing'), 1500);
  if (navigator.vibrate) navigator.vibrate([300, 150, 300, 150, 500]);

  if (alertType && 'Notification' in window && Notification.permission === 'granted') {
    const [title, body] = alertType === 'break_ended'
      ? ['Break over', 'Prepare a new session for deepwork.']
      : ['Deepwork session over', 'Time for a break.'];
    try {
      new Notification(title, { body, icon: 'icon-192.png' });
    } catch { /* not allowed in this context */ }
  }
}

document.addEventListener('pointerdown', unlockAudio, { once: true, passive: true });
document.addEventListener('keydown', unlockAudio, { once: true });
document.addEventListener('click', () => {
  if ('Notification' in window && Notification.permission === 'default') Notification.requestPermission();
}, { once: true });

// iOS suspends pages in the background: a server-generated MP3 made of
// silence followed by the alarm keeps playing on the lock screen and rings
// exactly when the countdown ends.
let streamRemaining = null;

function playSessionAudio() {
  if (!IS_IOS) return;
  const player = $('session-audio');
  const snap = state.timer;
  const pomodoro = !snap || snap.mode === 'pomodoro';
  if (pomodoro) {
    const remaining = snap && snap.remaining_seconds > 0 ? snap.remaining_seconds : (snap ? snap.work_duration_min * 60 : 3600);
    if (streamRemaining === null || player.paused || Math.abs(streamRemaining - remaining) > 3) {
      streamRemaining = remaining;
      player.loop = false;
      player.src = `/api/audio/session.mp3?remaining=${remaining}&t=${Date.now()}`;
    }
  } else if (!player.src.endsWith('silent.mp3')) {
    streamRemaining = 0;
    player.loop = true;
    player.src = 'silent.mp3';
  }
  player.play().catch(() => {});
}

function stopSessionAudio() {
  const player = $('session-audio');
  if (!player.paused) player.pause();
  streamRemaining = null;
}

$('session-audio').addEventListener('timeupdate', (e) => {
  // The stream is ringing on its own: do not ring a second time.
  const p = e.target;
  if (p.duration && p.duration - p.currentTime <= 4) lastAlarmAt = Date.now();
});

function updateMediaSession(snap, previous) {
  const fromThisDevice = !snap.origin_client_id || snap.origin_client_id === CLIENT_ID;
  if (snap.status === 'running' && fromThisDevice) requestWakeLock();
  else releaseWakeLock();
  if (!IS_IOS) return;

  if (snap.status === 'running' && fromThisDevice) {
    const phaseChanged = previous && (previous.phase !== snap.phase || previous.status !== 'running');
    if ($('session-audio').paused || phaseChanged) playSessionAudio();
  } else if (snap.status === 'idle' || !fromThisDevice) {
    stopSessionAudio();
  } else if (Date.now() - lastAlarmAt > 4000) {
    stopSessionAudio(); // paused, unless the alarm is still ringing
  }

  if ('mediaSession' in navigator && snap.status !== 'idle') {
    navigator.mediaSession.metadata = new MediaMetadata({
      title: `${snap.project} • ${snap.time_str}`,
      artist: snap.mode === 'free' ? 'Stopwatch' : snap.phase === 'break' ? 'Break' : 'Work',
      album: 'Focus',
    });
    navigator.mediaSession.playbackState = snap.status === 'running' ? 'playing' : 'paused';
  }
}

if (IS_IOS && 'mediaSession' in navigator) {
  for (const [event, action] of [['play', 'toggle'], ['pause', 'toggle'], ['nexttrack', 'next']]) {
    try {
      navigator.mediaSession.setActionHandler(event, () => sendAction(action));
    } catch { /* unsupported action */ }
  }
}

let wakeLock = null;
async function requestWakeLock() {
  if (!('wakeLock' in navigator) || wakeLock || document.visibilityState !== 'visible') return;
  try {
    wakeLock = await navigator.wakeLock.request('screen');
    wakeLock.addEventListener('release', () => { wakeLock = null; });
  } catch { /* denied */ }
}

function releaseWakeLock() {
  if (wakeLock) wakeLock.release().catch(() => {});
  wakeLock = null;
}

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible' && state.timer && state.timer.status === 'running') requestWakeLock();
});

// ==================== STARTUP ====================
document.addEventListener('DOMContentLoaded', () => {
  syncTheme();
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', syncTheme);
  document.querySelectorAll('.nav-tab').forEach((tab) => tab.addEventListener('click', () => switchTab(tab.dataset.tab)));
  initTimerControls();
  initProjectPicker();
  initMonthView();
  initStats();
  initDialogs();
  initMenu();
  initKeyboard();
  $('auth-form').addEventListener('submit', submitAuth);
  checkAuth();
});
