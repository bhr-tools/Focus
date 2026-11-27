// Focus's dialogs, on the kit's: editing an activity, renaming a project,
// the ⋯ menu (export, import, connected apps) and connecting an app.

import { h } from './dom.js';
import { confirmDialog, openMenu, openModal, toast } from '/kit/js/overlay.js';
import { MONTH_NAMES, canonicalProjectName, toDatetimeLocal } from './format.js';
import { createProjectPicker } from './picker.js';
import { hideTooltip } from './tooltip.js';

const $ = (id) => document.getElementById(id);

let app = null;

const field = (label, input) => h('label', { class: 'field' }, h('span', { class: 'field-label' }, label), input);
const errorLine = () => h('div', { class: 'form-error', hidden: true });
const showError = (el, message) => {
  el.textContent = message;
  el.hidden = !message;
};

/** A project field with the picker, for a dialog. */
function projectField(value) {
  const input = h('input', { type: 'text', class: 'input', value, required: true, autocomplete: 'off', spellcheck: false });
  const wrap = h('div', { class: 'project-picker' }, input);
  const picker = createProjectPicker(input, { projects: () => ({ all: app.state.allProjects, month: app.state.monthProjects }) });
  return { input, wrap, picker };
}

/** Edits a session (or a month segment of one); null adds one. */
export function editSession(session) {
  hideTooltip();
  const isNew = !session;
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
  const project = projectField(isNew ? (app.state.timer?.project || '') : session.project_name);
  const startInput = h('input', { type: 'datetime-local', class: 'input', value: toDatetimeLocal(start), required: true });
  const endInput = h('input', { type: 'datetime-local', class: 'input', value: toDatetimeLocal(end), required: true });
  const error = errorLine();
  const save = async () => {
    const from = new Date(startInput.value);
    const to = new Date(endInput.value);
    if (isNaN(from) || isNaN(to)) return showError(error, 'Please enter valid dates.');
    if (to <= from) return showError(error, 'The end must be after the start.');
    if (!project.input.value.trim()) return showError(error, 'Choose a project.');
    const body = { project_name: canonicalProjectName(project.input.value), start_time: from.toISOString(), end_time: to.toISOString() };
    try {
      if (isNew) await app.api('/api/sessions', { method: 'POST', body: { ...body, mode: 'pomodoro' } });
      else await app.api(`/api/sessions/${session.id}`, { method: 'PUT', body });
      modal.close();
      app.refreshData();
    } catch (err) {
      showError(error, `Could not save: ${err.message}`);
    }
  };
  const remove = async () => {
    if (!await confirmDialog('Delete this activity?', 'It leaves your history for good.', { okText: 'Delete' })) return;
    try {
      await app.api(`/api/sessions/${session.id}`, { method: 'DELETE' });
      modal.close();
      app.refreshData();
    } catch (err) {
      showError(error, `Could not delete: ${err.message}`);
    }
  };
  const form = h('form', { class: 'auth-form', onsubmit: (e) => { e.preventDefault(); save(); } },
    field('Project', project.wrap),
    h('div', { class: 'form-row' }, field('Start', startInput), field('End', endInput)),
    error,
    h('button', { type: 'submit', hidden: true }));
  const modal = openModal({
    title: isNew ? 'Add activity' : 'Edit activity',
    className: 'session-dialog',
    body: form,
    footer: [
      isNew ? null : h('button', { type: 'button', class: 'button ghost danger-text footer-start', onclick: remove }, 'Delete'),
      h('button', { type: 'button', class: 'button', onclick: () => modal.close() }, 'Cancel'),
      h('button', { type: 'button', class: 'button primary', onclick: save }, isNew ? 'Add' : 'Save'),
    ],
    autofocus: isNew,
    onClose: () => project.picker.destroy(),
  });
  if (isNew) project.input.select();
}

/** Renames a project everywhere, or only within one month (month view). */
export function renameProject(name, year, month) {
  hideTooltip();
  const monthOnly = Boolean(year && month);
  const project = projectField(name);
  const error = errorLine();
  const save = async () => {
    const newName = canonicalProjectName(project.input.value);
    if (!newName || newName === name) return modal.close();
    try {
      if (monthOnly) await app.api('/api/projects/rename-month', { method: 'POST', body: { year, month, old_name: name, new_name: newName } });
      else await app.api('/api/projects/rename', { method: 'POST', body: { old_name: name, new_name: newName } });
      modal.close();
      app.refreshData();
    } catch (err) {
      showError(error, `Could not rename: ${err.message}`);
    }
  };
  const modal = openModal({
    title: monthOnly ? `Rename “${name}” in ${MONTH_NAMES[month - 1]} ${year}` : `Rename “${name}” everywhere`,
    body: h('form', { class: 'auth-form', onsubmit: (e) => { e.preventDefault(); save(); } },
      field('New name', project.wrap),
      h('p', { class: 'dialog-note' }, monthOnly
        ? 'Only the sessions of this month are renamed.'
        : 'Every session of this project is renamed. Choose an existing project to merge both.'),
      error,
      h('button', { type: 'submit', hidden: true })),
    footer: [
      h('button', { type: 'button', class: 'button', onclick: () => modal.close() }, 'Cancel'),
      h('button', { type: 'button', class: 'button primary', onclick: save }, 'Rename'),
    ],
    onClose: () => project.picker.destroy(),
  });
  project.input.select();
}

// ---------------------------------------------------------------------------
// The ⋯ menu
// ---------------------------------------------------------------------------

async function exportData(format) {
  try {
    const res = await app.api(`/api/export?format=${format}`, { raw: true });
    const name = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') || '');
    const url = URL.createObjectURL(await res.blob());
    const a = h('a', { href: url, download: name ? name[1] : `focus-export.${format}` });
    document.body.append(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 1000);
  } catch (err) {
    app.toastError(`Export failed: ${err.message}`);
  }
}

async function importFile(file) {
  const form = new FormData();
  form.append('file', file);
  toast('Importing…');
  try {
    const res = await app.api('/api/import', { method: 'POST', body: form });
    toast(`Imported ${res.imported} session${res.imported === 1 ? '' : 's'}, skipped ${res.skipped} (duplicates or invalid).`, { type: 'success' });
    app.refreshData();
  } catch (err) {
    app.toastError(`Import failed: ${err.message}`);
  }
}

/** The apps and command lines reading or driving the timer, each revocable. */
async function connectedApps() {
  const list = h('div', { class: 'apps-list' });
  const day = (date) => new Date(date).toLocaleDateString(undefined, { day: 'numeric', month: 'short' });
  const load = async () => {
    let apps = [];
    try { apps = await app.api('/api/apps'); } catch { /* shown empty */ }
    list.replaceChildren(...(apps.length ? apps.map((a) => h('div', { class: 'app-row' },
      h('b', null, a.name),
      h('span', { class: 'app-when' }, a.used_at ? `used ${day(a.used_at)}` : `connected ${day(a.created_at)}`),
      h('button', {
        type: 'button', class: 'button ghost danger-text',
        onclick: async () => {
          try {
            await app.api(`/api/apps/${a.id}`, { method: 'DELETE' });
            toast(`${a.name} disconnected`);
          } catch (err) {
            app.toastError(err.message);
          }
          load();
        },
      }, 'Disconnect'))) : [h('p', { class: 'dialog-note' }, 'No app is connected.')]));
  };
  await load();
  openModal({
    title: 'Connected apps',
    body: [
      list,
      h('p', { class: 'dialog-note' }, 'Apps read your hours, the command line (focus login) drives the timer. They stay connected after a password change, until you disconnect them.'),
    ],
  });
}

export function openMoreMenu(anchor) {
  openMenu(anchor, [
    { label: 'Export JSON', icon: 'download', run: () => exportData('json') },
    { label: 'Export CSV', icon: 'file', run: () => exportData('csv') },
    { label: 'Import a file…', icon: 'upload', run: () => $('import-input').click() },
    'separator',
    { label: 'Connected apps', icon: 'link', run: connectedApps },
  ], { title: 'Focus' });
}

// ---------------------------------------------------------------------------
// Connecting an app
// ---------------------------------------------------------------------------

// Another app (Project…) asks to read the hours: it sends the user to
// /?connect=<name>&redirect_uri=<url>&state=<s>. Once signed in, the user
// allows or refuses; either way the browser goes back to the app.
export function showConnect() {
  const q = new URLSearchParams(location.search);
  const name = q.get('connect');
  if (!name) return;
  const req = { app: name, redirect: q.get('redirect_uri') || '', state: q.get('state') || '' };
  let host = '';
  try {
    const u = new URL(req.redirect);
    if (u.protocol === 'https:' || u.protocol === 'http:') host = u.host;
  } catch { /* invalid: refused below */ }
  const back = (params) => {
    const url = new URL(req.redirect);
    for (const [k, v] of Object.entries({ ...params, state: req.state })) url.searchParams.set(k, v);
    location.replace(url.toString());
  };
  const done = () => {
    modal.close();
    history.replaceState(null, '', location.pathname);
  };
  const error = errorLine();
  showError(error, host ? '' : 'This request has no valid address to come back to.');
  const allow = h('button', {
    type: 'button', class: 'button primary', disabled: !host,
    onclick: async () => {
      allow.disabled = true;
      try {
        const { code } = await app.api('/api/connect', { method: 'POST', body: { app: req.app, redirect_uri: req.redirect } });
        back({ code });
      } catch (err) {
        showError(error, err.message);
        allow.disabled = false;
      }
    },
  }, 'Allow');
  const modal = openModal({
    title: 'Connect an app',
    dismissible: false,
    body: [
      h('p', { class: 'connect-ask' }, h('b', null, req.app), ' wants to read the hours you time.'),
      h('p', { class: 'dialog-message' }, 'It will see how long you worked each day and on which project. It cannot change anything. Asked by ',
        h('b', null, host || 'an unknown address'), '.'),
      h('p', { class: 'dialog-note' }, 'Disconnect it whenever you like, in ⋯ → Connected apps.'),
      error,
    ],
    footer: [
      h('button', { type: 'button', class: 'button', onclick: () => (host ? back({ error: 'access_denied' }) : done()) }, 'Cancel'),
      allow,
    ],
  });
}

export function initDialogs(theApp) {
  app = theApp;
  $('btn-more-menu').addEventListener('click', (e) => openMoreMenu(e.currentTarget));
  $('import-input').addEventListener('change', async (e) => {
    const file = e.target.files[0];
    e.target.value = '';
    if (file) await importFile(file);
  });
}
