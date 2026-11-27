// Typing in a project field opens a panel of matching projects, so that an
// existing project is reused instead of created again under a variant. The
// timer field and the dialog fields share it.

import { h } from './dom.js';
import { canonicalProjectName, colorDot } from './format.js';

const pickers = new Set();

/** Every picker shows the new projects. */
export function refreshPickers() {
  for (const picker of pickers) if (picker.isOpen()) picker.open(picker.input.value);
}

export function closePickers() {
  for (const picker of pickers) picker.close();
}

/** Project fields are lowercased as the user types, the caret kept in place. */
function lowercaseWhileTyping(input) {
  input.addEventListener('input', () => {
    const lower = input.value.toLowerCase();
    if (lower === input.value) return;
    const { selectionStart, selectionEnd } = input;
    input.value = lower;
    input.setSelectionRange(selectionStart, selectionEnd);
  });
}

/**
 * Wires the panel to a text field inside a .project-picker container.
 *   projects()     { all, month } lists of { name, color }
 *   canOpen()      false keeps the panel closed (timer running)
 *   onChoose(name) the chosen project
 *   onToggle(open) the panel opened or closed
 *   onEnter(e)     Enter with no highlighted entry
 */
export function createProjectPicker(input, { projects, canOpen = () => true, onChoose = () => {}, onToggle = () => {}, onEnter = () => {} }) {
  const container = input.closest('.project-picker');
  const menu = h('div', { class: 'pm project-menu', hidden: true });
  container.append(menu);
  let items = [];
  let active = -1;

  const find = (name) => {
    const key = (name || '').trim().toLowerCase();
    return projects().all.find((p) => p.name.toLowerCase() === key) || null;
  };

  // Projects containing the query, prefix matches first.
  const matches = (query) => {
    const { all, month } = projects();
    const q = query.trim().toLowerCase();
    if (!q) return month.length > 0 ? month : all;
    const rank = (p) => (p.name.toLowerCase().startsWith(q) ? 0 : 1);
    return all.filter((p) => p.name.toLowerCase().includes(q))
      .sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name));
  };

  function highlight(index) {
    active = index;
    menu.querySelectorAll('.pm-item').forEach((el, i) => {
      el.classList.toggle('active', i === index);
      if (i === index) el.scrollIntoView({ block: 'nearest' });
    });
  }

  function close() {
    if (menu.hidden) return;
    menu.hidden = true;
    active = -1;
    onToggle(false);
  }

  function choose(index) {
    const entry = items[index];
    if (!entry) return;
    close();
    input.value = canonicalProjectName(entry.name);
    onChoose(input.value);
  }

  function open(query) {
    if (!canOpen()) return;
    const typed = query.trim();
    items = matches(query).map((p) => ({ name: p.name, color: p.color }));
    if (typed && !find(typed)) items.push({ name: typed, create: true });
    active = typed && items.length > 0 ? 0 : -1;
    menu.replaceChildren(
      h('div', { class: 'pm-header' }, typed ? 'Matching projects' : projects().month.length > 0 ? 'This month' : 'Projects'),
      items.length === 0 ? h('div', { class: 'pm-empty' }, 'No project yet: type a name') : null,
      ...items.map((entry, i) => h('button', {
        type: 'button',
        class: ['pm-item', entry.create && 'create'],
        tabIndex: -1,
        // Keep the focus in the field: the choice happens on click.
        onpointerdown: (e) => e.preventDefault(),
        onclick: () => choose(i),
      }, entry.create ? colorDot('var(--text-dim)') : colorDot(entry.color),
      h('span', null, entry.create ? `Create “${entry.name}”` : entry.name))));
    const wasHidden = menu.hidden;
    menu.hidden = false;
    if (wasHidden) onToggle(true);
    highlight(active);
  }

  lowercaseWhileTyping(input);
  input.addEventListener('input', () => {
    if (input.value.trim()) open(input.value);
    else close();
  });
  input.addEventListener('blur', () => setTimeout(close, 0));
  input.addEventListener('keydown', (e) => {
    const isOpen = !menu.hidden;
    switch (e.key) {
      case 'ArrowDown':
      case 'ArrowUp':
        e.preventDefault();
        if (!isOpen) open(input.value);
        else if (items.length > 0) highlight((active + (e.key === 'ArrowDown' ? 1 : -1) + items.length) % items.length);
        break;
      case 'Enter':
        if (isOpen && active >= 0) {
          e.preventDefault(); // do not submit the dialog
          choose(active);
        } else {
          onEnter(e);
        }
        break;
      case 'Escape':
        if (isOpen) {
          e.preventDefault(); // close the panel, not the dialog
          e.stopPropagation();
          close();
        }
        break;
    }
  });
  const outside = (e) => { if (!container.contains(e.target)) close(); };
  document.addEventListener('click', outside);

  const picker = {
    input, open, close, find,
    isOpen: () => !menu.hidden,
    destroy() {
      document.removeEventListener('click', outside);
      pickers.delete(picker);
    },
  };
  pickers.add(picker);
  return picker;
}
