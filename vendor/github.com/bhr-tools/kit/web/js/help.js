// The ? help, built from the key table in force: never kept by hand.

import { h } from './dom.js';
import { openModal } from './overlay.js';
import { helpGroups } from './keymap.js';

export function openHelp(title, table) {
  const body = h('div', { class: 'help-grid' },
    helpGroups(table).map(({ group, rows }) => h('section', { class: 'help-group' },
      h('h3', { class: 'help-title' }, group),
      rows.map((row) => h('div', { class: 'help-row' },
        h('span', { class: 'help-label' }, row.label),
        h('span', { class: 'help-keys' }, row.keys.map((k) => h('kbd', {}, k))))))));
  return openModal({ title, body, className: 'help', autofocus: false });
}
