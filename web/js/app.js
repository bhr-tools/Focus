// Focus — web client. The server owns the timer: this page renders the
// snapshots it streams (Server-Sent Events) and sends actions. Any change
// to recorded sessions bumps last_recorded_at in the snapshot, which
// reloads what is derived from them.

import { toast } from '/kit/js/overlay.js';
import { initKeys } from '/kit/js/keys.js';
import { createLock } from '/kit/js/lock.js';
import { api, onUnauthorized } from './api.js';
import './dom.js';
import { initAudio, playAlarm, updateMediaSession } from './audio.js';
import { closePickers, refreshPickers } from './picker.js';
import { cancelSession, finishSession, initTimer, nextPhase, renderProjectSelect, renderTimer, renderToday, toggleTimer } from './timer.js';
import { initMonth, loadMonth, stepMonth, thisMonth } from './month.js';
import { initStats, loadStats, stepStats, thisPeriod } from './stats.js';
import { editSession, initDialogs, renameProject, showConnect } from './dialogs.js';

const $ = (id) => document.getElementById(id);
const TABS = ['tab-timer', 'tab-month', 'tab-stats'];

function clientId() {
  try {
    let id = localStorage.getItem('focus_client_id');
    if (!id) {
      id = `client-${crypto.randomUUID ? crypto.randomUUID() : String(Math.random()).slice(2)}`;
      localStorage.setItem('focus_client_id', id);
    }
    return id;
  } catch {
    return 'client-ephemeral';
  }
}

const now = new Date();

const app = {
  clientId: clientId(),
  state: {
    activeTab: 'tab-timer',
    timer: null,          // latest snapshot from the server
    lastRecordedAt: null,
    lastAlertId: null,
    allProjects: [],      // every project (autocomplete)
    monthProjects: [],    // this month's (the picker without a query)
    month: { year: now.getFullYear(), month: now.getMonth() + 1 },
    stats: { period: 'month', year: now.getFullYear(), month: now.getMonth() + 1 },
  },
  api,
  editSession: (s) => editSession(s),
  renameProject: (name, year, month) => renameProject(name, year, month),
  toastError: (message) => toast(message, { type: 'error' }),

  async sendAction(action, payload = {}) {
    try {
      const snap = await api('/api/action', { method: 'POST', body: { action, client_id: app.clientId, ...payload } });
      onSnapshot(snap);
      return snap;
    } catch (err) {
      app.toastError(`Action failed: ${err.message}`);
      return null;
    }
  },

  /** Reloads everything derived from sessions; calls are coalesced. */
  refreshData() {
    clearTimeout(refreshTimer);
    refreshTimer = setTimeout(() => {
      loadToday();
      loadProjects();
      if (app.state.activeTab === 'tab-month') loadMonth();
      if (app.state.activeTab === 'tab-stats') loadStats();
    }, 100);
  },
};

let refreshTimer = 0;
let events = null;

function fromThisDevice(snap) {
  return !snap.origin_client_id || snap.origin_client_id === app.clientId;
}

function onSnapshot(snap) {
  const { state } = app;
  const previous = state.timer;
  state.timer = snap;
  if (state.lastRecordedAt !== null && snap.last_recorded_at > state.lastRecordedAt) app.refreshData();
  state.lastRecordedAt = snap.last_recorded_at;
  // A new alert: a countdown just reached zero. It rings where it started.
  if (state.lastAlertId !== null && snap.alert_id && snap.alert_id !== state.lastAlertId && fromThisDevice(snap)) {
    playAlarm(snap.alert_type);
  }
  state.lastAlertId = snap.alert_id || 0;
  renderTimer(snap);
  updateMediaSession(snap, previous, fromThisDevice(snap));
}

function connectEvents() {
  events?.close();
  events = new EventSource('/api/events');
  events.onmessage = (e) => {
    try {
      onSnapshot(JSON.parse(e.data));
    } catch (err) {
      console.error('Invalid event', err);
    }
  };
  // EventSource retries network errors itself but gives up on HTTP errors;
  // a poll keeps the page fresh meanwhile.
  events.onerror = () => {
    setTimeout(() => api('/api/state').then(onSnapshot).catch(() => {}), 1500);
    if (events.readyState === EventSource.CLOSED) {
      setTimeout(() => { if (unlocked) connectEvents(); }, 5000);
    }
  };
}

async function loadToday() {
  try {
    renderToday(await api('/api/sessions/today'));
  } catch { /* kept as it was */ }
}

async function loadProjects() {
  const month = `year=${now.getFullYear()}&month=${now.getMonth() + 1}`;
  try {
    [app.state.allProjects, app.state.monthProjects] = await Promise.all([api('/api/projects'), api(`/api/projects/month?${month}`)]);
  } catch {
    return;
  }
  renderProjectSelect();
  refreshPickers();
}

function switchTab(id) {
  app.state.activeTab = id;
  document.querySelectorAll('.nav-tab').forEach((t) => {
    const active = t.dataset.tab === id;
    t.classList.toggle('active', active);
    t.setAttribute('aria-selected', String(active));
  });
  document.querySelectorAll('.tab-pane').forEach((p) => p.classList.toggle('active', p.id === id));
  window.scrollTo(0, 0);
  if (id === 'tab-month') loadMonth();
  if (id === 'tab-stats') loadStats();
}

// Keys: Space, n and s drive the timer; h / l move by a period on the month
// and statistics; 1 2 3 (or & é " on AZERTY) change screen.
function keyTable() {
  const tab = app.state.activeTab;
  const table = [
    { keys: [' '], label: 'Start / pause / resume', group: 'Timer', helpKeys: ['Space'], run: toggleTimer },
    { keys: ['n'], label: 'Next phase (Pomodoro)', group: 'Timer', run: nextPhase },
    { keys: ['s'], label: 'Finish and record (stopwatch)', group: 'Timer', run: finishSession },
    { keys: ['x'], label: 'Cancel the session', group: 'Timer', run: cancelSession },
    { digit: true, label: 'Timer / Month / Statistics', group: 'Screens', helpKeys: ['1', '2', '3'], run: (n) => TABS[n - 1] && switchTab(TABS[n - 1]) },
    { keys: ['a'], label: 'Add an activity', group: 'History', run: () => editSession(null) },
    { keys: ['Escape'], label: 'Close, leave a field', group: 'General', helpKeys: ['Esc'], run: closePickers },
  ];
  if (tab === 'tab-month') {
    table.push(
      { keys: ['h', 'l'], label: 'Previous / next month', group: 'Month' },
      { keys: ['h', 'ArrowLeft'], hidden: true, run: () => stepMonth(-1) },
      { keys: ['l', 'ArrowRight'], hidden: true, run: () => stepMonth(1) },
      { keys: ['t'], label: 'This month', group: 'Month', run: thisMonth },
    );
  }
  if (tab === 'tab-stats') {
    table.push(
      { keys: ['h', 'l'], label: 'Previous / next period', group: 'Statistics' },
      { keys: ['h', 'ArrowLeft'], hidden: true, run: () => stepStats(-1) },
      { keys: ['l', 'ArrowRight'], hidden: true, run: () => stepStats(1) },
      { keys: ['t'], label: 'This period', group: 'Statistics', run: thisPeriod },
    );
  }
  return table;
}

let unlocked = false;

const lock = createLock({
  appName: 'Focus',
  afterLock: async () => {
    unlocked = false;
    events?.close();
  },
  onUnlocked: async () => {
    unlocked = true;
    connectEvents();
    app.refreshData();
    showConnect();
  },
});

onUnauthorized(() => {
  if (!unlocked) return;
  unlocked = false;
  events?.close();
  lock.showLogin();
});

function boot() {
  document.querySelectorAll('.nav-tab').forEach((tab) => tab.addEventListener('click', () => switchTab(tab.dataset.tab)));
  initAudio(app);
  initTimer(app);
  initMonth(app);
  initStats(app);
  initDialogs(app);
  initKeys({ table: keyTable, helpTitle: 'Keyboard shortcuts' });
  start();
}

/** Asks the server who is there; retries while it cannot be reached. */
async function start() {
  try {
    await lock.start();
  } catch {
    setTimeout(start, 3000);
    return;
  }
  if (!$('lock-button-slot').firstChild) {
    const button = lock.button();
    button.classList.add('icon-btn');
    $('lock-button-slot').append(button);
  }
}

boot();
