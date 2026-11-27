// Vim-style keyboard control. One window-level handler decides, in order:
//   1. Escape closes the topmost popover or dialog, or leaves a field;
//   2. while a dialog or popover is open, other keys belong to it;
//   3. while typing in a field (INSERT mode), keys belong to the field;
//   4. otherwise keys run the bindings of the current table (keymap.js).
// Components that handle a key themselves call preventDefault(), and this
// handler then ignores it.

import { escapeTopLayer, topModal, isPopoverOpen } from './overlay.js';
import { isTextInput } from './dom.js';
import { createMatcher, keyName } from './keymap.js';
import { openHelp } from './help.js';

/**
 * Starts the keyboard handling.
 *   table()   the bindings in force now (per view, per focused pane…)
 *   onMode(insert)  called when typing in a field starts or stops (status bar)
 *   helpTitle title of the ? help, which lists table() (a binding on '?'
 *             replaces it)
 * Returns { handleKey }: an iframe (a mail body) forwards its keys there.
 */
export function initKeys({ table, onMode, helpTitle = 'Keyboard shortcuts' }) {
  const withHelp = () => {
    const t = table();
    return t.some((b) => b.keys?.includes('?'))
      ? t
      : [...t, { keys: ['?'], label: 'This help', group: 'General', run: () => openHelp(helpTitle, table()) }];
  };
  const run = ({ binding, digit }) => (binding.digit ? binding.run(digit) : binding.run());
  const matcher = createMatcher(withHelp, { onTimeout: run });

  if (onMode) {
    const update = () => onMode(isTextInput(document.activeElement));
    document.addEventListener('focusin', update);
    document.addEventListener('focusout', () => setTimeout(update, 0));
  }

  function handleKey(e) {
    if (e.defaultPrevented || e.isComposing) return;
    if (e.key === 'Escape') {
      if (escapeTopLayer()) {
        e.preventDefault();
        matcher.reset();
        return;
      }
      if (isTextInput(document.activeElement)) {
        e.preventDefault();
        document.activeElement.blur();
        return;
      }
    }
    if (topModal() || isPopoverOpen() || isTextInput(e.target)) return;
    if (e.metaKey || e.altKey) return;
    if (['Shift', 'Control', 'CapsLock'].includes(e.key)) return;

    const hit = matcher.feed(keyName(e));
    if (!hit) return;
    e.preventDefault();
    if (hit.binding) run(hit);
  }

  window.addEventListener('keydown', handleKey);
  return { handleKey };
}
