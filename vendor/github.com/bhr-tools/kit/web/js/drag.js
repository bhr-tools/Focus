// Dragging rows, with the mouse or (after a long press) a finger:
//   sortable()  reorders a list; where the row lands is one line in the gap
//               between two rows (never an outline on the rows), and pulling
//               right or left picks the nesting depth when the list nests;
//   draggable() drops a row on a target (a folder, a project, a box), which
//               lights up while the row is over it.
// sortable() takes targets too: a task dragged onto a sidebar project.
// Built on pointer.js (follow) and dropgaps.js. Styles: css/gestures.css.

import { h } from './dom.js';
import { follow } from './pointer.js';
import { listGaps, nearestGap, pickDepth, resolveDrop } from './dropgaps.js';

let line = null;
let ghost = null;
let dragging = false;
let deferred = null;

/**
 * True while a row is dragged: `render` then runs once it is dropped, so
 * that a list drawn again (a sync) does not pull the row from under the
 * pointer.
 */
export function deferWhileDragging(render) {
  if (!dragging) return false;
  deferred = render;
  return true;
}

function showLine(pos) {
  line ??= document.body.appendChild(h('div', { class: 'drop-line', hidden: true }));
  line.hidden = !pos;
  if (pos) Object.assign(line.style, { top: `${pos.y}px`, left: `${pos.left}px`, width: `${Math.max(0, pos.width)}px` });
}

function showGhost(label, point) {
  if (!label) return;
  ghost ??= document.body.appendChild(h('div', { class: 'drag-ghost' }));
  ghost.textContent = label;
  ghost.hidden = !point;
  if (point) ghost.style.transform = `translate(${point.x + 12}px, ${point.y + 12}px)`;
}

/** The drop target under a point: { el, key } from targets(), or null. */
function targetAt(targets, point, dragged) {
  if (!targets) return null;
  for (const t of targets()) {
    if (t.key === dragged) continue;
    const r = t.el.getBoundingClientRect();
    if (point.x >= r.left && point.x <= r.right && point.y >= r.top && point.y <= r.bottom) return t;
  }
  return null;
}

function lightTarget(current, next) {
  if (current?.el !== next?.el) {
    current?.el.classList.remove('drop-target');
    next?.el.classList.add('drop-target');
  }
  return next;
}

/**
 * Starts dragging a row from a pointerdown. Shared by sortable and
 * draggable; `gaps(point)` (sortable) returns where the line goes, or null.
 */
function drag(down, row, { key, label, gaps, targets, onDrop, onDropOn, onClick, immediate }) {
  let over = null;
  let place = null;
  follow(down, {
    onStart(p) {
      dragging = true;
      row.classList.add('dragging');
      showGhost(label?.(row), p);
    },
    onMove(p) {
      showGhost(label?.(row), p);
      over = lightTarget(over, targetAt(targets, p, key));
      place = over ? null : gaps?.(p) ?? null;
      showLine(place);
    },
    onEnd() {
      const target = over;
      const drop = place;
      cleanup();
      if (target) onDropOn?.({ key, target });
      else if (drop) onDrop?.({ key, ...drop.result });
    },
    onCancel: cleanup,
    onClick: (p) => onClick?.(p),
    immediate,
  });
  function cleanup() {
    row.classList.remove('dragging');
    lightTarget(over, null);
    over = null;
    place = null;
    showLine(null);
    showGhost(label?.(row), null);
    dragging = false;
    const render = deferred;
    deferred = null;
    render?.();
  }
}

/**
 * Makes the rows of `container` (children with data-key) reorderable.
 *   handle      selector of the part to grab (default: the whole row); on
 *               touch screens a handle drags at once (no long press): give
 *               it the class drag-handle, which stops it from scrolling
 *   indent      px per nesting level (0: a flat list)
 *   depthOf(row)  nesting depth of a row (default 0)
 *   hidden(key, row)  rows that move with the dragged one (its children):
 *               left out of the gaps
 *   targets()   [{ el, key }] other places to drop on (sidebar projects…)
 *   label(row)  text of the ghost under the pointer (none: no ghost)
 *   onMove({ key, index, depth, parentId, afterId, beforeId })
 *               dropped in a gap; index counts rows without the dragged one
 *   onDropOn({ key, target })
 */
export function sortable(container, { handle = null, indent = 0, depthOf = () => 0, hidden = () => false, targets, label, onMove, onDropOn }) {
  container.addEventListener('pointerdown', (down) => {
    if (down.button !== 0) return;
    const row = down.target.closest('[data-key]');
    if (!row || row.parentElement !== container) return;
    if (handle && !down.target.closest(handle)) return;
    if (down.target.closest('input, textarea, select, [contenteditable]')) return;
    const key = row.dataset.key;
    const startDepth = depthOf(row);
    let depth = startDepth;
    const gaps = (p) => {
      const rows = [...container.children]
        .filter((r) => r.dataset.key && r.dataset.key !== key && !hidden(key, r))
        .map((r) => {
          const b = r.getBoundingClientRect();
          return { id: r.dataset.key, depth: depthOf(r), top: b.top, bottom: b.bottom, left: b.left, width: b.width };
        });
      const box = container.getBoundingClientRect();
      const gap = nearestGap(listGaps(rows, box.top + 4), p.y);
      if (indent) depth = pickDepth(startDepth + (p.x - down.clientX) / indent, gap, depth);
      else depth = 0;
      const ref = rows[gap.index] || rows[gap.index - 1];
      const left = (ref?.left ?? box.left) + depth * indent + 6;
      return {
        y: gap.y,
        left,
        width: (ref ? ref.left + ref.width : box.right) - left,
        result: { index: gap.index, depth, ...resolveDrop(rows, gap.index, depth) },
      };
    };
    drag(down, row, { key, label, gaps, targets, onDrop: onMove, onDropOn, immediate: !!handle });
  });
}

/**
 * Lets the rows of `container` (children with data-key, or `rows` selector)
 * be dropped on targets: { targets(), label(row), onDropOn({ key, target }) }.
 * accept(down) may refuse a drag (no target on screen: the row keeps its
 * other gestures, a swipe for instance).
 */
export function draggable(container, { rows = '[data-key]', targets, label, onDropOn, accept = () => true }) {
  container.addEventListener('pointerdown', (down) => {
    if (down.button !== 0 || !accept(down)) return;
    const row = down.target.closest(rows);
    if (!row || !container.contains(row)) return;
    if (down.target.closest('input, textarea, select, [contenteditable]')) return;
    drag(down, row, { key: row.dataset.key, label, targets, onDropOn });
  });
}
