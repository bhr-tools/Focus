/**
 * Focus — Client Application Script
 * Minimalist Timetracker & Pomodoro with Rosé Pine theme
 */

// ==================== STATE ====================
const state = {
  activeTab: 'tab-timer',
  mode: 'pomodoro',        // 'pomodoro' | 'free'
  timer: null,            // Current snapshot from server
  defaultProject: 'Focus',
  allProjects: [],        // Cached list of all projects
  currentMonthProjects: [],
  todaySessions: [],
  monthHistory: [],       // Loaded months [{ year, month, data }]
  oldestLoaded: null,     // { year, month }
  newestLoaded: null,     // { year, month }
  statsPeriod: 'month',   // 'month' | 'year' | 'all'
  statsYear: new Date().getFullYear(),
  statsMonth: new Date().getMonth() + 1,
  theme: 'dark',
  activeEditSession: null
};

function getClientId() {
  let id = localStorage.getItem('focus_client_id');
  if (!id) {
    id = 'client-' + Math.random().toString(36).substring(2, 10) + Date.now().toString(36);
    localStorage.setItem('focus_client_id', id);
  }
  return id;
}

const CIRCLE_CIRCUMFERENCE = 2 * Math.PI * 140; // 879.6

const RosePinePalette = [
  "#eb6f92", "#f6c177", "#ebbcba", "#31748f", "#9ccfd8",
  "#c4a7e7", "#ea9a97", "#56949f", "#b4637a", "#ea9d34"
];

const MONTH_NAMES = [
  '', 'January', 'February', 'March', 'April', 'May', 'June',
  'July', 'August', 'September', 'October', 'November', 'December'
];

const MONTH_SHORT = [
  '', 'Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun',
  'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'
];

// ==================== CLIENT UUID GENERATOR ====================
function generateUUID() {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID();
  }
  return 'xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx'.replace(/[xy]/g, function(c) {
    const r = Math.random() * 16 | 0;
    const v = c === 'x' ? r : (r & 0x3 | 0x8);
    return v.toString(16);
  });
}

// ==================== DOM ELEMENT CACHE ====================
const dom = {};
function initDomCache() {
  dom.timerDisplay = document.getElementById('timer-display');
  dom.phaseBadge = document.getElementById('phase-badge');
  dom.timerProjectDot = document.getElementById('timer-project-dot');
  dom.timerProjectName = document.getElementById('timer-project-name');
  dom.btnToggle = document.getElementById('btn-toggle');
  dom.iconToggle = document.getElementById('icon-toggle');
  dom.labelToggle = document.getElementById('label-toggle');
  dom.progressRingBar = document.getElementById('progress-ring-bar');
  dom.projectInput = document.getElementById('project-input');
  dom.projectColorDot = document.getElementById('project-color-dot');
  dom.btnProjectDropdown = document.getElementById('btn-project-dropdown');
  dom.monthProjectsSelect = document.getElementById('month-projects-select');
  dom.durationsConfig = document.getElementById('durations-config');
  dom.modeBtns = document.querySelectorAll('.mode-btn');
  dom.todaySessionsList = document.getElementById('today-sessions-list');
  dom.todayTotalTime = document.getElementById('today-total-time');
  dom.monthsScrollContainer = document.getElementById('months-scroll-container');
  dom.editSessionModal = document.getElementById('edit-session-modal');
  dom.sessionTooltip = document.getElementById('session-tooltip');
  dom.authOverlay = document.getElementById('auth-overlay');
}

// ==================== INITIALIZATION ====================
document.addEventListener('DOMContentLoaded', () => {
  initDomCache();
  initThemeSync();
  initTabs();
  initControls();
  initProjectInput();
  initMonthView();
  initStats();
  initEditModal();
  initRenameProjectModal();
  initKeyboardShortcuts();
  initSecurityModal();
  cleanMobileKeyboardHints();

  // Initialize Authentication & Data Loading
  initAuth();
});

// ==================== THEME AUTO-SYNC & SYSTEM PREFERENCE ====================
// Clear any legacy manual override so the theme strictly follows the system as requested
localStorage.removeItem('focus_theme_override');
localStorage.removeItem('focus_outbox_queue');

function getSystemTheme() {
  if (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches) {
    return 'dark';
  }
  return 'light';
}

function applyTheme(theme) {
  const effectiveTheme = theme || getSystemTheme();
  state.theme = effectiveTheme;
  document.documentElement.setAttribute('data-theme', effectiveTheme);

  // Update status bar theme-color for mobile Safari / browser
  const metaTheme = document.getElementById('meta-theme-color');
  if (metaTheme) {
    metaTheme.setAttribute('content', effectiveTheme === 'dark' ? '#191724' : '#faf4ed');
  }
}

function initThemeSync() {
  // 1. Initial application based on client system preference
  applyTheme(getSystemTheme());

  // 2. React immediately to client OS theme change (e.g. iPhone dark/light mode toggle)
  if (window.matchMedia) {
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const onChange = (e) => {
      applyTheme(e.matches ? 'dark' : 'light');
    };
    if (mq.addEventListener) {
      mq.addEventListener('change', onChange);
    } else if (mq.addListener) {
      mq.addListener(onChange);
    }
  }

  // 3. For local desktop environment (NixOS localhost), sync with ~/.config/theme_state if available
  const isLocalhost = window.location.hostname === 'localhost' || window.location.hostname === '127.0.0.1';
  if (isLocalhost) {
    const checkServerTheme = async () => {
      try {
        const res = await fetch('/api/theme');
        if (res.ok) {
          const data = await res.json();
          if (data.theme && data.theme !== state.theme) {
            applyTheme(data.theme);
          }
        }
      } catch (err) {}
    };

    checkServerTheme();
    setInterval(checkServerTheme, 2000);
  }
}

// ==================== TABS ====================
function initTabs() {
  const tabs = document.querySelectorAll('.nav-tab');
  tabs.forEach(tab => {
    tab.addEventListener('click', () => {
      switchTab(tab.dataset.tab);
    });
  });
}

function switchTab(tabId) {
  state.activeTab = tabId;
  window.scrollTo(0, 0);

  document.querySelectorAll('.nav-tab').forEach(t => {
    t.classList.toggle('active', t.dataset.tab === tabId);
  });

  document.querySelectorAll('.tab-pane').forEach(p => {
    p.classList.toggle('active', p.id === tabId);
  });

  if (tabId === 'tab-month') {
    if (state.monthHistory.length === 0) {
      loadInitialMonths();
    } else {
      refreshCurrentMonth().then(() => {
        updateVisibleMonthLabel();
      });
    }
  } else if (tabId === 'tab-stats') {
    loadStats();
  }
}

// ==================== SSE REAL-TIME TIMER ====================
function initSSE() {
  const evtSource = new EventSource('/api/events');

  evtSource.onmessage = (e) => {
    try {
      const snap = JSON.parse(e.data);
      updateTimerUI(snap);

      const isLocalhost = window.location.hostname === 'localhost' || window.location.hostname === '127.0.0.1';
      if (isLocalhost && snap.theme && snap.theme !== state.theme) {
        applyTheme(snap.theme);
      }
    } catch (err) {
      console.error('SSE JSON error', err);
    }
  };

  evtSource.onerror = () => {
    setTimeout(pollState, 1500);
  };
}

async function pollState() {
  try {
    const res = await fetch('/api/state');
    if (res.ok) {
      const snap = await res.json();
      updateTimerUI(snap);
      const isLocalhost = window.location.hostname === 'localhost' || window.location.hostname === '127.0.0.1';
      if (isLocalhost && snap.theme && snap.theme !== state.theme) {
        applyTheme(snap.theme);
      }
    }
  } catch (err) {}
}

function updateTimerUI(snap) {
  state.timer = snap;

  // Track target alert time for background lock-screen audio watchdog
  if (snap.status === 'running' && snap.mode === 'pomodoro' && snap.remaining_seconds > 0) {
    state.targetAlertTime = Date.now() + (snap.remaining_seconds * 1000);
  } else {
    state.targetAlertTime = null;
  }

  // Work phase countdown reaches 0: ensure client calls stop action
  if (snap.mode === 'pomodoro' && snap.status === 'running' && snap.phase === 'work' && snap.remaining_seconds <= 0) {
    sendAction('stop');
  }

  // Natural session end alarm (only when countdown finishes while page is actively open)
  if (state.lastHandledAlertId === undefined) {
    state.lastHandledAlertId = snap.alert_id || 0;
  } else if (snap.alert_id && snap.alert_id > state.lastHandledAlertId) {
    state.lastHandledAlertId = snap.alert_id;
    state.targetAlertTime = null;
    const isOrigin = !snap.origin_client_id || snap.origin_client_id === getClientId();
    if (isOrigin) {
      playSessionEndAlert();
    } else {
      console.log('[Audio] Suppressing alarm sound: session was launched by another device (' + snap.origin_client_id + ')');
    }
  }

  const isWorkCompleted = (snap.alert_type === 'work_completed' && snap.alert_id && snap.alert_id > (state.lastHandledWorkAlertId || 0)) ||
                          (state.previousPhase === 'work' && snap.phase === 'break');
  if (isWorkCompleted) {
    state.lastHandledWorkAlertId = snap.alert_id || Date.now();
    loadTodaySessions();
    refreshCurrentMonth();
    loadMonthProjects();
    loadAllProjects();
    if (state.activeTab === 'tab-stats') loadStats();
    loadDefaultProject();
  }

  state.previousPhase = snap.phase;

  // Sync iOS Lock Screen / Dynamic Island Media Session & Wake Lock
  updateMediaSession(snap);

  if (state.lastRecordedAt && snap.last_recorded_at && snap.last_recorded_at > state.lastRecordedAt) {
    refreshCurrentMonth();
    loadTodaySessions();
    loadMonthProjects();
    loadAllProjects();
    if (state.activeTab === 'tab-stats') loadStats();
  }
  if (snap.last_recorded_at) {
    state.lastRecordedAt = snap.last_recorded_at;
  }

  const timerDisplay = dom.timerDisplay || document.getElementById('timer-display');
  const phaseBadge = dom.phaseBadge || document.getElementById('phase-badge');
  const projectDot = dom.timerProjectDot || document.getElementById('timer-project-dot');
  const projectName = dom.timerProjectName || document.getElementById('timer-project-name');
  const btnToggle = dom.btnToggle || document.getElementById('btn-toggle');
  const iconToggle = dom.iconToggle || document.getElementById('icon-toggle');
  const labelToggle = dom.labelToggle || document.getElementById('label-toggle');
  const ringBar = dom.progressRingBar || document.getElementById('progress-ring-bar');
  const input = dom.projectInput || document.getElementById('project-input');
  const inputDot = dom.projectColorDot || document.getElementById('project-color-dot');

  // Time text
  if (timerDisplay) timerDisplay.textContent = snap.time_str || '00:00';

  // Phase badge
  if (phaseBadge) {
    if (snap.mode === 'free') {
      phaseBadge.textContent = 'STOPWATCH';
    } else {
      phaseBadge.textContent = snap.phase === 'break' ? 'BREAK' : 'WORK SESSION';
    }
  }

  // True project color (never overwritten by break foam color)
  const trueProjectColor = snap.project_color || (snap.phase === 'work' ? snap.color : null) || getProjectColor(snap.project) || RosePinePalette[0];

  // Project badge below timer
  if (projectName) projectName.textContent = snap.project || state.defaultProject;
  if (projectDot) {
    projectDot.style.backgroundColor = trueProjectColor;
  }

  // Input placeholder and dot
  if (input) {
    input.placeholder = snap.project || state.defaultProject;
    if (!input.value.trim() && inputDot) {
      inputDot.style.backgroundColor = trueProjectColor;
    }
  }

  // Ring progress & color
  if (ringBar) {
    let offset = 0;
    if (snap.mode === 'pomodoro' && snap.total_seconds > 0) {
      const progress = snap.elapsed_seconds / snap.total_seconds;
      offset = CIRCLE_CIRCUMFERENCE * (1 - Math.min(progress, 1));
    } else if (snap.mode === 'free') {
      offset = 0; // full ring
    }
    ringBar.style.strokeDashoffset = offset;
    ringBar.style.stroke = snap.color || trueProjectColor;
  }

  // Toggle button status
  if (btnToggle && labelToggle && iconToggle) {
    if (snap.status === 'running') {
      btnToggle.classList.add('running');
      labelToggle.textContent = 'Pause';
      iconToggle.innerHTML = '<rect x="6" y="4" width="4" height="16"></rect><rect x="14" y="4" width="4" height="16"></rect>';
    } else if (snap.status === 'paused') {
      btnToggle.classList.remove('running');
      labelToggle.textContent = 'Resume';
      iconToggle.innerHTML = '<polygon points="5 3 19 12 5 21 5 3"></polygon>';
    } else {
      btnToggle.classList.remove('running');
      labelToggle.textContent = 'Start';
      iconToggle.innerHTML = '<polygon points="5 3 19 12 5 21 5 3"></polygon>';
    }
  }

  // Update Page Title
  if (snap.status === 'running') {
    document.title = `(${snap.time_str}) Focus • ${snap.project}`;
  } else if (snap.status === 'paused') {
    document.title = `[Pause] Focus`;
  } else {
    document.title = 'Focus';
  }

  // Update Mode buttons (Pomodoro vs Stopwatch):
  // When an active session is started/running/paused, gray out the opposite mode until stopped
  const modeBtns = dom.modeBtns || document.querySelectorAll('.mode-btn');
  const isTimerActive = snap.status !== 'idle';

  modeBtns.forEach(btn => {
    const isCurrentMode = btn.dataset.mode === snap.mode;
    btn.classList.toggle('active', isCurrentMode);

    if (isTimerActive && !isCurrentMode) {
      btn.disabled = true;
      btn.classList.add('disabled');
      btn.title = `Stop the ${snap.mode === 'pomodoro' ? 'Pomodoro' : 'Stopwatch'} session first to switch modes.`;
    } else {
      btn.disabled = false;
      btn.classList.remove('disabled');
      btn.removeAttribute('title');
    }
  });

  // Lock project name and duration settings while timer is active (running or paused)
  const projectInput = dom.projectInput || document.getElementById('project-input');
  const btnProjectDropdown = dom.btnProjectDropdown || document.getElementById('btn-project-dropdown');
  const monthProjectsSelect = dom.monthProjectsSelect || document.getElementById('month-projects-select');
  const durationsConfig = dom.durationsConfig || document.getElementById('durations-config');

  if (projectInput) {
    projectInput.disabled = isTimerActive;
    projectInput.classList.toggle('disabled', isTimerActive);
    if (isTimerActive) {
      projectInput.title = 'Cannot change project during an active session.';
    } else {
      projectInput.removeAttribute('title');
    }
  }
  if (btnProjectDropdown) {
    btnProjectDropdown.disabled = isTimerActive;
    btnProjectDropdown.classList.toggle('disabled', isTimerActive);
  }
  if (monthProjectsSelect) {
    monthProjectsSelect.disabled = isTimerActive;
  }
  if (durationsConfig) {
    durationsConfig.classList.toggle('disabled', isTimerActive);
    durationsConfig.querySelectorAll('button, input').forEach(el => {
      el.disabled = isTimerActive;
    });
  }

  if (snap.mode && state.mode !== snap.mode) {
    state.mode = snap.mode;
    updateModeUI();
  }
}

// ==================== CONTROLS ====================
function initControls() {
  // Request desktop notification permission on user interaction if available
  if ('Notification' in window && Notification.permission === 'default') {
    const askNotif = () => {
      Notification.requestPermission();
      document.removeEventListener('click', askNotif);
    };
    document.addEventListener('click', askNotif, { once: true });
  }

  // Mode selector: does NOT auto-start!
  const modeBtns = document.querySelectorAll('.mode-btn');
  modeBtns.forEach(btn => {
    btn.addEventListener('click', () => {
      // Disallow clicking if button is disabled or session is active in another mode
      if (btn.disabled || btn.classList.contains('disabled') || (state.timer && state.timer.status !== 'idle')) {
        return;
      }
      modeBtns.forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      state.mode = btn.dataset.mode;
      updateModeUI();

      let proj = document.getElementById('project-input').value.trim();
      if (!proj) proj = (state.timer ? state.timer.project : state.defaultProject);
      sendAction('set_mode', { project: proj, mode: state.mode });
    });
  });

  // Action buttons
  document.getElementById('btn-toggle').addEventListener('click', () => {
    // Synchronously activate/stop audio loop within user gesture for iOS Safari / Mobile
    onTimerToggleGesture();

    const inputVal = document.getElementById('project-input').value.trim();
    const proj = inputVal || (state.timer ? state.timer.project : state.defaultProject);
    if (state.timer && state.timer.status === 'idle') {
      sendAction('start', { project: proj, mode: state.mode });
    } else {
      sendAction('toggle');
    }
  });

  // Next: saves current work time and transitions to break, remains paused
  document.getElementById('btn-next').addEventListener('click', () => {
    stopSessionAudioStream();
    sendAction('next');
    setTimeout(() => {
      loadTodaySessions();
      refreshCurrentMonth();
      loadMonthProjects();
      loadAllProjects();
      if (state.activeTab === 'tab-stats') loadStats();
      loadDefaultProject();
    }, 250);
  });

  // Stop (Finish): finishes stopwatch session and records it
  document.getElementById('btn-stop').addEventListener('click', async () => {
    stopSessionAudioStream();
    await sendAction('stop');
    loadTodaySessions();
    refreshCurrentMonth();
    loadMonthProjects();
    loadAllProjects();
    if (state.activeTab === 'tab-stats') loadStats();
    loadDefaultProject();
  });

  const cancelModal = document.getElementById('cancel-confirm-modal');
  const cancelCloseBtn = document.getElementById('cancel-modal-close-btn');
  const cancelAbortBtn = document.getElementById('btn-cancel-abort');
  const cancelConfirmBtn = document.getElementById('btn-cancel-confirm');

  const closeCancelModal = () => {
    if (cancelModal) cancelModal.style.display = 'none';
  };

  if (cancelCloseBtn) cancelCloseBtn.addEventListener('click', closeCancelModal);
  if (cancelAbortBtn) cancelAbortBtn.addEventListener('click', closeCancelModal);
  if (cancelModal) {
    cancelModal.addEventListener('click', (e) => {
      if (e.target === cancelModal) closeCancelModal();
    });
  }

  if (cancelConfirmBtn) {
    cancelConfirmBtn.addEventListener('click', () => {
      closeCancelModal();
      stopSessionAudioStream();
      sendAction('cancel');
    });
  }

  const btnCancel = document.getElementById('btn-cancel');
  if (btnCancel) {
    btnCancel.addEventListener('click', () => {
      if (state.status === 'idle') return;
      if (cancelModal) {
        cancelModal.style.display = 'flex';
      } else {
        stopSessionAudioStream();
        sendAction('cancel');
      }
    });
  }

  // Sound test button in header
  const btnSoundTest = document.getElementById('btn-sound-test');
  if (btnSoundTest) {
    btnSoundTest.addEventListener('click', () => {
      triggerAlarmSound(true);
    });
  }

  // Durations presets (60 / Custom & 10 / Custom)
  initDurationPresets();
}

function updateModeUI() {
  const durConfig = document.getElementById('durations-config');
  const btnNext = document.getElementById('btn-next');
  const btnStop = document.getElementById('btn-stop');

  if (state.mode === 'free') {
    durConfig.style.display = 'none';
    btnNext.style.display = 'none';
    btnStop.style.display = 'inline-flex';
  } else {
    durConfig.style.display = 'flex';
    btnNext.style.display = 'inline-flex';
    btnStop.style.display = 'none';
  }
}

function initDurationPresets() {
  const workInput = document.getElementById('work-duration-input');
  const breakInput = document.getElementById('break-duration-input');
  const workCustomBox = document.getElementById('work-custom-box');
  const breakCustomBox = document.getElementById('break-custom-box');

  const onDurationsChange = () => {
    if (state.timer && state.timer.status !== 'idle') return;
    let w = parseInt(workInput.value, 10);
    let b = parseInt(breakInput.value, 10);
    if (!w || w <= 0) w = 60;
    if (!b || b <= 0) b = 10;
    sendAction('set_durations', { work_duration: w, break_duration: b });
  };

  workInput.addEventListener('input', onDurationsChange);
  workInput.addEventListener('change', onDurationsChange);
  workInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      onDurationsChange();
      workInput.blur();
    }
  });

  breakInput.addEventListener('input', onDurationsChange);
  breakInput.addEventListener('change', onDurationsChange);
  breakInput.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      onDurationsChange();
      breakInput.blur();
    }
  });

  document.querySelectorAll('.preset-btn').forEach(btn => {
    btn.addEventListener('click', () => {
      if (btn.disabled || (state.timer && state.timer.status !== 'idle')) return;
      const type = btn.dataset.type;
      const val = btn.dataset.val;

      document.querySelectorAll(`.preset-btn[data-type="${type}"]`).forEach(b => b.classList.remove('active'));
      btn.classList.add('active');

      if (type === 'work') {
        if (val === 'custom') {
          workCustomBox.style.display = 'inline-flex';
          workInput.focus();
          onDurationsChange();
        } else {
          workCustomBox.style.display = 'none';
          workInput.value = 60;
          onDurationsChange();
        }
      } else {
        if (val === 'custom') {
          breakCustomBox.style.display = 'inline-flex';
          breakInput.focus();
          onDurationsChange();
        } else {
          breakCustomBox.style.display = 'none';
          breakInput.value = 10;
          onDurationsChange();
        }
      }
    });
  });
}

async function sendAction(action, payload = {}) {
  try {
    const res = await fetch('/api/action', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ action, client_id: getClientId(), ...payload })
    });
    if (res.ok) {
      const snap = await res.json();
      updateTimerUI(snap);
      return snap;
    }
  } catch (err) {
    console.error('[Action] Error sending action:', action, err);
  }
}

// ==================== PROJECTS ====================
function getProjectColor(name) {
  if (!name) return RosePinePalette[0];
  const trimmed = name.trim().toLowerCase();
  const found = state.allProjects.find(p => p.name.toLowerCase() === trimmed) ||
                state.currentMonthProjects.find(p => p.name.toLowerCase() === trimmed);
  if (found && found.color) return found.color;

  return RosePinePalette[state.allProjects.length % RosePinePalette.length];
}

async function loadAllProjects() {
  try {
    const res = await fetch('/api/projects');
    if (res.ok) {
      state.allProjects = await res.json() || [];
      renderProjectDropdownList();
    }
  } catch (err) {}
}

function renderProjectDropdownList() {
  const menu = document.getElementById('project-dropdown-menu');
  const select = document.getElementById('month-projects-select');

  // Strictly propose projects that appeared in the current month
  const map = new Map();
  (state.currentMonthProjects || []).forEach(p => {
    if (p && p.name) map.set(p.name.toLowerCase(), p);
  });

  const projects = Array.from(map.values()).sort((a, b) => a.name.localeCompare(b.name));

  // 1. Mobile native select
  if (select) {
    select.innerHTML = '<option value="" disabled selected>Projects this month...</option>';
    projects.forEach(p => {
      const opt = document.createElement('option');
      opt.value = p.name;
      opt.textContent = p.name;
      select.appendChild(opt);
    });
  }

  // 2. Desktop custom Rosé Pine dropdown menu
  if (!menu) return;
  menu.innerHTML = '';
  if (projects.length === 0) {
    menu.innerHTML = '<div class="project-dropdown-empty">No projects this month</div>';
    return;
  }

  projects.forEach(p => {
    const item = document.createElement('div');
    item.className = 'project-dropdown-item';
    const color = p.color || getProjectColor(p.name);
    item.innerHTML = `
      <span class="color-dot" style="background-color: ${color}"></span>
      <span>${escapeHtml(p.name)}</span>
    `;
    item.addEventListener('click', (e) => {
      e.stopPropagation();
      selectProject(p.name);
      closeProjectDropdown();
    });
    menu.appendChild(item);
  });
}

function selectProject(name) {
  if (state.timer && state.timer.status !== 'idle') return;
  const input = document.getElementById('project-input');
  const dot = document.getElementById('project-color-dot');
  if (input) {
    input.value = name;
  }
  const col = getProjectColor(name);
  if (dot) dot.style.backgroundColor = col;
  sendAction('set_project', { project: name });
  loadAllProjects();
}

function toggleProjectDropdown(e) {
  if (e) e.stopPropagation();
  if (state.timer && state.timer.status !== 'idle') return;
  const menu = document.getElementById('project-dropdown-menu');
  const btn = document.getElementById('btn-project-dropdown');
  if (!menu) return;

  const isOpen = menu.style.display !== 'none';
  if (isOpen) {
    closeProjectDropdown();
  } else {
    renderProjectDropdownList();
    menu.style.display = 'block';
    if (btn) btn.classList.add('open');
  }
}

function closeProjectDropdown() {
  const menu = document.getElementById('project-dropdown-menu');
  const btn = document.getElementById('btn-project-dropdown');
  if (menu) menu.style.display = 'none';
  if (btn) btn.classList.remove('open');
}

// Global click to close dropdown when clicking outside
document.addEventListener('click', (e) => {
  const bar = document.querySelector('.project-selector-bar');
  if (bar && !bar.contains(e.target)) {
    closeProjectDropdown();
  }
});

function initProjectInput() {
  const input = document.getElementById('project-input');
  const monthSelect = document.getElementById('month-projects-select');
  const dot = document.getElementById('project-color-dot');
  const dropdownBtn = document.getElementById('btn-project-dropdown');

  if (dropdownBtn) {
    dropdownBtn.addEventListener('click', (e) => {
      toggleProjectDropdown(e);
    });
  }

  // Immediately update dot color as user types
  input.addEventListener('input', () => {
    const val = input.value.trim();
    if (val) {
      const col = getProjectColor(val);
      if (dot) dot.style.backgroundColor = col;
    } else if (state.timer) {
      const col = state.timer.project_color || (state.timer.phase === 'work' ? state.timer.color : null) || RosePinePalette[0];
      if (dot) dot.style.backgroundColor = col;
    }
  });

  input.addEventListener('change', () => {
    const val = input.value.trim();
    const name = val || (state.timer ? state.timer.project : state.defaultProject);
    sendAction('set_project', { project: name });
    loadAllProjects();
  });

  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      input.blur();
      closeProjectDropdown();
    } else if (e.key === 'Escape') {
      closeProjectDropdown();
    }
  });

  if (monthSelect) {
    monthSelect.addEventListener('change', () => {
      const selected = monthSelect.value;
      if (selected) {
        selectProject(selected);
      }
    });
  }
}

async function loadDefaultProject() {
  try {
    const res = await fetch('/api/projects/last');
    if (res.ok) {
      const p = await res.json();
      state.defaultProject = p.name || 'Focus';
      const input = document.getElementById('project-input');
      if (input && !input.value.trim()) {
        input.placeholder = state.defaultProject;
      }
      const dot = document.getElementById('project-color-dot');
      if (dot && p.color && (!input || !input.value.trim())) {
        dot.style.backgroundColor = p.color;
      }
    }
  } catch (err) {}

  loadMonthProjects();
}

async function loadMonthProjects() {
  const now = new Date();
  try {
    const res = await fetch(`/api/projects/month?year=${now.getFullYear()}&month=${now.getMonth() + 1}`);
    if (res.ok) {
      state.currentMonthProjects = await res.json() || [];
      renderProjectDropdownList();
    }
  } catch (err) {}
}

// ==================== TODAY'S SESSIONS ====================
async function loadTodaySessions() {
  try {
    const res = await fetch('/api/sessions/today');
    if (res.ok) {
      state.todaySessions = (await res.json()) || [];
      renderTodaySessions();
    }
  } catch (err) {
    console.error('Failed to load today sessions:', err);
  }
}

function renderTodaySessions() {
  const list = dom.todaySessionsList || document.getElementById('today-sessions-list');
  const totalBadge = dom.todayTotalTime || document.getElementById('today-total-time');
  if (!list || !totalBadge) return;

  if (state.todaySessions.length === 0) {
    list.innerHTML = '<div class="empty-state">No sessions recorded today. Start your first timer!</div>';
    totalBadge.textContent = '0h 00m';
    return;
  }

  let totalSec = 0;
  list.innerHTML = '';

  state.todaySessions.forEach(s => {
    totalSec += s.duration_seconds;

    const start = new Date(s.start_time);
    const end = new Date(s.end_time);
    const timeRangeStr = `${formatHM(start)} - ${formatHM(end)}`;
    const durStr = formatDuration(s.duration_seconds);

    const item = document.createElement('div');
    item.className = 'session-item';
    item.style.cursor = 'pointer';
    item.title = 'Click to edit or delete';
    item.innerHTML = `
      <div class="session-left">
        <span class="session-proj-badge">
          <span class="color-dot" style="background-color: ${s.project_color || getProjectColor(s.project_name)}"></span>
          ${escapeHtml(s.project_name)}
        </span>
        <span class="session-time-range">${timeRangeStr}</span>
      </div>
      <div class="session-right">
        <span class="session-dur">${durStr}</span>
      </div>
    `;

    item.addEventListener('click', () => {
      openEditModal(s);
    });

    list.appendChild(item);
  });

  totalBadge.textContent = formatDuration(totalSec);
}

// ==================== TAB 2 : TIMEWARRIOR MONTH VIEW ====================
let currentYear = new Date().getFullYear();
let currentMonth = new Date().getMonth() + 1;

function initMonthView() {
  const btnPrev = document.getElementById('btn-month-prev');
  const btnLabel = document.getElementById('btn-month-label');
  const btnNext = document.getElementById('btn-month-next');

  if (btnPrev) {
    btnPrev.addEventListener('click', (e) => {
      e.preventDefault();
      currentMonth--;
      if (currentMonth < 1) {
        currentMonth = 12;
        currentYear--;
      }
      loadMonthData(currentYear, currentMonth);
    });
  }

  // Ensure #btn-month-label resets to the current system month and year
  if (btnLabel) {
    btnLabel.addEventListener('click', (e) => {
      e.preventDefault();
      const now = new Date();
      currentMonth = now.getMonth() + 1;
      currentYear = now.getFullYear();
      loadMonthData(currentYear, currentMonth);
    });
  }

  if (btnNext) {
    btnNext.addEventListener('click', (e) => {
      e.preventDefault();
      currentMonth++;
      if (currentMonth > 12) {
        currentMonth = 1;
        currentYear++;
      }
      loadMonthData(currentYear, currentMonth);
    });
  }

  // Throttled scroll listener using requestAnimationFrame
  const container = document.getElementById('months-scroll-container');
  let scrollTicking = false;
  const onScroll = () => {
    if (state.activeTab !== 'tab-month') return;
    if (!scrollTicking) {
      window.requestAnimationFrame(() => {
        updateVisibleMonthLabel();
        scrollTicking = false;
      });
      scrollTicking = true;
    }
  };

  if (container) {
    container.addEventListener('scroll', onScroll, { passive: true });
  }
  window.addEventListener('scroll', onScroll, { passive: true });
}

async function loadAndPrependPrevMonth() {
  if (state.isLoadingPrevMonth || !state.oldestLoaded) return;
  state.isLoadingPrevMonth = true;

  let prevY = state.oldestLoaded.year;
  let prevM = state.oldestLoaded.month - 1;
  if (prevM < 1) {
    prevM = 12;
    prevY--;
  }
  state.oldestLoaded = { year: prevY, month: prevM };

  const container = document.getElementById('months-scroll-container');
  const isMobile = window.innerWidth <= 768;
  const oldScrollTop = isMobile ? (window.pageYOffset || document.documentElement.scrollTop) : (container ? container.scrollTop : 0);
  const oldScrollHeight = isMobile ? document.documentElement.scrollHeight : (container ? container.scrollHeight : 0);

  const btn = document.getElementById('btn-load-prev-month');
  if (btn) {
    btn.innerHTML = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" class="spin-icon"><circle cx="12" cy="12" r="10"></circle><path d="M12 6v6l4 2"></path></svg> <span>Loading...</span>`;
  }

  const mData = await fetchMonthData(prevY, prevM);
  if (mData) {
    state.monthHistory.unshift(mData);
    renderMonthBlocks();

    // Preserve scroll position so page doesn't jump
    if (isMobile) {
      const newScrollHeight = document.documentElement.scrollHeight;
      window.scrollTo(0, oldScrollTop + (newScrollHeight - oldScrollHeight));
    } else if (container && container.scrollTop > 0) {
      const newScrollHeight = container.scrollHeight;
      container.scrollTop = oldScrollTop + (newScrollHeight - oldScrollHeight);
    }
  }

  setTimeout(() => {
    state.isLoadingPrevMonth = false;
  }, 300);
}

function getVisibleMonthBlock() {
  const blocks = Array.from(document.querySelectorAll('.month-block'));
  if (blocks.length === 0) return null;

  const isMobile = window.innerWidth <= 768;
  const container = document.getElementById('months-scroll-container');

  if (isMobile) {
    const scrollY = window.pageYOffset || document.documentElement.scrollTop;
    const windowH = window.innerHeight;
    const docH = document.documentElement.scrollHeight;

    // Boundary checks: very bottom or very top
    if (windowH + scrollY >= docH - 80) {
      return blocks[blocks.length - 1];
    }
    if (scrollY <= 50) {
      return blocks[0];
    }

    // Focal reading line on phone (140px from top of screen)
    const focalY = 140;
    for (const block of blocks) {
      const rect = block.getBoundingClientRect();
      if (rect.top <= focalY && rect.bottom > focalY) {
        return block;
      }
    }

    // Fallback: block closest to focal line
    let closestBlock = blocks[0];
    let minDistance = Infinity;
    for (const block of blocks) {
      const rect = block.getBoundingClientRect();
      const dist = Math.abs(rect.top - focalY);
      if (dist < minDistance) {
        minDistance = dist;
        closestBlock = block;
      }
    }
    return closestBlock;
  } else {
    // Desktop inside container
    const cRect = container ? container.getBoundingClientRect() : { top: 0 };
    const focalY = cRect.top + 80;

    for (const block of blocks) {
      const rect = block.getBoundingClientRect();
      if (rect.top <= focalY && rect.bottom > focalY) {
        return block;
      }
    }
    if (container && container.scrollTop <= 20) return blocks[0];
    return blocks[blocks.length - 1];
  }
}

function updateVisibleMonthLabel() {
  const labelBtn = document.getElementById('btn-month-label');
  if (!labelBtn) return;

  const block = getVisibleMonthBlock();
  if (block) {
    const y = parseInt(block.dataset.year, 10);
    const m = parseInt(block.dataset.month, 10);
    if (!isNaN(y) && !isNaN(m)) {
      currentYear = y;
      currentMonth = m;
    }
    const titleEl = block.querySelector('.month-block-title');
    if (titleEl && titleEl.textContent) {
      labelBtn.textContent = titleEl.textContent;
    }
  }
}

function scrollToMonthBlock(el, smooth = true) {
  if (!el) return;
  const isMobile = window.innerWidth <= 768;
  const container = document.getElementById('months-scroll-container');

  // Immediately update label to prevent visual lag
  const titleEl = el.querySelector('.month-block-title');
  const labelBtn = document.getElementById('btn-month-label');
  if (titleEl && labelBtn) {
    labelBtn.textContent = titleEl.textContent;
  }

  if (isMobile) {
    const rect = el.getBoundingClientRect();
    const currentScroll = window.pageYOffset || document.documentElement.scrollTop;
    // Offset slightly below top header
    const targetY = currentScroll + rect.top - 12;

    window.scrollTo({
      top: Math.max(0, Math.round(targetY)),
      behavior: smooth ? 'smooth' : 'auto'
    });
  } else if (container && container.scrollHeight > container.clientHeight) {
    const cRect = container.getBoundingClientRect();
    const eRect = el.getBoundingClientRect();
    const targetScroll = container.scrollTop + (eRect.top - cRect.top) - 10;

    container.scrollTo({
      top: Math.max(0, Math.round(targetScroll)),
      behavior: smooth ? 'smooth' : 'auto'
    });
  } else {
    el.scrollIntoView({ behavior: smooth ? 'smooth' : 'auto', block: 'start' });
  }
}

async function loadInitialMonths() {
  const now = new Date();
  const curY = now.getFullYear();
  const curM = now.getMonth() + 1;
  currentYear = curY;
  currentMonth = curM;

  state.monthHistory = [];

  // Fetch available periods to find all recorded months
  let targetMonths = [];
  try {
    const res = await fetch('/api/stats/periods');
    if (res.ok) {
      const data = await res.json();
      if (data.months && data.months.length > 0) {
        targetMonths = [...data.months].sort((a, b) => {
          if (a.year !== b.year) return a.year - b.year;
          return a.month - b.month;
        });
      }
    }
  } catch (err) {}

  const hasCur = targetMonths.some(m => m.year === curY && m.month === curM);
  if (!hasCur) {
    targetMonths.push({ year: curY, month: curM });
  }

  // Preload all recorded months so user can immediately scroll natively
  const loadedMonths = await Promise.all(targetMonths.map(m => fetchMonthData(m.year, m.month)));
  state.monthHistory = loadedMonths.filter(Boolean);

  if (state.monthHistory.length > 0) {
    state.oldestLoaded = { year: state.monthHistory[0].year, month: state.monthHistory[0].month };
    state.newestLoaded = { year: state.monthHistory[state.monthHistory.length - 1].year, month: state.monthHistory[state.monthHistory.length - 1].month };
  } else {
    state.oldestLoaded = { year: curY, month: curM };
    state.newestLoaded = { year: curY, month: curM };
  }

  renderMonthBlocks();

  // Scroll to current month block initially
  requestAnimationFrame(() => {
    setTimeout(() => {
      jumpToCurrentMonth(false);
    }, 60);
  });
}

async function loadMonthData(year, month) {
  currentYear = year;
  currentMonth = month;

  const labelBtn = document.getElementById('btn-month-label');
  if (labelBtn) {
    labelBtn.textContent = `${MONTH_NAMES[month]} ${year}`;
  }

  let mData = state.monthHistory.find(m => m.year === year && m.month === month);
  if (!mData) {
    mData = await fetchMonthData(year, month);
    if (mData) {
      state.monthHistory.push(mData);
      state.monthHistory.sort((a, b) => {
        if (a.year !== b.year) return a.year - b.year;
        return a.month - b.month;
      });
      state.oldestLoaded = { year: state.monthHistory[0].year, month: state.monthHistory[0].month };
      state.newestLoaded = { year: state.monthHistory[state.monthHistory.length - 1].year, month: state.monthHistory[state.monthHistory.length - 1].month };
      renderMonthBlocks();
    }
  }

  const el = document.querySelector(`.month-block[data-year="${year}"][data-month="${month}"]`);
  if (el) {
    scrollToMonthBlock(el, true);
  }
}

async function jumpToCurrentMonth(smooth = true) {
  const now = new Date();
  currentYear = now.getFullYear();
  currentMonth = now.getMonth() + 1;
  await loadMonthData(currentYear, currentMonth);
}

function navigateToPrevMonth() {
  currentMonth--;
  if (currentMonth < 1) {
    currentMonth = 12;
    currentYear--;
  }
  loadMonthData(currentYear, currentMonth);
}

function navigateToNextMonth() {
  currentMonth++;
  if (currentMonth > 12) {
    currentMonth = 1;
    currentYear++;
  }
  loadMonthData(currentYear, currentMonth);
}

async function refreshCurrentMonth() {
  const now = new Date();
  const year = now.getFullYear();
  const month = now.getMonth() + 1;
  await refreshSpecificMonth(year, month);
}

async function refreshSpecificMonth(year, month) {
  const data = await fetchMonthData(year, month);
  if (data) {
    const idx = state.monthHistory.findIndex(m => m.year === year && m.month === month);
    if (idx !== -1) {
      state.monthHistory[idx] = data;
    } else {
      state.monthHistory.push(data);
      state.monthHistory.sort((a, b) => {
        if (a.year !== b.year) return a.year - b.year;
        return a.month - b.month;
      });
    }
    updateOrReplaceMonthBlock(data);
  }
}

async function fetchMonthData(year, month) {
  try {
    const res = await fetch(`/api/month?year=${year}&month=${month}`);
    if (res.ok) {
      return await res.json();
    }
  } catch (err) {}
  return null;
}

function updateOrReplaceMonthBlock(mData) {
  const existing = document.querySelector(`.month-block[data-year="${mData.year}"][data-month="${mData.month}"]`);
  const newEl = createMonthBlockElement(mData);
  if (existing) {
    existing.replaceWith(newEl);
  } else {
    renderMonthBlocks();
  }
}

function renderMonthBlocks() {
  const list = document.getElementById('months-list');
  list.innerHTML = '';

  // 1. Top button to load earlier months in history
  if (state.oldestLoaded) {
    let prevY = state.oldestLoaded.year;
    let prevM = state.oldestLoaded.month - 1;
    if (prevM < 1) {
      prevM = 12;
      prevY--;
    }
    const prevMonthName = `${MONTH_NAMES[prevM]} ${prevY}`;

    const prevWrapper = document.createElement('div');
    prevWrapper.className = 'history-nav-wrapper history-nav-top';
    const prevBtn = document.createElement('button');
    prevBtn.id = 'btn-load-prev-month';
    prevBtn.className = 'btn-history-stepper';
    prevBtn.innerHTML = `
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
        <polyline points="15 18 9 12 15 6"></polyline>
      </svg>
      <span>Load previous month (${escapeHtml(prevMonthName)})</span>
    `;
    prevBtn.addEventListener('click', (e) => {
      e.preventDefault();
      loadAndPrependPrevMonth();
    });
    prevWrapper.appendChild(prevBtn);
    list.appendChild(prevWrapper);
  }

  // 2. Render all loaded month blocks
  state.monthHistory.forEach(mData => {
    list.appendChild(createMonthBlockElement(mData));
  });

  // 3. Bottom button if older months are being inspected
  const now = new Date();
  const curY = now.getFullYear();
  const curM = now.getMonth() + 1;
  const hasCurrentMonth = state.monthHistory.some(m => m.year === curY && m.month === curM);
  if (!hasCurrentMonth || state.monthHistory.length > 2) {
    const nextWrapper = document.createElement('div');
    nextWrapper.className = 'history-nav-wrapper history-nav-bottom';
    const nextBtn = document.createElement('button');
    nextBtn.className = 'btn-history-stepper';
    nextBtn.innerHTML = `
      <span>Return to current month (${escapeHtml(MONTH_NAMES[curM])} ${curY})</span>
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
        <polygon points="5 3 19 12 5 21 5 3"></polygon>
      </svg>
    `;
    nextBtn.addEventListener('click', (e) => {
      e.preventDefault();
      jumpToCurrentMonth(true);
    });
    nextWrapper.appendChild(nextBtn);
    list.appendChild(nextWrapper);
  }
}

function createMonthBlockElement(mData) {
  const block = document.createElement('div');
  block.className = 'month-block';
  block.dataset.year = mData.year;
  block.dataset.month = mData.month;

  // Month Title Banner
  const totalStr = formatDuration(mData.total_seconds);
  const header = document.createElement('div');
  header.className = 'month-block-header';
  header.innerHTML = `
    <span class="month-block-title">${escapeHtml(mData.month_name)}</span>
    <span class="month-block-total">Tracked: ${totalStr}</span>
  `;
  block.appendChild(header);

  // Individual project legend for THIS month (clicking a project renames it for this month!)
  const legendRow = document.createElement('div');
  legendRow.className = 'month-projects-legend';
  if (mData.projects && mData.projects.length > 0) {
    mData.projects.forEach(p => {
      const item = document.createElement('div');
      item.className = 'month-legend-item';
      item.title = `Click to rename project "${p.name}" for ${mData.month_name} ${mData.year}`;
      item.innerHTML = `<span class="color-dot" style="background-color: ${p.color};"></span><span>${escapeHtml(p.name)}</span>`;
      item.addEventListener('click', (e) => {
        e.stopPropagation();
        openRenameProjectModal(mData.year, mData.month, p.name);
      });
      legendRow.appendChild(item);
    });
  } else {
    legendRow.innerHTML = '<span class="month-legend-empty">No projects tracked this month</span>';
  }
  block.appendChild(legendRow);

  // Timeline Matrix Table
  const tableWrapper = document.createElement('div');
  tableWrapper.className = 'timeline-table-wrapper';

  const table = document.createElement('table');
  table.className = 'timeline-table';

  let hoursHeaderHtml = '<div class="th-hours-container">';
  for (let h = 0; h < 24; h++) {
    hoursHeaderHtml += `<div class="th-hour">${h}</div>`;
  }
  hoursHeaderHtml += '</div>';

  table.innerHTML = `
    <thead>
      <tr class="timeline-header-row">
        <th class="th-date">
          <span class="date-full">Date</span>
          <span class="date-mobile-short">#</span>
        </th>
        <th class="th-track-header">${hoursHeaderHtml}</th>
        <th class="th-total">Total</th>
      </tr>
    </thead>
    <tbody></tbody>
  `;

  const tbody = table.querySelector('tbody');

  // Day Rows
  mData.days.forEach(day => {
    const row = document.createElement('tr');
    const isWeekend = (day.day_of_week === 0 || day.day_of_week === 6);
    row.className = `timeline-day-row ${isWeekend ? 'weekend' : ''}`;

    // Left Day Label (On desktop: W41 Mon 12. On mobile: only number 12!)
    const tdDate = document.createElement('td');
    tdDate.className = 'td-date';
    tdDate.innerHTML = `
      <span class="date-full"><span class="week-num">W${day.week_number}</span> ${day.day_name} ${day.day_number}</span>
      <span class="date-mobile-short">${day.day_number}</span>
    `;
    row.appendChild(tdDate);

    // 24-Hour Track
    const tdTrack = document.createElement('td');
    tdTrack.className = 'td-track';

    const trackWrap = document.createElement('div');
    trackWrap.className = 'track-timeline';

    // Grid hour guidelines
    for (let h = 1; h < 24; h++) {
      const line = document.createElement('div');
      line.className = 'hour-grid-line';
      line.style.left = `${(h / 24) * 100}%`;
      trackWrap.appendChild(line);
    }

    // Tracked session blocks
    if (day.sessions && day.sessions.length > 0) {
      day.sessions.forEach(sess => {
        const sDate = new Date(sess.start_time);
        const eDate = new Date(sess.end_time);

        const startHourFloat = sDate.getHours() + sDate.getMinutes() / 60 + sDate.getSeconds() / 3600;
        const durHoursFloat = sess.duration_seconds / 3600;

        const leftPct = (startHourFloat / 24) * 100;
        const widthPct = Math.max((durHoursFloat / 24) * 100, 0.4);

        const blockEl = document.createElement('div');
        blockEl.className = 'session-block';
        blockEl.style.left = `${leftPct}%`;
        blockEl.style.width = `${widthPct}%`;
        blockEl.style.backgroundColor = sess.project_color || 'var(--rp-pine)';
        blockEl.textContent = sess.project_name;
        blockEl.title = "Click to edit or delete";

        // Click to open edit modal
        blockEl.addEventListener('click', (e) => {
          e.stopPropagation();
          openEditModal(sess);
        });

        // Tooltip on hover
        const tooltipText = `<b>${escapeHtml(sess.project_name)}</b><br>${formatHM(sDate)} - ${formatHM(eDate)} (${formatDuration(sess.duration_seconds)})<br><i>Click to edit</i>`;
        attachTooltip(blockEl, tooltipText);

        trackWrap.appendChild(blockEl);
      });
    }

    tdTrack.appendChild(trackWrap);
    row.appendChild(tdTrack);

    // Right Day Total
    const tdTotal = document.createElement('td');
    tdTotal.className = `td-total ${day.total_seconds > 0 ? 'has-time' : ''}`;
    tdTotal.textContent = day.total_seconds > 0 ? formatHMShort(day.total_seconds) : '';
    row.appendChild(tdTotal);

    tbody.appendChild(row);
  });

  tableWrapper.appendChild(table);
  block.appendChild(tableWrapper);
  return block;
}

// Tooltip helper
function attachTooltip(el, html) {
  // Never attach hover tooltip on touch devices to avoid stuck ghost tooltips
  if (window.matchMedia && window.matchMedia('(hover: none) and (pointer: coarse)').matches) {
    return;
  }
  const tooltip = document.getElementById('session-tooltip');
  if (!tooltip) return;
  el.addEventListener('mouseenter', (e) => {
    tooltip.innerHTML = html;
    tooltip.style.display = 'block';
    moveTooltip(e);
  });
  el.addEventListener('mousemove', moveTooltip);
  el.addEventListener('mouseleave', () => {
    tooltip.style.display = 'none';
  });
}

function moveTooltip(e) {
  const tooltip = document.getElementById('session-tooltip');
  if (!tooltip) return;
  tooltip.style.left = `${e.clientX + 14}px`;
  tooltip.style.top = `${e.clientY + 14}px`;
}

// ==================== EDIT SESSION MODAL ====================
function initEditModal() {
  const modal = document.getElementById('edit-session-modal');
  const closeBtn = document.getElementById('modal-close-btn');
  const cancelBtn = document.getElementById('btn-modal-cancel');
  const deleteBtn = document.getElementById('btn-modal-delete');
  const deleteConfirmBox = document.getElementById('modal-delete-confirm-box');
  const deleteConfirmBtn = document.getElementById('btn-modal-delete-confirm');
  const deleteAbortBtn = document.getElementById('btn-modal-delete-abort');
  const form = document.getElementById('edit-session-form');

  const addBtn = document.getElementById('btn-add-activity');
  if (addBtn) {
    addBtn.addEventListener('click', () => {
      openAddModal();
    });
  }

  const fabAddBtn = document.getElementById('btn-fab-add-activity');
  if (fabAddBtn) {
    fabAddBtn.addEventListener('click', () => {
      openAddModal();
    });
  }

  const closeModal = () => {
    modal.style.display = 'none';
    state.activeEditSession = null;
    resetDeleteUI();
  };

  const resetDeleteUI = () => {
    const delGroup = modal.querySelector('.modal-delete-group');
    const actRight = modal.querySelector('.modal-actions-right');
    if (delGroup) delGroup.style.display = '';
    if (actRight) actRight.style.display = '';
    deleteConfirmBox.style.display = 'none';
  };

  closeBtn.addEventListener('click', closeModal);
  cancelBtn.addEventListener('click', closeModal);
  modal.addEventListener('click', (e) => {
    if (e.target === modal) closeModal();
  });

  // Step 1: Click Delete -> Show inline confirmation bar
  deleteBtn.addEventListener('click', () => {
    const delGroup = modal.querySelector('.modal-delete-group');
    const actRight = modal.querySelector('.modal-actions-right');
    if (delGroup) delGroup.style.display = 'none';
    if (actRight) actRight.style.display = 'none';
    deleteConfirmBox.style.display = 'flex';
  });

  // Abort delete
  deleteAbortBtn.addEventListener('click', () => {
    resetDeleteUI();
  });

  // Step 2: Confirm Delete -> Delete immediately without ugly confirm() dialog
  deleteConfirmBtn.addEventListener('click', async () => {
    if (!state.activeEditSession || state.activeEditSession.isNew) return;
    const sess = state.activeEditSession;
    const id = sess.id || sess.uuid;
    if (!id) return;

    closeModal();
    try {
      const res = await fetch(`/api/sessions/${id}`, {
        method: 'DELETE'
      });
      if (res.ok) {
        refreshCurrentMonth();
        loadTodaySessions();
        loadMonthProjects();
        loadAllProjects();
        if (state.activeTab === 'tab-stats') loadStats();
      } else {
        alert('Failed to delete session');
      }
    } catch (err) {
      console.error('Error deleting session:', err);
      alert('Failed to delete session');
    }
  });

  // Save changes (Create or Update)
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    if (!state.activeEditSession) return;

    const isNew = !!state.activeEditSession.isNew;
    const id = state.activeEditSession.id;
    const projName = (document.getElementById('edit-proj-name').value || '').trim() || 'Focus';

    let startTimeISO, endTimeISO;
    try {
      startTimeISO = new Date(document.getElementById('edit-start-time').value).toISOString();
      endTimeISO = new Date(document.getElementById('edit-end-time').value).toISOString();
    } catch (err) {
      alert("Please provide valid dates.");
      return;
    }

    try {
      let res;
      if (isNew) {
        res = await fetch('/api/sessions', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            project_name: projName,
            start_time: startTimeISO,
            end_time: endTimeISO,
            mode: 'pomodoro'
          })
        });
      } else {
        res = await fetch(`/api/sessions/${id}`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            project_name: projName,
            start_time: startTimeISO,
            end_time: endTimeISO
          })
        });
      }

      if (res.ok) {
        closeModal();
        loadTodaySessions();
        refreshCurrentMonth();
        loadMonthProjects();
        loadAllProjects();
        if (typeof loadStats === 'function') loadStats();
      } else {
        const errorMsg = await res.text();
        alert('Failed to save session: ' + errorMsg);
      }
    } catch (err) {
      console.error('Error saving session:', err);
      alert('Failed to save session');
    }
  });
}

function openAddModal() {
  state.activeEditSession = { isNew: true, uuid: generateUUID() };
  const tooltip = dom.sessionTooltip || document.getElementById('session-tooltip');
  if (tooltip) tooltip.style.display = 'none';

  const modal = dom.editSessionModal || document.getElementById('edit-session-modal');
  const title = document.getElementById('edit-modal-title');
  const saveBtn = document.getElementById('btn-modal-save');
  if (title) title.textContent = 'Add Activity';
  if (saveBtn) saveBtn.textContent = 'Add';

  document.getElementById('edit-session-id').value = '';

  // Default project: current project or last recorded project or Focus
  let defaultProj = (state.timer && state.timer.project) ? state.timer.project : state.defaultProject;
  if (!defaultProj && state.todaySessions && state.todaySessions.length > 0) {
    defaultProj = state.todaySessions[0].project_name;
  }
  document.getElementById('edit-proj-name').value = defaultProj || 'Focus';

  // Default time range: 1 hour ending at current time
  const now = new Date();
  now.setSeconds(0, 0);
  const oneHourAgo = new Date(now.getTime() - 60 * 60 * 1000);

  document.getElementById('edit-start-time').value = toDatetimeLocalValue(oneHourAgo);
  document.getElementById('edit-end-time').value = toDatetimeLocalValue(now);

  const delGroup = modal.querySelector('.modal-delete-group');
  const actRight = modal.querySelector('.modal-actions-right');
  if (delGroup) delGroup.style.display = 'none'; // No delete button when adding
  if (actRight) actRight.style.display = '';
  document.getElementById('modal-delete-confirm-box').style.display = 'none';

  modal.style.display = 'flex';
  setTimeout(() => {
    const input = document.getElementById('edit-proj-name');
    if (input) input.focus();
  }, 50);
}

function openEditModal(session) {
  if (!session.uuid) {
    session.uuid = generateUUID();
  }
  state.activeEditSession = session;
  const tooltip = dom.sessionTooltip || document.getElementById('session-tooltip');
  if (tooltip) tooltip.style.display = 'none';

  const modal = dom.editSessionModal || document.getElementById('edit-session-modal');
  const title = document.getElementById('edit-modal-title');
  const saveBtn = document.getElementById('btn-modal-save');
  if (title) title.textContent = 'Edit Activity';
  if (saveBtn) saveBtn.textContent = 'Save';

  document.getElementById('edit-session-id').value = session.id || '';
  document.getElementById('edit-proj-name').value = session.project_name;

  const st = new Date(session.start_time);
  const et = new Date(session.end_time);

  document.getElementById('edit-start-time').value = toDatetimeLocalValue(st);
  document.getElementById('edit-end-time').value = toDatetimeLocalValue(et);

  const delGroup = modal.querySelector('.modal-delete-group');
  const actRight = modal.querySelector('.modal-actions-right');
  if (delGroup) delGroup.style.display = '';
  if (actRight) actRight.style.display = '';
  document.getElementById('modal-delete-confirm-box').style.display = 'none';

  modal.style.display = 'flex';
}

function toDatetimeLocalValue(date) {
  const y = date.getFullYear();
  const m = String(date.getMonth() + 1).padStart(2, '0');
  const d = String(date.getDate()).padStart(2, '0');
  const h = String(date.getHours()).padStart(2, '0');
  const min = String(date.getMinutes()).padStart(2, '0');
  return `${y}-${m}-${d}T${h}:${min}`;
}

// ==================== PROJECT RENAME FOR MONTH MODAL ====================
function initRenameProjectModal() {
  const modal = document.getElementById('rename-project-modal');
  const form = document.getElementById('rename-project-form');
  const closeBtn = document.getElementById('rename-modal-close-btn');
  const cancelBtn = document.getElementById('rename-modal-cancel-btn');

  const closeModal = () => {
    modal.style.display = 'none';
  };

  closeBtn.addEventListener('click', closeModal);
  cancelBtn.addEventListener('click', closeModal);
  modal.addEventListener('click', (e) => {
    if (e.target === modal) closeModal();
  });

  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    const year = parseInt(document.getElementById('rename-target-year').value, 10);
    const month = parseInt(document.getElementById('rename-target-month').value, 10);
    const oldName = document.getElementById('rename-old-name').value;
    const newName = document.getElementById('rename-new-name-input').value.trim();

    if (!newName) return;
    if (newName === oldName) {
      closeModal();
      return;
    }

    try {
      const res = await fetch('/api/projects/rename-month', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ year, month, old_name: oldName, new_name: newName })
      });
      if (res.ok) {
        closeModal();
        await refreshSpecificMonth(year, month);
        await loadAllProjects();
        await loadMonthProjects();
        if (state.timer && state.timer.project && state.timer.project.toLowerCase() === oldName.toLowerCase()) {
          const pInput = document.getElementById('project-input');
          if (pInput) pInput.value = newName;
          const dot = document.getElementById('project-color-dot');
          if (dot) dot.style.backgroundColor = getProjectColor(newName);
        }
        if (state.activeTab === 'tab-stats') {
          loadStats();
        }
      } else {
        const txt = await res.text();
        alert('Could not rename project: ' + txt);
      }
    } catch (err) {
      console.error('Error renaming project', err);
    }
  });
}

function openRenameProjectModal(year, month, oldName) {
  const modal = document.getElementById('rename-project-modal');
  document.getElementById('rename-target-year').value = year;
  document.getElementById('rename-target-month').value = month;
  document.getElementById('rename-old-name').value = oldName;

  const input = document.getElementById('rename-new-name-input');
  input.value = oldName;

  modal.style.display = 'flex';
  setTimeout(() => {
    input.focus();
    input.select();
  }, 50);
}

// ==================== TAB 3 : STATS ====================
function initStats() {
  const pills = document.querySelectorAll('.stats-pill');
  const monthStepper = document.getElementById('stats-month-stepper');
  const yearStepper = document.getElementById('stats-year-stepper');

  pills.forEach(p => {
    p.addEventListener('click', () => {
      pills.forEach(b => b.classList.remove('active'));
      p.classList.add('active');
      state.statsPeriod = p.dataset.period;

      if (state.statsPeriod === 'month') {
        monthStepper.style.display = 'inline-flex';
        yearStepper.style.display = 'none';
      } else if (state.statsPeriod === 'year') {
        monthStepper.style.display = 'none';
        yearStepper.style.display = 'inline-flex';
      } else {
        monthStepper.style.display = 'none';
        yearStepper.style.display = 'none';
      }

      updateStatsPeriodVisibility();
      loadStats();
    });
  });

  // Stepper buttons for Month
  document.getElementById('btn-stats-prev-month').addEventListener('click', () => {
    state.statsMonth--;
    if (state.statsMonth < 1) {
      state.statsMonth = 12;
      state.statsYear--;
    }
    updateStatsLabels();
    loadStats();
  });

  // Middle click: Jump to current month!
  document.getElementById('stats-month-label').addEventListener('click', () => {
    const now = new Date();
    state.statsMonth = now.getMonth() + 1;
    state.statsYear = now.getFullYear();
    updateStatsLabels();
    loadStats();
  });

  document.getElementById('btn-stats-next-month').addEventListener('click', () => {
    state.statsMonth++;
    if (state.statsMonth > 12) {
      state.statsMonth = 1;
      state.statsYear++;
    }
    updateStatsLabels();
    loadStats();
  });

  // Stepper buttons for Year
  document.getElementById('btn-stats-prev-year').addEventListener('click', () => {
    state.statsYear--;
    updateStatsLabels();
    loadStats();
  });

  // Middle click: Jump to current year!
  document.getElementById('stats-year-label').addEventListener('click', () => {
    const now = new Date();
    state.statsYear = now.getFullYear();
    updateStatsLabels();
    loadStats();
  });

  document.getElementById('btn-stats-next-year').addEventListener('click', () => {
    state.statsYear++;
    updateStatsLabels();
    loadStats();
  });

  updateStatsLabels();
  updateStatsPeriodVisibility();
}

function updateStatsPeriodVisibility() {
  const cardAnnual = document.getElementById('card-annual-volume');
  const cardMonthly = document.getElementById('card-monthly-volume');

  if (state.statsPeriod === 'month') {
    if (cardMonthly) cardMonthly.style.display = 'block';
    if (cardAnnual) cardAnnual.style.display = 'none';
  } else {
    // 'year' or 'all'
    if (cardMonthly) cardMonthly.style.display = 'none';
    if (cardAnnual) cardAnnual.style.display = 'block';
  }
}

function updateStatsLabels() {
  const mLabel = document.getElementById('stats-month-label');
  if (mLabel) {
    mLabel.textContent = `${MONTH_NAMES[state.statsMonth]} ${state.statsYear}`;
  }
  const yLabel = document.getElementById('stats-year-label');
  if (yLabel) {
    yLabel.textContent = `${state.statsYear}`;
  }
}

async function loadStats() {
  let url = `/api/stats?period=${state.statsPeriod}`;
  if (state.statsPeriod === 'month') {
    url += `&year=${state.statsYear}&month=${state.statsMonth}`;
  } else if (state.statsPeriod === 'year') {
    url += `&year=${state.statsYear}`;
  }

  try {
    const res = await fetch(url);
    if (res.ok) {
      const stats = await res.json();
      renderStats(stats);
    }
  } catch (err) {}
}

function renderStats(stats) {
  // KPIs
  document.getElementById('kpi-total-time').textContent = formatDuration(stats.total_seconds);
  document.getElementById('kpi-session-count').textContent = `${stats.session_count} sessions recorded`;

  document.getElementById('kpi-avg-day').textContent = formatDuration(stats.average_per_day_seconds);
  document.getElementById('kpi-active-days').textContent = `${stats.active_days_count} active days`;

  document.getElementById('kpi-pomodoro-count').textContent = stats.pomodoro_count;
  document.getElementById('kpi-free-count').textContent = `${stats.free_count} in stopwatch mode`;

  const topProj = stats.projects && stats.projects.length > 0 ? stats.projects[0] : null;
  if (topProj) {
    document.getElementById('kpi-top-project').textContent = topProj.project_name;
    document.getElementById('kpi-top-project-time').textContent = `${formatDuration(topProj.total_seconds)} (${topProj.percentage.toFixed(1)}%)`;
  } else {
    document.getElementById('kpi-top-project').textContent = '—';
    document.getElementById('kpi-top-project-time').textContent = '0h (0%)';
  }

  // Projects Breakdown
  const projContainer = document.getElementById('projects-breakdown');
  projContainer.innerHTML = '';

  if (stats.projects && stats.projects.length > 0) {
    const stackedBar = document.createElement('div');
    stackedBar.className = 'stacked-bar-container';

    stats.projects.forEach(p => {
      const seg = document.createElement('div');
      seg.className = 'stacked-bar-segment';
      seg.style.width = `${p.percentage}%`;
      seg.style.backgroundColor = p.color;
      attachTooltip(seg, `<b>${escapeHtml(p.project_name)}</b>: ${p.percentage.toFixed(1)}% (${formatDuration(p.total_seconds)})`);
      stackedBar.appendChild(seg);
    });
    projContainer.appendChild(stackedBar);

    const table = document.createElement('table');
    table.className = 'proj-table';
    table.innerHTML = `
      <thead>
        <tr>
          <th>Project</th>
          <th>Total Time</th>
          <th>Share</th>
          <th class="col-sessions">Sessions</th>
          <th class="col-avg-dur">Avg Duration</th>
        </tr>
      </thead>
      <tbody>
        ${stats.projects.map(p => `
          <tr>
            <td>
              <span class="session-proj-badge">
                <span class="color-dot" style="background-color: ${p.color};"></span>
                <span class="proj-name-text">${escapeHtml(p.project_name)}</span>
              </span>
            </td>
            <td class="cell-time"><b>${formatDuration(p.total_seconds)}</b></td>
            <td class="cell-share">${p.percentage.toFixed(1)}%</td>
            <td class="col-sessions">${p.session_count}</td>
            <td class="col-avg-dur">${formatDuration(p.average_duration_seconds)}</td>
          </tr>
        `).join('')}
      </tbody>
    `;
    projContainer.appendChild(table);
  } else {
    projContainer.innerHTML = '<div class="empty-state">No sessions recorded for this period.</div>';
  }

  // Annual Bar Chart
  renderAnnualChart(stats.yearly_breakdown || []);

  // Monthly Bar Chart (12 months full volume)
  const chartTitle = document.getElementById('monthly-chart-title');
  if (chartTitle) {
    chartTitle.textContent = state.statsPeriod === 'year' ? `Work Volume (${state.statsYear})` : 'Monthly Work Volume';
  }
  renderMonthlyChart(stats.monthly_breakdown || []);

  // Weekday Distribution Chart
  renderWeekdayChart(stats.weekday_distribution || [0,0,0,0,0,0,0]);

  // Hourly Distribution Chart (24h)
  renderHourlyChart(stats.hourly_distribution || []);
}

function renderAnnualChart(years) {
  const container = document.getElementById('annual-chart-container');
  if (!container) return;
  container.innerHTML = '';

  if (!years || years.length === 0) {
    container.innerHTML = '<div class="empty-state">No annual data available.</div>';
    return;
  }

  const maxSec = Math.max(...years.map(y => y.total_seconds), 1);
  const count = Math.max(years.length, 1);
  const width = Math.min(Math.max(count * 90 + 40, 320), 600);
  const height = 140;
  const paddingBottom = 26;
  const barWidth = Math.min(Math.floor((width - 40) / count) - 16, 52);
  const gap = (width - count * barWidth) / (count + 1);

  let svgHtml = `<svg class="svg-chart" viewBox="0 0 ${width} ${height}">`;

  years.forEach((y, i) => {
    const barHeight = Math.max((y.total_seconds / maxSec) * (height - paddingBottom - 8), y.total_seconds > 0 ? 3 : 0);
    const x = gap + i * (barWidth + gap);
    const yPos = height - paddingBottom - barHeight;

    const isSelected = (y.year === state.statsYear);
    const barFill = isSelected ? 'var(--rp-gold)' : 'var(--rp-pine)';

    svgHtml += `
      <rect x="${x}" y="${yPos}" width="${barWidth}" height="${barHeight}" rx="4" fill="${barFill}" style="cursor: pointer;" data-year="${y.year}">
        <title>${y.label}: ${formatDuration(y.total_seconds)}</title>
      </rect>
      <text x="${x + barWidth/2}" y="${height - 8}" text-anchor="middle" font-size="12" fill="${isSelected ? 'var(--text)' : 'var(--text-dim)'}" font-family="var(--font-mono)" font-weight="${isSelected ? '700' : '400'}">${y.label}</text>
    `;
  });

  svgHtml += `</svg>`;
  container.innerHTML = svgHtml;

  // Clicking an annual bar selects that year
  container.querySelectorAll('rect').forEach(rect => {
    rect.addEventListener('click', () => {
      const yr = parseInt(rect.dataset.year, 10);
      if (yr) {
        state.statsYear = yr;
        updateStatsLabels();
        loadStats();
      }
    });
  });
}

function renderMonthlyChart(months) {
  const container = document.getElementById('monthly-chart-container');
  if (!container) return;
  container.innerHTML = '';

  if (months.length === 0) {
    container.innerHTML = '<div class="empty-state">No monthly data available.</div>';
    return;
  }

  const maxSec = Math.max(...months.map(m => m.total_seconds), 1);
  const width = 540;
  const height = 140;
  const paddingBottom = 24;
  const barWidth = 28;
  const gap = (width - months.length * barWidth) / (months.length + 1);

  let svgHtml = `<svg class="svg-chart" viewBox="0 0 ${width} ${height}">`;

  const curKey = `${state.statsYear}-${String(state.statsMonth).padStart(2, '0')}`;

  months.forEach((m, i) => {
    const barHeight = Math.max((m.total_seconds / maxSec) * (height - paddingBottom - 8), m.total_seconds > 0 ? 3 : 0);
    const x = gap + i * (barWidth + gap);
    const y = height - paddingBottom - barHeight;

    const mNum = parseInt(m.label.substring(5), 10);
    const shortName = MONTH_SHORT[mNum] || m.label.substring(5);
    const isSelected = (state.statsPeriod === 'month' && m.label === curKey);
    const barFill = isSelected ? 'var(--rp-gold)' : 'var(--rp-pine)';

    svgHtml += `
      <rect x="${x}" y="${y}" width="${barWidth}" height="${barHeight}" rx="3" fill="${barFill}">
        <title>${m.label}: ${formatDuration(m.total_seconds)}</title>
      </rect>
      <text x="${x + barWidth/2}" y="${height - 8}" text-anchor="middle" font-size="11" fill="${isSelected ? 'var(--text)' : 'var(--text-dim)'}" font-family="var(--font-mono)" font-weight="${isSelected ? '700' : '400'}">${shortName}</text>
    `;
  });

  svgHtml += `</svg>`;
  container.innerHTML = svgHtml;
}

function renderWeekdayChart(dist) {
  const container = document.getElementById('weekday-chart-container');
  if (!container) return;
  const days = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];
  const maxSec = Math.max(...dist, 1);

  const width = 360;
  const height = 135;
  const paddingBottom = 22;
  const barWidth = 30;
  const gap = (width - 7 * barWidth) / 8;

  let svgHtml = `<svg class="svg-chart" viewBox="0 0 ${width} ${height}">`;

  dist.forEach((sec, i) => {
    const barHeight = Math.max((sec / maxSec) * (height - paddingBottom - 8), sec > 0 ? 3 : 0);
    const x = gap + i * (barWidth + gap);
    const y = height - paddingBottom - barHeight;

    svgHtml += `
      <rect x="${x}" y="${y}" width="${barWidth}" height="${barHeight}" rx="3" fill="var(--rp-foam)">
        <title>${days[i]}: ${formatDuration(sec)}</title>
      </rect>
      <text x="${x + barWidth/2}" y="${height - 6}" text-anchor="middle" font-size="12" fill="var(--text-dim)" font-family="var(--font-mono)">${days[i]}</text>
    `;
  });

  svgHtml += `</svg>`;
  container.innerHTML = svgHtml;
}

function renderHourlyChart(dist) {
  const container = document.getElementById('hourly-chart-container');
  if (!container) return;
  const maxSec = Math.max(...dist, 1);

  const width = 480;
  const height = 135;
  const paddingBottom = 22;
  const barWidth = 14;
  const gap = (width - 24 * barWidth) / 25;

  let svgHtml = `<svg class="svg-chart" viewBox="0 0 ${width} ${height}">`;

  dist.forEach((sec, h) => {
    const barHeight = Math.max((sec / maxSec) * (height - paddingBottom - 8), sec > 0 ? 2 : 0);
    const x = gap + h * (barWidth + gap);
    const y = height - paddingBottom - barHeight;

    const showLabel = (h % 3 === 0);

    svgHtml += `
      <rect x="${x}" y="${y}" width="${barWidth}" height="${barHeight}" rx="2" fill="var(--rp-iris)">
        <title>${h}:00: ${formatDuration(sec)}</title>
      </rect>
      ${showLabel ? `<text x="${x + barWidth/2}" y="${height - 6}" text-anchor="middle" font-size="11" fill="var(--text-dim)" font-family="var(--font-mono)">${h}h</text>` : ''}
    `;
  });

  svgHtml += `</svg>`;
  container.innerHTML = svgHtml;
}

// ==================== KEYBOARD SHORTCUTS ====================
function initKeyboardShortcuts() {
  window.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') {
      const editModal = document.getElementById('edit-session-modal');
      const renameModal = document.getElementById('rename-project-modal');
      const changePwdModal = document.getElementById('change-password-modal');
      const cancelModal = document.getElementById('cancel-confirm-modal');
      if (editModal && editModal.style.display !== 'none') {
        editModal.style.display = 'none';
        return;
      }
      if (renameModal && renameModal.style.display !== 'none') {
        renameModal.style.display = 'none';
        return;
      }
      if (changePwdModal && changePwdModal.style.display !== 'none') {
        changePwdModal.style.display = 'none';
        return;
      }
      if (cancelModal && cancelModal.style.display !== 'none') {
        cancelModal.style.display = 'none';
        return;
      }
      // Escape never cancels the timer!
      return;
    }

    if (['INPUT', 'TEXTAREA', 'SELECT'].includes(e.target.tagName)) return;
    if (document.getElementById('edit-session-modal') && document.getElementById('edit-session-modal').style.display !== 'none') return;
    if (document.getElementById('rename-project-modal') && document.getElementById('rename-project-modal').style.display !== 'none') return;
    if (document.getElementById('change-password-modal') && document.getElementById('change-password-modal').style.display !== 'none') return;
    if (document.getElementById('cancel-confirm-modal') && document.getElementById('cancel-confirm-modal').style.display !== 'none') return;

    if (e.code === 'Space') {
      e.preventDefault();
      onTimerToggleGesture();
      sendAction('toggle');
    } else if (e.key === 'n' || e.key === 'N') {
      if (state.mode === 'pomodoro') {
        sendAction('next');
        setTimeout(() => {
          loadTodaySessions();
          refreshCurrentMonth();
        }, 250);
      }
    } else if (e.key === 's' || e.key === 'S') {
      if (state.mode === 'free') {
        sendAction('stop');
        setTimeout(() => {
          loadTodaySessions();
          refreshCurrentMonth();
          loadDefaultProject();
        }, 250);
      }
    } else if (e.key === '1') {
      switchTab('tab-timer');
    } else if (e.key === '2') {
      switchTab('tab-month');
    } else if (e.key === '3') {
      switchTab('tab-stats');
    }
  });
}

// ==================== FORMATTING UTILITIES ====================
function formatDuration(totalSeconds) {
  if (!totalSeconds || totalSeconds < 0) return '0h 00m';
  const hrs = Math.floor(totalSeconds / 3600);
  const mins = Math.floor((totalSeconds % 3600) / 60);
  return `${hrs}h ${String(mins).padStart(2, '0')}m`;
}

function formatHMShort(totalSeconds) {
  const hrs = Math.floor(totalSeconds / 3600);
  const mins = Math.floor((totalSeconds % 3600) / 60);
  return `${hrs}:${String(mins).padStart(2, '0')}`;
}

function formatHM(date) {
  const h = String(date.getHours()).padStart(2, '0');
  const m = String(date.getMinutes()).padStart(2, '0');
  return `${h}:${m}`;
}

function escapeHtml(str) {
  if (!str) return '';
  return str.replace(/[&<>"']/g, (m) => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    '"': '&quot;',
    "'": '&#39;'
  }[m]));
}

// ==================== AUTHENTICATION & SECURITY ====================
let authMode = 'login'; // 'login' or 'setup'

function initAuth() {
  const authForm = document.getElementById('auth-form');
  if (authForm) {
    authForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      await handleAuthSubmit();
    });
  }

  checkAuthStatus();
}

async function checkAuthStatus() {
  const overlay = document.getElementById('auth-overlay');
  const subtitle = document.getElementById('auth-subtitle');
  const confirmGroup = document.getElementById('auth-confirm-group');
  const submitBtn = document.getElementById('auth-submit-btn');
  const errorBox = document.getElementById('auth-error-msg');

  if (errorBox) errorBox.style.display = 'none';

  try {
    const res = await fetch('/api/auth/status');
    if (res.ok) {
      const data = await res.json();

      if (!data.has_password) {
        // First-time setup: user chooses their password
        authMode = 'setup';
        if (subtitle) subtitle.textContent = 'Welcome! Set a password to protect your personal session.';
        if (confirmGroup) confirmGroup.style.display = 'block';
        if (submitBtn) submitBtn.textContent = 'Set Password';
        if (overlay) overlay.style.display = 'flex';
        document.body.classList.add('auth-locked');
        return;
      } else if (!data.authenticated) {
        // Password is set, but session is locked
        authMode = 'login';
        if (subtitle) subtitle.textContent = 'Enter your password to unlock your session.';
        if (confirmGroup) confirmGroup.style.display = 'none';
        if (submitBtn) submitBtn.textContent = 'Unlock';
        if (overlay) overlay.style.display = 'flex';
        document.body.classList.add('auth-locked');
        return;
      }
    }
  } catch (err) {
    console.error('Auth status check error', err);
  }

  // If already authenticated:
  unlockApp();
}

async function handleAuthSubmit() {
  const pwdInput = document.getElementById('auth-input-password');
  const confirmInput = document.getElementById('auth-input-confirm');
  const errorBox = document.getElementById('auth-error-msg');
  const submitBtn = document.getElementById('auth-submit-btn');

  if (errorBox) errorBox.style.display = 'none';
  const pwd = pwdInput ? pwdInput.value.trim() : '';
  if (!pwd) {
    if (errorBox) {
      errorBox.textContent = 'Please enter a password.';
      errorBox.style.display = 'block';
    }
    return;
  }

  if (submitBtn) submitBtn.disabled = true;

  try {
    if (authMode === 'setup') {
      const confirmPwd = confirmInput ? confirmInput.value.trim() : '';
      if (pwd !== confirmPwd) {
        if (errorBox) {
          errorBox.textContent = 'Passwords do not match.';
          errorBox.style.display = 'block';
        }
        if (submitBtn) submitBtn.disabled = false;
        return;
      }

      const res = await fetch('/api/auth/setup', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: pwd })
      });

      if (res.ok) {
        unlockApp();
      } else {
        const msg = await res.text();
        if (errorBox) {
          errorBox.textContent = msg || 'Error setting password.';
          errorBox.style.display = 'block';
        }
      }
    } else {
      // Login mode
      const res = await fetch('/api/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: pwd })
      });

      if (res.ok) {
        unlockApp();
      } else {
        if (errorBox) {
          errorBox.textContent = 'Incorrect password.';
          errorBox.style.display = 'block';
        }
      }
    }
  } catch (err) {
    if (errorBox) {
      errorBox.textContent = 'Connection error.';
      errorBox.style.display = 'block';
    }
  } finally {
    if (submitBtn) submitBtn.disabled = false;
  }
}

function unlockApp() {
  const overlay = document.getElementById('auth-overlay');
  if (overlay) overlay.style.display = 'none';
  document.body.classList.remove('auth-locked');

  // Load app data & connect SSE
  initSSE();
  loadDefaultProject();
  loadTodaySessions();
  loadAllProjects();
}

function initSecurityModal() {
  const btnSecurity = document.getElementById('btn-security-modal');
  const modal = document.getElementById('change-password-modal');
  const closeBtn = document.getElementById('change-pwd-close-btn');
  const cancelBtn = document.getElementById('change-pwd-cancel-btn');
  const form = document.getElementById('change-password-form');
  const errorBox = document.getElementById('change-pwd-error-msg');
  const logoutBtn = document.getElementById('btn-auth-logout');

  if (btnSecurity && modal) {
    btnSecurity.addEventListener('click', () => {
      if (errorBox) errorBox.style.display = 'none';
      if (form) form.reset();
      modal.style.display = 'flex';
    });
  }

  const hideModal = () => {
    if (modal) modal.style.display = 'none';
  };

  if (closeBtn) closeBtn.addEventListener('click', hideModal);
  if (cancelBtn) cancelBtn.addEventListener('click', hideModal);
  if (modal) {
    modal.addEventListener('click', (e) => {
      if (e.target === modal) hideModal();
    });
  }

  if (form) {
    form.addEventListener('submit', async (e) => {
      e.preventDefault();
      if (errorBox) errorBox.style.display = 'none';

      const curPwd = document.getElementById('pwd-current').value;
      const newPwd = document.getElementById('pwd-new').value;
      const confirmPwd = document.getElementById('pwd-new-confirm').value;

      if (newPwd !== confirmPwd) {
        if (errorBox) {
          errorBox.textContent = 'New passwords do not match.';
          errorBox.style.display = 'block';
        }
        return;
      }

      try {
        const res = await fetch('/api/auth/change-password', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ old_password: curPwd, new_password: newPwd })
        });
        if (res.ok) {
          hideModal();
          alert('Password successfully updated!');
        } else {
          const txt = await res.text();
          if (errorBox) {
            errorBox.textContent = txt || 'Current password is incorrect.';
            errorBox.style.display = 'block';
          }
        }
      } catch (err) {
        if (errorBox) {
          errorBox.textContent = 'Connection error.';
          errorBox.style.display = 'block';
        }
      }
    });
  }

  if (logoutBtn) {
    logoutBtn.addEventListener('click', async () => {
      hideModal();
      await fetch('/api/auth/logout', { method: 'POST' });
      window.location.reload();
    });
  }

  setupExportImport();
}

function setupExportImport() {
  const downloadExport = (format) => {
    fetch(`/api/export?format=${format}`)
      .then(res => {
        if (!res.ok) throw new Error('Export failed');
        return res.blob();
      })
      .then(blob => {
        const url = window.URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        const now = new Date().toISOString().slice(0, 10);
        let ext = format;
        if (format === 'timewarrior' || format === 'timew') ext = 'json';
        a.download = `focus-export-${format}-${now}.${ext}`;
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        window.URL.revokeObjectURL(url);
      })
      .catch(err => {
        alert('Export error: ' + err.message);
      });
  };

  // Three dots more menu toggle
  const btnMoreMenu = document.getElementById('btn-more-menu');
  const moreDropdown = document.getElementById('more-dropdown-menu');

  if (btnMoreMenu && moreDropdown) {
    btnMoreMenu.addEventListener('click', (e) => {
      e.stopPropagation();
      const isVisible = moreDropdown.style.display !== 'none';
      moreDropdown.style.display = isVisible ? 'none' : 'flex';
    });

    document.addEventListener('click', (e) => {
      if (moreDropdown.style.display !== 'none' && !moreDropdown.contains(e.target) && e.target !== btnMoreMenu) {
        moreDropdown.style.display = 'none';
      }
    });
  }

  // Dropdown items
  const menuExportJson = document.getElementById('menu-export-json');
  if (menuExportJson) {
    menuExportJson.addEventListener('click', () => {
      if (moreDropdown) moreDropdown.style.display = 'none';
      downloadExport('json');
    });
  }

  const menuExportCsv = document.getElementById('menu-export-csv');
  if (menuExportCsv) {
    menuExportCsv.addEventListener('click', () => {
      if (moreDropdown) moreDropdown.style.display = 'none';
      downloadExport('csv');
    });
  }

  const globalImportInput = document.getElementById('global-file-import-input');
  const menuImportFile = document.getElementById('menu-import-file');
  if (menuImportFile && globalImportInput) {
    menuImportFile.addEventListener('click', () => {
      if (moreDropdown) moreDropdown.style.display = 'none';
      globalImportInput.click();
    });
  }

  const menuOpenSec = document.getElementById('menu-open-security');
  const pwdModal = document.getElementById('change-password-modal');
  if (menuOpenSec && pwdModal) {
    menuOpenSec.addEventListener('click', () => {
      if (moreDropdown) moreDropdown.style.display = 'none';
      pwdModal.style.display = 'flex';
    });
  }

  const btnExportCsv = document.getElementById('btn-export-csv');
  if (btnExportCsv) {
    btnExportCsv.addEventListener('click', () => downloadExport('csv'));
  }

  const btnExportJson = document.getElementById('btn-export-json');
  if (btnExportJson) {
    btnExportJson.addEventListener('click', () => downloadExport('json'));
  }

  // Guard any legacy month CSV export button reference with optional chaining
  document.getElementById('btn-month-export-csv')?.addEventListener('click', () => downloadExport('csv'));

  const processImportFile = async (file, statusEl) => {
    if (!file) return;

    if (statusEl) {
      statusEl.style.display = 'block';
      statusEl.className = 'import-status-msg info';
      statusEl.textContent = 'Importing...';
    }

    const formData = new FormData();
    formData.append('file', file);

    try {
      const res = await fetch('/api/import', {
        method: 'POST',
        body: formData
      });

      if (!res.ok) {
        const errTxt = await res.text();
        throw new Error(errTxt || 'Import failed');
      }

      const data = await res.json();
      const msg = `✓ Import successful: ${data.imported} sessions imported, ${data.skipped} duplicates skipped (total processed: ${data.total}).`;
      if (statusEl) {
        statusEl.className = 'import-status-msg success';
        statusEl.textContent = msg;
      } else {
        alert(msg);
      }

      // Refresh views
      loadTodaySessions();
      await refreshCurrentMonth();
      loadMonthProjects();
      loadAllProjects();
      if (state.activeTab === 'tab-stats') loadStats();
    } catch (err) {
      const errMsg = 'Import error: ' + err.message;
      if (statusEl) {
        statusEl.className = 'import-status-msg error';
        statusEl.textContent = errMsg;
      } else {
        alert(errMsg);
      }
    }
  };

  const fileInput = document.getElementById('file-import-input');
  const statusEl = document.getElementById('import-status-msg');

  if (fileInput) {
    fileInput.addEventListener('change', async (e) => {
      const file = e.target.files && e.target.files[0];
      await processImportFile(file, statusEl);
      fileInput.value = '';
    });
  }

  if (globalImportInput) {
    globalImportInput.addEventListener('change', async (e) => {
      const file = e.target.files && e.target.files[0];
      await processImportFile(file, null);
      globalImportInput.value = '';
    });
  }
}

// ==================== MOBILE / TOUCH SHORTCUT CLEANUP ====================
function cleanMobileKeyboardHints() {
  const isTouch = window.matchMedia('(hover: none) and (pointer: coarse)').matches || window.innerWidth <= 768;
  if (isTouch) {
    // Strip keyboard shortcut title tooltips on mobile so tap/hold does not display keyboard callouts
    ['btn-toggle', 'btn-next', 'btn-stop', 'btn-cancel'].forEach(id => {
      const el = document.getElementById(id);
      if (el) el.removeAttribute('title');
    });
  }
}
window.addEventListener('resize', cleanMobileKeyboardHints);

// ==================== AUDIO ALERTS (Exact PC alarm.mp3 & Zero-Widget Lock Screen Audio) ====================
// Pure Web Audio without HTML5 media looping:
// 1. Zero iOS Lock Screen Media Widget / Now Playing controls (no mediaSession, no HTML5 background loop).
// 2. Plays authentic deepwork-alarm.mp3 asset directly from memory.
// 3. Sample-accurate Web Audio clock scheduling for locked/background alarms.

const ALARM_AUDIO_BASE64 = "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MDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpMQU1FMy4x//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEMDCqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq//t0ZIGP8AAAaQAAAAgAAA0gAAABAAABpAAAACAAADSAAAAEqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqq";

let globalAudioCtx = null;
let alarmAudioBuffer = null;
let alarmBufferLoading = false;
let lastAlarmTriggerTime = 0;
let scheduledAlarmSource = null;
let webAudioSilenceSource = null;

function base64ToArrayBuffer(base64) {
  const binaryString = window.atob(base64);
  const len = binaryString.length;
  const bytes = new Uint8Array(len);
  for (let i = 0; i < len; i++) {
    bytes[i] = binaryString.charCodeAt(i);
  }
  return bytes.buffer;
}

function getUnlockedAudioContext() {
  if (!globalAudioCtx) {
    const AudioCtx = window.AudioContext || window.webkitAudioContext;
    if (AudioCtx) {
      globalAudioCtx = new AudioCtx();
    }
  }
  if (globalAudioCtx && globalAudioCtx.state === 'suspended') {
    globalAudioCtx.resume().catch(() => {});
  }
  return globalAudioCtx;
}

function getAlarmPlayer() {
  let player = document.getElementById('alarm-audio');
  if (!player) {
    player = new Audio();
    player.id = 'alarm-audio';
    player.setAttribute('playsinline', 'true');
    player.setAttribute('webkit-playsinline', 'true');
    player.preload = 'auto';
    player.src = 'alarm.mp3';
    document.body.appendChild(player);
  }
  return player;
}

function preloadAndDecodeAlarmBuffer() {
  if (alarmAudioBuffer || alarmBufferLoading) return;
  const ctx = getUnlockedAudioContext();
  if (!ctx) return;
  alarmBufferLoading = true;

  const tryDecode = (arrayBuffer) => {
    ctx.decodeAudioData(
      arrayBuffer,
      (buf) => {
        alarmAudioBuffer = buf;
        alarmBufferLoading = false;
        console.log('[Audio] alarm.mp3 decoded into Web Audio buffer');
      },
      (err) => {
        console.warn('[Audio] decodeAudioData error, retrying base64:', err);
        try {
          const fallbackBuf = base64ToArrayBuffer(ALARM_AUDIO_BASE64);
          ctx.decodeAudioData(fallbackBuf, (buf2) => {
            alarmAudioBuffer = buf2;
            alarmBufferLoading = false;
            console.log('[Audio] alarm.mp3 base64 fallback decoded');
          }, () => { alarmBufferLoading = false; });
        } catch (e) {
          alarmBufferLoading = false;
        }
      }
    );
  };

  fetch('alarm.mp3')
    .then(res => {
      if (!res.ok) throw new Error('Status ' + res.status);
      return res.arrayBuffer();
    })
    .then(ab => tryDecode(ab))
    .catch(err => {
      console.warn('[Audio] Network fetch failed, decoding base64:', err);
      try {
        const ab = base64ToArrayBuffer(ALARM_AUDIO_BASE64);
        tryDecode(ab);
      } catch (e) {
        alarmBufferLoading = false;
      }
    });
}

function warmupMobileAudio() {
  getUnlockedAudioContext();
  preloadAndDecodeAlarmBuffer();

  const alarmEl = getAlarmPlayer();
  if (alarmEl) {
    alarmEl.load();
  }
}

// Detect iOS Safari / WebKit (the only platform needing a continuous background audio stream for lock screen)
const isIOS = typeof navigator !== 'undefined' && (
  /iPad|iPhone|iPod/.test(navigator.userAgent) ||
  (navigator.platform === 'MacIntel' && navigator.maxTouchPoints > 1)
);

// Pre-unlock audio on user interaction
['touchstart', 'touchend', 'click', 'keydown'].forEach(evt => {
  document.addEventListener(evt, () => {
    warmupMobileAudio();
  }, { passive: true });
});

// Synchronous gesture triggers (called directly in click handlers)
function onTimerToggleGesture() {
  warmupMobileAudio();
  if (isIOS) {
    const willRun = !state.timer || state.timer.status === 'idle' || state.timer.status === 'paused';
    if (willRun) {
      playSessionAudioStream();
    } else {
      stopSessionAudioStream();
    }
  }
}

function getSessionPlayer() {
  return document.getElementById('session-audio');
}

let activeStreamRemaining = null;

function playSessionAudioStream() {
  // NEVER stream dummy session audio on desktop/PC or non-iOS to avoid hijacking MPRIS / media players
  if (!isIOS) return;

  const player = getSessionPlayer();
  if (!player) return;

  if ('audioSession' in navigator) {
    try {
      navigator.audioSession.type = 'playback';
    } catch (e) {}
  }

  const snap = state.timer;
  const isPomodoro = !snap || snap.mode === 'pomodoro';
  const remaining = (snap && snap.remaining_seconds > 0) ? snap.remaining_seconds : 1500;

  if (isPomodoro) {
    if (activeStreamRemaining === null || Math.abs(activeStreamRemaining - remaining) > 3 || player.paused) {
      activeStreamRemaining = remaining;
      player.src = `/api/audio/session.mp3?remaining=${remaining}&_t=${Date.now()}`;
      player.loop = false;
    }
  } else {
    activeStreamRemaining = 0;
    if (!player.src.endsWith('silent.mp3') || player.paused) {
      player.src = 'silent.mp3';
      player.loop = true;
    }
  }

  const p = player.play();
  if (p !== undefined) {
    p.catch(err => console.warn('[Audio] Session player play error:', err));
  }
}

function stopSessionAudioStream() {
  const player = getSessionPlayer();
  if (player) {
    player.pause();
    activeStreamRemaining = null;
  }
}

function isSessionAudioPlayingAlarm() {
  const p = getSessionPlayer();
  if (!p || p.paused) return false;
  const now = Date.now();
  if (state.lastHandledAlertId && (now - state.lastHandledAlertId < 4000)) {
    return true;
  }
  if (p.duration && (p.duration - p.currentTime) <= 4.0) {
    return true;
  }
  return false;
}

// Attach listeners to session-audio to catch end of stream
const sessionAudioEl = getSessionPlayer();
if (sessionAudioEl) {
  sessionAudioEl.addEventListener('timeupdate', () => {
    // When the stream enters its final 4 seconds (the embedded alarm.mp3),
    // mark lastAlarmTriggerTime so that SSE alert_id won't trigger a duplicate alarm sound
    if (sessionAudioEl.duration && (sessionAudioEl.duration - sessionAudioEl.currentTime) <= 4.0) {
      lastAlarmTriggerTime = Date.now();
    }
  });
  sessionAudioEl.addEventListener('ended', () => {
    activeStreamRemaining = null;
    lastAlarmTriggerTime = Date.now();
  });
}

function playSessionEndAlert() {
  const snap = state.timer;
  if (snap && snap.origin_client_id && snap.origin_client_id !== getClientId()) {
    console.log('[Audio] Skipping playSessionEndAlert: not origin client (' + snap.origin_client_id + ' vs ' + getClientId() + ')');
    return;
  }
  const now = Date.now();

  // If the stream is already playing the embedded alarm, let it complete naturally without cut-off
  if (isSessionAudioPlayingAlarm()) {
    console.log('[Audio] Stream is already playing the embedded alarm, letting it complete naturally.');
    lastAlarmTriggerTime = now;
    return;
  }

  // If the alarm was recently triggered (within 4 seconds), do not ring again
  if (now - lastAlarmTriggerTime < 4000) {
    return;
  }
  triggerAlarmSound();
}

function playWebAudioAlarmBuffer() {
  try {
    const ctx = getUnlockedAudioContext();
    if (!ctx) return false;
    if (ctx.state === 'suspended') ctx.resume().catch(() => {});
    if (!alarmAudioBuffer) {
      preloadAndDecodeAlarmBuffer();
      return false;
    }
    const source = ctx.createBufferSource();
    source.buffer = alarmAudioBuffer;
    const gain = ctx.createGain();
    gain.gain.setValueAtTime(1.0, ctx.currentTime);
    source.connect(gain);
    gain.connect(ctx.destination);
    source.start(0);
    return true;
  } catch (err) {
    console.warn('[Audio] playWebAudioAlarmBuffer error:', err);
    return false;
  }
}

function triggerAlarmSound(force = false) {
  const now = Date.now();
  if (!force && (now - lastAlarmTriggerTime < 3000)) {
    return; // Prevent duplicate triggers within 3 seconds
  }
  lastAlarmTriggerTime = now;

  console.log('[Audio] Triggering alarm sound (deepwork-alarm.mp3)...');

  stopSessionAudioStream();

  // Play exactly ONE audio output (Web Audio preferred, HTML5 audio fallback)
  const played = playWebAudioAlarmBuffer();
  if (!played) {
    const alarmEl = getAlarmPlayer();
    if (alarmEl) {
      alarmEl.currentTime = 0;
      alarmEl.volume = 1.0;
      const playPromise = alarmEl.play();
      if (playPromise !== undefined) {
        playPromise.catch((err) => console.warn('[Audio] alarm-audio play error:', err));
      }
    }
  }

  // Visual notification on sound test button if present
  const btnSoundTest = document.getElementById('btn-sound-test');
  if (btnSoundTest) {
    btnSoundTest.style.color = 'var(--rp-love)';
    setTimeout(() => {
      btnSoundTest.style.color = '';
    }, 1500);
  }

  // Trigger mobile vibration if supported
  if (navigator.vibrate) {
    try {
      navigator.vibrate([300, 150, 300, 150, 500]);
    } catch (e) {}
  }

  // Trigger web/desktop notification if permitted
  if ('Notification' in window && Notification.permission === 'granted') {
    try {
      const snap = state.timer;
      const notifTitle = (snap && snap.phase === 'work') ? 'Deepwork session over' : 'Break over';
      const notifBody = (snap && snap.phase === 'work')
        ? 'Time for a break.'
        : 'Prepare a new session for deepwork.';
      new Notification(notifTitle, {
        body: notifBody,
        icon: '/favicon.ico'
      });
    } catch (e) {}
  }
}

// Media Session (iOS Lock Screen Widget & Dynamic Island) & Wake Lock sync
function updateMediaSession(snap) {
  const isOrigin = !snap.origin_client_id || snap.origin_client_id === getClientId();

  // On desktop / non-iOS platforms: NEVER hijack MediaSession / MPRIS / Now Playing
  if (!isIOS) {
    if ('mediaSession' in navigator) {
      try {
        navigator.mediaSession.metadata = null;
        navigator.mediaSession.playbackState = 'none';
      } catch (e) {}
    }
    stopSessionAudioStream();
    if (snap.status === 'running' && isOrigin) {
      requestScreenWakeLock();
    } else {
      releaseScreenWakeLock();
    }
    return;
  }

  const player = getSessionPlayer();

  if (snap.status === 'running') {
    if (isOrigin) {
      if (player && player.paused) {
        playSessionAudioStream();
      }
    } else {
      if (player && !player.paused) {
        stopSessionAudioStream();
      }
    }

    if ('mediaSession' in navigator) {
      const phaseLabel = snap.phase === 'work' ? 'Work' : 'Break';
      const modeLabel = snap.mode === 'pomodoro' ? 'Pomodoro' : 'Stopwatch';
      navigator.mediaSession.metadata = new MediaMetadata({
        title: `${snap.project || 'Focus'} • ${snap.time_str || '00:00'}`,
        artist: `${phaseLabel} (${modeLabel})`,
        album: 'Time Tracker'
      });

      navigator.mediaSession.playbackState = isOrigin ? 'playing' : 'none';

      if (snap.mode === 'pomodoro' && snap.total_seconds > 0 && 'setPositionState' in navigator.mediaSession) {
        try {
          navigator.mediaSession.setPositionState({
            duration: snap.total_seconds,
            playbackRate: 1,
            position: Math.min(snap.elapsed_seconds, snap.total_seconds)
          });
        } catch (e) {}
      }
    }
    if (isOrigin) {
      requestScreenWakeLock();
    } else {
      releaseScreenWakeLock();
    }
  } else if (snap.status === 'paused') {
    // If the stream is currently playing the alarm, DO NOT pause it mid-sound!
    const isPlayingAlarm = isSessionAudioPlayingAlarm();
    if (player && !player.paused && !isPlayingAlarm) {
      player.pause();
    }
    if ('mediaSession' in navigator) {
      navigator.mediaSession.playbackState = 'paused';
      if (navigator.mediaSession.metadata) {
        navigator.mediaSession.metadata.title = `[Pause] ${snap.project || 'Focus'} (${snap.time_str || '00:00'})`;
      }
    }
    releaseScreenWakeLock();
  } else {
    // idle / stopped
    stopSessionAudioStream();
    if ('mediaSession' in navigator) {
      navigator.mediaSession.playbackState = 'none';
    }
    releaseScreenWakeLock();
  }
}

// Lock screen action handlers (iOS only)
if (isIOS && 'mediaSession' in navigator) {
  try {
    navigator.mediaSession.setActionHandler('play', () => {
      sendAction('toggle');
    });
    navigator.mediaSession.setActionHandler('pause', () => {
      sendAction('toggle');
    });
    navigator.mediaSession.setActionHandler('nexttrack', () => {
      sendAction('next');
    });
    navigator.mediaSession.setActionHandler('stop', () => {
      sendAction('cancel');
    });
  } catch (e) {}
} else if (!isIOS && 'mediaSession' in navigator) {
  // Clear any existing action handlers on desktop
  ['play', 'pause', 'nexttrack', 'previoustrack', 'stop', 'seekbackward', 'seekforward'].forEach(act => {
    try {
      navigator.mediaSession.setActionHandler(act, null);
    } catch (e) {}
  });
}

// ==================== SCREEN WAKE LOCK ====================
let wakeLock = null;

async function requestScreenWakeLock() {
  if ('wakeLock' in navigator && !wakeLock && document.visibilityState === 'visible') {
    try {
      wakeLock = await navigator.wakeLock.request('screen');
      wakeLock.addEventListener('release', () => {
        wakeLock = null;
      });
    } catch (err) {}
  }
}

function releaseScreenWakeLock() {
  if (wakeLock) {
    wakeLock.release().catch(() => {});
    wakeLock = null;
  }
}

document.addEventListener('visibilitychange', () => {
  if (document.visibilityState === 'visible' && state.timer && state.timer.status === 'running') {
    requestScreenWakeLock();
  }
});
