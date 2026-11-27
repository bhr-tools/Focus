// The master password of an app (server side: kit/auth): first-run setup,
// the lock screen, the lock-icon menu (Change password / Lock). Same labels
// and flow in every app (GetDone's).
//
//   const lock = createLock({
//     appName: 'Get Done',
//     beforeLock: async () => true,  // e.g. sync, or confirm losing changes
//     afterLock: async () => {},     // e.g. engine.reset()
//     onUnlocked: async () => {},    // load the data
//   });
//   await lock.start();              // shows setup or login when needed
//   (an app with its own start-up, offline for instance, calls
//   lock.showSetup() / lock.showLogin() itself)
//   lockButton.onclick = () => lock.menu(lockButton);
//   api 401 → lock.showLogin();

import { h, icon } from './dom.js';
import { openModal, openMenu, closeAllModals, topModal, toast } from './overlay.js';
import { ApiError } from './errors.js';

async function call(method, path, body) {
  let res;
  try {
    res = await fetch(path, {
      method,
      credentials: 'same-origin',
      headers: body ? { 'Content-Type': 'application/json' } : {},
      body: body ? JSON.stringify(body) : undefined,
    });
  } catch {
    throw new ApiError('Cannot reach the server. Check your connection.', 0);
  }
  const data = res.status === 204 ? null : await res.json().catch(() => null);
  if (!res.ok) throw new ApiError(data?.error || `Error ${res.status}`, res.status);
  return data;
}

export const authApi = {
  status: () => call('GET', '/api/auth/status'),
  setup: (password) => call('POST', '/api/auth/setup', { password }),
  login: (password) => call('POST', '/api/auth/login', { password }),
  logout: () => call('POST', '/api/auth/logout'),
  changePassword: (oldPassword, newPassword) =>
    call('POST', '/api/auth/change-password', { old_password: oldPassword, new_password: newPassword }),
};

/**
 * A password dialog. fields: [{ label, placeholder, autocomplete }];
 * onSubmit(values) resolves to an error message to show, or null to close.
 */
export function passwordForm({ title, intro, fields, submitLabel, dismissible = true, onSubmit }) {
  const error = h('div', { class: 'form-error', hidden: true });
  const inputs = fields.map((f) => h('input', {
    type: 'password', class: 'input', placeholder: f.placeholder, autocomplete: f.autocomplete, attrs: { 'aria-label': f.label },
  }));
  const submit = h('button', { type: 'submit', class: 'button primary wide' }, submitLabel);
  const form = h('form', { class: 'auth-form' },
    intro ? h('p', { class: 'dialog-message' }, intro) : null,
    error,
    fields.map((f, i) => h('label', { class: 'field' }, h('span', { class: 'field-label' }, f.label), inputs[i])),
    submit);
  let modal;
  form.addEventListener('submit', async (e) => {
    e.preventDefault();
    error.hidden = true;
    submit.disabled = true;
    try {
      const message = await onSubmit(inputs.map((i) => i.value));
      if (message) {
        error.textContent = message;
        error.hidden = false;
      } else {
        modal.close();
      }
    } catch (err) {
      error.textContent = err.message;
      error.hidden = false;
    } finally {
      submit.disabled = false;
    }
  });
  modal = openModal({ title, body: form, className: 'dialog auth', dismissible });
  return modal;
}

export function createLock({ appName, beforeLock = async () => true, afterLock = async () => {}, onUnlocked = async () => {} }) {
  let status = { has_password: false, authenticated: false, no_auth: false };

  const unlocked = async () => {
    status = { ...status, has_password: true, authenticated: true };
    await onUnlocked();
  };

  function showSetup() {
    closeAllModals({ keepUndismissible: false });
    passwordForm({
      title: `Welcome to ${appName}`,
      intro: 'Choose a master password to protect this instance. You can change it later.',
      fields: [
        { label: 'Master password', placeholder: 'At least 4 characters', autocomplete: 'new-password' },
        { label: 'Confirm password', placeholder: 'Repeat the password', autocomplete: 'new-password' },
      ],
      submitLabel: 'Set password and continue',
      dismissible: false,
      onSubmit: async ([password, confirm]) => {
        if (password.length < 4) return 'The password must be at least 4 characters long.';
        if (password !== confirm) return 'The passwords do not match.';
        await authApi.setup(password);
        unlocked();
        return null;
      },
    });
  }

  function showLogin() {
    if (topModal()?.el.classList.contains('auth')) return;
    closeAllModals({ keepUndismissible: false });
    status.authenticated = false;
    passwordForm({
      title: `${appName} is locked`,
      intro: 'Enter your master password to unlock.',
      fields: [{ label: 'Password', placeholder: 'Master password', autocomplete: 'current-password' }],
      submitLabel: 'Unlock',
      dismissible: false,
      onSubmit: async ([password]) => {
        if (!password) return 'Enter your password.';
        await authApi.login(password);
        unlocked();
        return null;
      },
    });
  }

  function changePassword() {
    passwordForm({
      title: 'Change password',
      fields: [
        { label: 'Current password', placeholder: 'Current master password', autocomplete: 'current-password' },
        { label: 'New password', placeholder: 'At least 4 characters', autocomplete: 'new-password' },
        { label: 'Confirm new password', placeholder: 'Repeat the new password', autocomplete: 'new-password' },
      ],
      submitLabel: 'Update password',
      onSubmit: async ([current, next, confirm]) => {
        if (next.length < 4) return 'The new password must be at least 4 characters long.';
        if (next !== confirm) return 'The new passwords do not match.';
        await authApi.changePassword(current, next);
        toast('Password updated. Other devices were signed out.', { type: 'success' });
        return null;
      },
    });
  }

  async function lock() {
    if (!(await beforeLock())) return;
    try {
      await authApi.logout();
    } catch (err) {
      toast(err.message, { type: 'error' });
      return;
    }
    await afterLock();
    showLogin();
  }

  return {
    get status() { return status; },

    /** Asks the server; shows setup or login when needed, else runs onUnlocked. */
    async start() {
      status = await authApi.status();
      if (status.no_auth || status.authenticated) return unlocked();
      if (!status.has_password) return showSetup();
      return showLogin();
    },

    showSetup,
    showLogin,
    changePassword,
    lock,

    /** The lock-icon menu. Hidden without a password (no_auth). */
    menu(anchor) {
      return openMenu(anchor, [
        { label: 'Change password', icon: 'key', run: changePassword },
        { label: 'Lock', icon: 'lock', run: lock },
      ], { title: 'Security' });
    },

    /** A lock icon button opening the menu, hidden while there is no password. */
    button() {
      const b = h('button', { type: 'button', class: 'icon-button lock-button', title: 'Security', attrs: { 'aria-label': 'Security' } }, icon('lock'));
      b.addEventListener('click', () => this.menu(b));
      b.hidden = status.no_auth;
      return b;
    },
  };
}
