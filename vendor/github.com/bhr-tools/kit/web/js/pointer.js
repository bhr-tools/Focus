// Pointer gestures (from Calendar): click, drag (with a small threshold)
// and, on touch screens, a long press before dragging so that swipes still
// scroll. Holding Ctrl (or Alt / Cmd) during a drag turns a move into a copy;
// the key can be pressed or released at any time before the drop.

const THRESHOLD = 4; // px before a press becomes a drag
const LONG_PRESS = 350; // ms on touch screens

function preventScroll(e) {
  e.preventDefault();
}

/**
 * Follows a pointer from `down` (a pointerdown event).
 *   onStart(point)            the press became a drag
 *   onMove(point)             during the drag
 *   onEnd(point)              dropped
 *   onClick(point)            released without dragging
 *   onCancel()                interrupted (Escape, pointercancel)
 *   immediate                 touch drags at once, without the long press
 *                             (a drag handle, which does not scroll)
 * point: { x, y, copy, event }
 */
export function follow(down, { onStart, onMove, onEnd, onClick, onCancel, immediate = false }) {
  const touch = down.pointerType === 'touch' && !immediate;
  const origin = { x: down.clientX, y: down.clientY };
  let dragging = false;
  let armed = !touch; // touch: armed by the long press
  let copy = down.ctrlKey || down.altKey || down.metaKey;
  let last = { x: origin.x, y: origin.y };
  let timer = 0;
  const target = down.currentTarget || down.target;

  const point = (e) => ({ x: last.x, y: last.y, copy, event: e });

  const start = (e) => {
    dragging = true;
    try { target.setPointerCapture?.(down.pointerId); } catch { /* element gone */ }
    if (touch) document.addEventListener('touchmove', preventScroll, { passive: false });
    document.body.classList.add('is-dragging');
    onStart?.(point(e));
  };

  const cleanup = () => {
    clearTimeout(timer);
    window.removeEventListener('pointermove', move);
    window.removeEventListener('pointerup', up);
    window.removeEventListener('pointercancel', cancel);
    window.removeEventListener('keydown', key, true);
    window.removeEventListener('keyup', key, true);
    document.removeEventListener('touchmove', preventScroll);
    document.body.classList.remove('is-dragging');
  };

  function move(e) {
    if (e.pointerId !== down.pointerId) return;
    last = { x: e.clientX, y: e.clientY };
    copy = e.ctrlKey || e.altKey || e.metaKey;
    const dist = Math.hypot(last.x - origin.x, last.y - origin.y);
    if (!dragging) {
      if (!armed) {
        // A touch that moves before the long press is a scroll.
        if (dist > 8) {
          cleanup();
        }
        return;
      }
      if (dist < THRESHOLD) return;
      start(e);
    }
    onMove?.(point(e));
  }

  function up(e) {
    if (e.pointerId !== down.pointerId) return;
    cleanup();
    if (dragging) onEnd?.(point(e));
    else onClick?.(point(e));
  }

  function cancel() {
    cleanup();
    if (dragging) onCancel?.();
  }

  function key(e) {
    if (e.key === 'Escape' && dragging) {
      e.preventDefault();
      e.stopPropagation();
      cleanup();
      onCancel?.();
      return;
    }
    if (['Control', 'Alt', 'Meta'].includes(e.key)) {
      copy = e.ctrlKey || e.altKey || e.metaKey;
      if (dragging) onMove?.(point(e));
    }
  }

  if (touch) {
    timer = setTimeout(() => {
      armed = true;
      navigator.vibrate?.(10);
      start(down);
      onMove?.(point(down));
    }, LONG_PRESS);
  }
  window.addEventListener('pointermove', move);
  window.addEventListener('pointerup', up);
  window.addEventListener('pointercancel', cancel);
  window.addEventListener('keydown', key, true);
  window.addEventListener('keyup', key, true);
}
