// Floating layers: popovers (anchored menus that become bottom sheets on
// phones), a stack of modal dialogs (full-screen pages pushed from the right
// on phones), and toasts. Escape and outside clicks are handled here, for
// every layer, in one place. Styles: css/overlay.css.

import { h, icon, isMobile } from './dom.js';

// iOS ignores interactive-widget=resizes-content: the keyboard covers the page
// instead of shrinking it. Expose the visible area so that dialogs and bottom
// sheets can sit above the keyboard (overlay.css reads --vv-*).
const viewport = window.visualViewport;
if (viewport) {
  const sync = () => {
    const style = document.documentElement.style;
    style.setProperty('--vv-top', `${viewport.offsetTop}px`);
    style.setProperty('--vv-height', `${viewport.height}px`);
    style.setProperty('--vv-bottom', `${Math.max(0, window.innerHeight - viewport.offsetTop - viewport.height)}px`);
  };
  viewport.addEventListener('resize', sync);
  viewport.addEventListener('scroll', sync);
  sync();
}

const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)');

// ---------------------------------------------------------------------------
// Popover
// ---------------------------------------------------------------------------

let popover = null; // { el, backdrop, anchor, onClose }

/**
 * Shows `content` next to `anchor` (desktop) or as a bottom sheet with a
 * title and a close button (phone). `point` ({ x, y }) places it at the
 * pointer instead of under the anchor (context menus). Only one popover is
 * open at a time. Returns a function closing it.
 */
export function openPopover(anchor, content, { onClose, title, className = '', point = null } = {}) {
  closePopover();
  const sheet = isMobile();
  const el = h('div', { class: ['popover', sheet && 'sheet', className], attrs: { role: 'dialog' } },
    sheet ? h('div', { class: 'sheet-head' },
      h('div', { class: 'sheet-title' }, title || ''),
      h('button', { type: 'button', class: 'icon-button', title: 'Close', attrs: { 'aria-label': 'Close' }, onclick: () => closePopover() }, icon('close')))
      : null,
    content);
  const backdrop = sheet ? h('div', { class: 'sheet-backdrop', onclick: () => closePopover() }) : null;
  if (backdrop) document.body.append(backdrop);
  document.body.append(el);
  popover = { el, backdrop, anchor, onClose };
  if (!sheet) {
    const at = point && { getBoundingClientRect: () => ({ left: point.x, right: point.x, top: point.y, bottom: point.y, width: 0, height: 0 }) };
    positionPopover(el, at || anchor);
  }
  anchor?.classList.add('is-open');
  return closePopover;
}

export function closePopover() {
  if (!popover) return false;
  const { el, backdrop, anchor, onClose } = popover;
  popover = null;
  el.remove();
  backdrop?.remove();
  anchor?.classList.remove('is-open');
  onClose?.();
  return true;
}

export function isPopoverOpen() {
  return popover !== null;
}

/** Places the popover under its anchor, flipped above or shifted left to stay on screen. */
function positionPopover(el, anchor) {
  const margin = 8;
  const a = anchor.getBoundingClientRect();
  // As wide as a field button; a wider anchor (a list row) leaves the
  // popover at its content's width.
  el.style.minWidth = `${a.width <= 340 ? Math.max(a.width, 180) : 180}px`;
  const p = el.getBoundingClientRect();
  let top = a.bottom + 4;
  if (top + p.height > window.innerHeight - margin && a.top - p.height - 4 > margin) top = a.top - p.height - 4;
  top = Math.max(margin, Math.min(top, window.innerHeight - p.height - margin));
  let left = a.left;
  if (left + p.width > window.innerWidth - margin) left = Math.max(margin, a.right - p.width);
  el.style.top = `${top}px`;
  el.style.left = `${left}px`;
  el.style.maxHeight = `${window.innerHeight - top - margin}px`;
}

// Close the popover on any press outside it (and outside its anchor, which
// toggles it itself).
document.addEventListener('pointerdown', (e) => {
  if (popover && !popover.el.contains(e.target) && !popover.anchor?.contains(e.target) && !popover.backdrop) {
    closePopover();
  }
}, true);
window.addEventListener('resize', () => {
  if (popover && !popover.backdrop) closePopover();
});

/**
 * A list of choices for a popover: items are 'separator' or
 * { label, icon?, hint?, selected?, danger?, className?, run }.
 */
export function menuList(items) {
  return h('div', { class: 'menu', attrs: { role: 'menu' } },
    items.map((item) => item === 'separator'
      ? h('div', { class: 'menu-separator' })
      : h('button', {
        type: 'button',
        class: ['menu-item', item.selected && 'selected', item.danger && 'danger', item.className],
        attrs: { role: 'menuitem' },
        onclick: () => { closePopover(); item.run(); },
      },
      item.icon ? icon(item.icon) : null,
      h('span', { class: 'menu-label' }, item.label),
      item.hint ? h('span', { class: 'menu-hint' }, item.hint) : null)));
}

/** Opens a menu of items under anchor. Falsy items are skipped. */
export function openMenu(anchor, items, options) {
  return openPopover(anchor, menuList(items.filter(Boolean)), options);
}

/**
 * Opens the app's own menu in place of the browser's, at the pointer of a
 * contextmenu event (a bottom sheet on phones).
 */
export function openContextMenu(e, items, options = {}) {
  e.preventDefault();
  e.stopPropagation();
  return openPopover(null, menuList(items.filter(Boolean)), { ...options, point: { x: e.clientX, y: e.clientY } });
}

// ---------------------------------------------------------------------------
// Modals
// ---------------------------------------------------------------------------

const modals = []; // stack, top last

/**
 * Opens a modal dialog: centred on a desktop, a full-screen page pushed from
 * the right on a phone, closed by its back button or a swipe from the left
 * edge.
 *   title     header text (omit for a custom header inside `body`; '' for a
 *             header with no title)
 *   back      text of the close button on phones (default "Cancel" for
 *             forms, "Back" otherwise). With a footer and no `back`, phones
 *             leave it out: the footer's own Cancel / Done is in thumb reach
 *   action    { label, run }: a header button (phones: on the right)
 *   body      element(s)
 *   footer    element(s), typically buttons
 *   dismissible  false for dialogs that must not be closed (login)
 *   onClose   called once when the modal closes
 *   onEscape  overrides what Escape, the back button and the swipe do
 *             (default: close) — e.g. to ask "Discard changes?"
 * Returns { el, overlay, action, close }.
 */
export function openModal({ title, back, action, body, footer, className = '', dismissible = true, autofocus = true, onClose, onEscape }) {
  // Pages that only show something go back; forms are cancelled.
  const footerCloses = back == null && !!footer;
  back ??= action || footer ? 'Cancel' : 'Back';
  const actionButton = action
    ? h('button', { type: 'button', class: 'modal-action', onclick: () => action.run() }, action.label)
    : null;
  const box = h('div', { class: ['modal', className, footerCloses && 'footer-closes'], tabIndex: -1, attrs: { role: 'dialog', 'aria-modal': 'true' } },
    title != null ? h('div', { class: 'modal-header' },
      dismissible
        ? h('button', { type: 'button', class: 'modal-close', title: 'Close (Esc)', attrs: { 'aria-label': back }, onclick: () => handle.onEscape() },
          h('span', { class: 'close-text' }, icon('chevronLeft'), back), icon('close'))
        : null,
      h('h2', { class: 'modal-title' }, title),
      actionButton || h('span', { class: 'modal-spacer' }))
      : null,
    h('div', { class: 'modal-body' }, body),
    footer ? h('div', { class: 'modal-footer' }, footer) : null);
  const overlay = h('div', { class: 'modal-overlay' }, box);
  overlay.addEventListener('mousedown', (e) => {
    if (e.target === overlay && dismissible) handle.onEscape();
  });
  if (dismissible) swipeBack(overlay, box, () => handle.onEscape());

  let closed = false;
  const handle = {
    el: box,
    overlay,
    action: actionButton,
    dismissible,
    onEscape: onEscape || (() => handle.close()),
    close() {
      if (closed) return;
      closed = true;
      closePopover();
      modals.splice(modals.indexOf(handle), 1);
      document.body.classList.toggle('has-modal', modals.length > 0);
      // Phones slide the page back out; it no longer takes clicks meanwhile.
      if (isMobile() && !reducedMotion.matches) {
        overlay.classList.remove('open');
        overlay.style.pointerEvents = 'none';
        setTimeout(() => overlay.remove(), 240);
      } else {
        overlay.remove();
      }
      onClose?.();
    },
  };
  // A field left with blur() drops focus on <body>, out of reach of the
  // dialog's own key handlers: hand it back to the dialog.
  box.addEventListener('focusout', () => setTimeout(() => {
    if (!closed && topModal() === handle && (document.activeElement === document.body || !document.activeElement)) {
      box.focus({ preventScroll: true });
    }
  }, 0));
  modals.push(handle);
  document.body.append(overlay);
  document.body.classList.add('has-modal');
  // Focus synchronously: iOS only opens the keyboard when focus() runs inside
  // the tap that opened the dialog, not in a later animation frame.
  if (autofocus) box.querySelector('[autofocus], input:not([type=hidden]):not([readonly]), textarea')?.focus();
  else box.focus({ preventScroll: true });
  requestAnimationFrame(() => overlay.classList.add('open'));
  return handle;
}

/** Phones: dragging a page from the left edge to the right goes back. */
function swipeBack(overlay, box, goBack) {
  let start = null;
  overlay.addEventListener('touchstart', (e) => {
    const x = e.touches[0].clientX;
    if (!isMobile() || e.touches.length > 1 || x > 24) return;
    start = { x, y: e.touches[0].clientY, dx: 0 };
  }, { passive: true });
  overlay.addEventListener('touchmove', (e) => {
    if (!start) return;
    start.dx = Math.max(0, e.touches[0].clientX - start.x);
    if (Math.abs(e.touches[0].clientY - start.y) > 40 && start.dx < 20) {
      start = null;
      return;
    }
    overlay.classList.add('dragging');
    box.style.transform = `translateX(${start.dx}px)`;
  }, { passive: true });
  const end = () => {
    if (!start) return;
    const { dx } = start;
    start = null;
    overlay.classList.remove('dragging');
    box.style.transform = '';
    if (dx > 90) goBack();
  };
  overlay.addEventListener('touchend', end, { passive: true });
  overlay.addEventListener('touchcancel', end, { passive: true });
}

export function topModal() {
  return modals[modals.length - 1] || null;
}

export function closeAllModals({ keepUndismissible = true } = {}) {
  for (const m of [...modals].reverse()) {
    if (!keepUndismissible || m.dismissible) m.close();
  }
}

/** Handles Escape for the topmost layer. Returns true when something was closed. */
export function escapeTopLayer() {
  if (closePopover()) return true;
  const m = topModal();
  if (m && m.dismissible) {
    m.onEscape();
    return true;
  }
  return false;
}

/** Resolves to true (confirm) or false (cancel). */
export function confirmDialog(title, message, { okText = 'Confirm', cancelText = 'Cancel', danger = true } = {}) {
  return new Promise((resolve) => {
    let answer = false;
    const ok = h('button', { type: 'button', class: ['button', danger ? 'danger' : 'primary'], onclick: () => { answer = true; m.close(); } }, okText);
    const m = openModal({
      title,
      className: 'dialog',
      body: h('p', { class: 'dialog-message' }, message),
      footer: [h('button', { type: 'button', class: 'button', onclick: () => m.close() }, cancelText), ok],
      onClose: () => resolve(answer),
    });
    m.el.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        ok.click();
      }
    });
    // At once: a key pressed right after must reach this dialog, not the
    // field that opened it.
    ok.focus();
  });
}

/**
 * Asks for a line of text. Resolves to the text, or null when cancelled (or,
 * without allowEmpty, when left empty; with it, an empty answer is '').
 */
export function promptDialog(title, { value = '', placeholder = '', okText = 'Save', maxLength = 200, allowEmpty = false } = {}) {
  return new Promise((resolve) => {
    let answer = null;
    const input = h('input', { type: 'text', class: 'input', value, placeholder, maxLength, autocomplete: 'off' });
    const submit = () => {
      answer = input.value.trim() || (allowEmpty ? '' : null);
      m.close();
    };
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        submit();
      }
    });
    const m = openModal({
      title,
      className: 'dialog',
      body: input,
      footer: [
        h('button', { type: 'button', class: 'button', onclick: () => m.close() }, 'Cancel'),
        h('button', { type: 'button', class: 'button primary', onclick: submit }, okText),
      ],
      onClose: () => resolve(answer),
    });
    // At once, like the focus: selected a frame later, the text would swallow
    // the first letters typed right after the shortcut.
    input.select();
  });
}

// ---------------------------------------------------------------------------
// Toast
// ---------------------------------------------------------------------------

let toastEl = null;
let toastTimer = 0;

/** Shows a short message. type: 'info' | 'success' | 'error'. action: { label, run }. */
export function toast(message, { type = 'info', action = null } = {}) {
  if (!toastEl) {
    toastEl = h('div', { class: 'toast', attrs: { role: 'status', 'aria-live': 'polite' } });
    document.body.append(toastEl);
  }
  toastEl.className = `toast ${type}`;
  toastEl.replaceChildren(h('span', { class: 'toast-message' }, message));
  if (action) {
    toastEl.append(h('button', {
      type: 'button',
      class: 'toast-action',
      onclick: () => { hideToast(); action.run(); },
    }, action.label));
  }
  requestAnimationFrame(() => toastEl.classList.add('open'));
  clearTimeout(toastTimer);
  toastTimer = setTimeout(hideToast, action ? 6000 : 3500);
}

function hideToast() {
  toastEl?.classList.remove('open');
}
