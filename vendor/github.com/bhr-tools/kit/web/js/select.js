// Multi-selection of rows: Space (or x) toggles the cursor row, Ctrl+click
// toggles, Shift+click selects the range from the last row clicked. Rows are
// keys; the app keeps their order and draws the selection (class "selected"
// on rows, its own action bar). Without the DOM: tested in Node.

export function createSelection({ onChange = () => {} } = {}) {
  const keys = new Set();
  let anchor = null;

  const changed = () => onChange([...keys]);

  return {
    has: (key) => keys.has(key),
    get size() { return keys.size; },
    get keys() { return [...keys]; },

    toggle(key) {
      if (keys.has(key)) keys.delete(key);
      else keys.add(key);
      anchor = key;
      changed();
    },

    /** Adds the rows from the anchor to `key`, in `order` (all keys of the list). */
    extend(key, order) {
      const from = order.indexOf(anchor ?? key);
      const to = order.indexOf(key);
      if (from < 0 || to < 0) return this.toggle(key);
      const [a, b] = from <= to ? [from, to] : [to, from];
      for (const k of order.slice(a, b + 1)) keys.add(k);
      anchor = key;
      changed();
    },

    /**
     * Handles a click on a row. Returns true when the click was a selection
     * (Ctrl or Shift, or while something is selected), false for a plain
     * click the app handles (open the row).
     */
    click(e, key, order) {
      if (e.shiftKey) this.extend(key, order);
      else if (e.ctrlKey || e.metaKey || keys.size) this.toggle(key);
      else return false;
      return true;
    },

    /** Keeps only keys still in the list (after a reload). */
    retain(order) {
      const before = keys.size;
      for (const k of [...keys]) if (!order.includes(k)) keys.delete(k);
      if (keys.size !== before) changed();
    },

    clear() {
      if (!keys.size) return false;
      keys.clear();
      anchor = null;
      changed();
      return true;
    },
  };
}
