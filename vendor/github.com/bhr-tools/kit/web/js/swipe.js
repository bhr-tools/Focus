// Touch gestures of a phone: a row of the list slides aside to show an
// action, run when let go far enough; the list pulled down past its top
// checks for new mail. A mouse is left alone: the keys do this on a desktop.

import { h, icon } from './dom.js';

const START = 10; // pixels moved before a press is a swipe
const PULL = 70; // pixels pulled past the top to check for mail

// A row drawn again under the finger would lose the swipe: while a finger is
// on a row, the list waits.
let pressed = false;
let deferred = null;

/** True when a finger is on a row: `render` then runs once it is lifted. */
export function deferWhileSwiping(render) {
  if (!pressed) return false;
  deferred = render;
  return true;
}

function release() {
  pressed = false;
  const render = deferred;
  deferred = null;
  render?.();
}

/**
 * Makes `row` swipeable. `left` and `right` are the actions of a swipe that
 * way: { label, icon, className, away, run }. `away` slides the row out
 * before running (the row leaves the list). Returns the element to show.
 */
export function swipeable(row, { left, right }) {
  const el = h('div', { class: 'swipe' },
    right ? h('div', { class: ['swipe-action', 'swipe-right', right.className] }, icon(right.icon), h('span', null, right.label)) : null,
    left ? h('div', { class: ['swipe-action', 'swipe-left', left.className] }, h('span', null, left.label), icon(left.icon)) : null,
    row);
  let start = null; // { x, y, id }
  let swiping = false;
  let dx = 0;
  let swiped = false; // the click that ends a swipe is not a tap

  const threshold = () => Math.min(140, el.clientWidth * 0.35);
  const chosen = () => (Math.abs(dx) > threshold() ? (dx < 0 ? left : right) : null);
  const slide = (x, animate) => {
    row.style.transition = animate ? 'transform 0.2s ease-out' : 'none';
    row.style.transform = x ? `translateX(${x}px)` : '';
  };
  const finish = () => {
    swiping = false;
    el.classList.remove('swiping', 'armed', 'to-left', 'to-right');
  };

  row.addEventListener('pointerdown', (e) => {
    if (e.pointerType !== 'touch' || !e.isPrimary) return;
    start = { x: e.clientX, y: e.clientY, id: e.pointerId };
    pressed = true;
    swiping = false;
    dx = 0;
  });
  row.addEventListener('pointermove', (e) => {
    if (!start || e.pointerId !== start.id) return;
    const mx = e.clientX - start.x;
    const my = e.clientY - start.y;
    if (!swiping) {
      // A scroll of the list, not a swipe.
      if (Math.abs(my) > START && Math.abs(my) >= Math.abs(mx)) {
        start = null;
        release();
        return;
      }
      if (Math.abs(mx) < START) return;
      swiping = true;
      try {
        row.setPointerCapture(e.pointerId);
      } catch { /* a pointer gone already */ }
      el.classList.add('swiping');
    }
    dx = (mx < 0 && !left) || (mx > 0 && !right) ? 0 : mx;
    slide(dx, false);
    el.classList.toggle('to-left', dx < 0);
    el.classList.toggle('to-right', dx > 0);
    el.classList.toggle('armed', !!chosen());
  });
  const end = (e, cancelled) => {
    if (!start || e.pointerId !== start.id) return;
    start = null;
    if (!swiping) {
      release();
      return;
    }
    swiped = true;
    setTimeout(() => { swiped = false; }, 50);
    const action = cancelled ? null : chosen();
    if (action?.away) {
      slide(Math.sign(dx) * el.clientWidth, true);
      setTimeout(() => {
        finish();
        pressed = false;
        deferred = null; // the action draws the list
        action.run();
      }, 200);
      return;
    }
    slide(0, true);
    finish();
    release();
    action?.run();
  };
  row.addEventListener('pointerup', (e) => end(e, false));
  row.addEventListener('pointercancel', (e) => end(e, true));
  el.addEventListener('click', (e) => {
    if (!swiped) return;
    e.preventDefault();
    e.stopPropagation();
  }, true);
  return el;
}

/**
 * Pulled past its top (the bounce of iOS), `list` runs `run` once let go.
 * `hint` is shown in the space the bounce opens.
 */
export function pullToRefresh(list, hint, run) {
  list.addEventListener('scroll', () => {
    const y = -list.scrollTop;
    hint.style.opacity = y > 0 ? String(Math.min(1, y / PULL)) : '';
    hint.classList.toggle('ready', y > PULL);
  }, { passive: true });
  list.addEventListener('touchend', () => {
    if (-list.scrollTop > PULL) run();
  }, { passive: true });
}

/**
 * A swipe from the left edge of the screen slides `el` away and runs `run`
 * (back, as in the apps of iOS; a home screen app has no such gesture).
 * Touch events: they keep coming while the page scrolls.
 */
export function edgeSwipe(el, run) {
  let start = null; // { x, y, dx }
  const slide = (x, animate) => {
    el.style.transition = animate ? 'transform 0.2s ease-out' : 'none';
    el.style.transform = x ? `translateX(${x}px)` : '';
  };
  el.addEventListener('touchstart', (e) => {
    const t = e.touches[0];
    start = e.touches.length === 1 && t.clientX < 28 ? { x: t.clientX, y: t.clientY, dx: 0 } : null;
  }, { passive: true });
  el.addEventListener('touchmove', (e) => {
    if (!start) return;
    const t = e.touches[0];
    const dx = t.clientX - start.x;
    if (Math.abs(t.clientY - start.y) > Math.max(START, dx)) {
      start = null;
      slide(0, true);
      return;
    }
    start.dx = Math.max(0, dx);
    slide(start.dx, false);
  }, { passive: true });
  el.addEventListener('touchend', () => {
    if (!start) return;
    const back = start.dx > Math.min(100, el.clientWidth * 0.3);
    start = null;
    if (!back) {
      slide(0, true);
      return;
    }
    slide(el.clientWidth, true);
    setTimeout(() => {
      run();
      slide(0, false);
    }, 200);
  });
  el.addEventListener('touchcancel', () => {
    start = null;
    slide(0, true);
  });
}
