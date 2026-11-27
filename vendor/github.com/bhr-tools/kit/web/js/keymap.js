// Key tables, without the DOM (tested in Node). A table is a list of
// bindings; keys.js feeds it the keys typed outside fields and dialogs.
//
//   { keys: ['j', 'ArrowDown'], label: 'Next item', group: 'Move', run() {} }
//   { keys: ['gg'], … }       a sequence: typed within a second
//   { keys: ['C-d'], … }      with Ctrl
//   { digit: true, run(n) {} } 1–9 and 0, and the same keys on AZERTY
//                             (& é " ' ( - è _ ç à) without Shift
//   { …, hidden: true }       left out of the ? help
//   { keys, label, group }    without run: only listed in the ? help (keys a
//                             dialog handles itself, such as Ctrl+Enter)
//   { …, helpKeys: ['1', '2', '3'] }  what the help shows instead of keys
//
// Conventions of every app: u = undo, ? = help, / = search, Esc = clear.

/** Digit of each key of the top row, on QWERTY and AZERTY. */
export const DIGITS = {
  1: 1, 2: 2, 3: 3, 4: 4, 5: 5, 6: 6, 7: 7, 8: 8, 9: 9, 0: 0,
  '&': 1, 'é': 2, '"': 3, "'": 4, '(': 5, '-': 6, 'è': 7, '_': 8, 'ç': 9, 'à': 0,
};

const SEQUENCE_MS = 1000;

/** Name of a keydown event in a table: 'j', 'G', 'C-d', 'Enter', ' '. */
export function keyName(e) {
  return e.ctrlKey && !e.metaKey && !e.altKey ? `C-${e.key}` : e.key;
}

/**
 * Follows the keys typed against a table. feed(key) returns what to run:
 * { binding, digit? } or null; a key that starts a longer sequence waits
 * (and, alone, fires when the sequence times out, through onTimeout).
 */
export function createMatcher(getTable, { onTimeout } = {}) {
  let pending = '';
  let timer = 0;

  const reset = () => {
    clearTimeout(timer);
    pending = '';
  };

  const live = () => getTable().filter((b) => b.run);
  const find = (seq) => live().find((b) => b.keys?.includes(seq)) || null;
  const startsLonger = (seq) => live().some((b) => b.keys?.some((k) => k.length > seq.length && k.startsWith(seq)));

  function feed(key) {
    const seq = pending + key;
    clearTimeout(timer);
    if (startsLonger(seq)) {
      pending = seq;
      timer = setTimeout(() => {
        const exact = find(pending);
        pending = '';
        if (exact) onTimeout?.({ binding: exact });
      }, SEQUENCE_MS);
      return { waiting: true };
    }
    pending = '';
    const exact = find(seq);
    if (exact) return { binding: exact };
    // A wrong second key: try it on its own.
    if (seq !== key) return feed(key);
    if (key in DIGITS) {
      const binding = live().find((b) => b.digit);
      if (binding) return { binding, digit: DIGITS[key] };
    }
    return null;
  }

  return { feed, reset, get pending() { return pending; } };
}

const NAMES = { ' ': 'Space', ArrowDown: '↓', ArrowUp: '↑', ArrowLeft: '←', ArrowRight: '→', Escape: 'Esc', Delete: 'Del' };

/** How a key of a table is shown: 'C-d' → 'Ctrl+d', ' ' → 'Space'. */
export function displayKey(key) {
  if (key.startsWith('C-') && key.length > 2) return `Ctrl+${key.slice(2)}`;
  return NAMES[key] ?? key;
}

/** Groups of the ? help: [{ group, rows: [{ label, keys: [...] }] }], in table order. */
export function helpGroups(table) {
  const groups = new Map();
  for (const b of table) {
    if (b.hidden || !b.label) continue;
    const name = b.group || 'General';
    if (!groups.has(name)) groups.set(name, []);
    const keys = b.helpKeys ?? (b.digit ? ['1–9'] : b.keys);
    groups.get(name).push({ label: b.label, keys: keys.map(displayKey) });
  }
  return [...groups].map(([group, rows]) => ({ group, rows }));
}
