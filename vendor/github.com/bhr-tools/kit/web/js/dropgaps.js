// Where a dragged row lands, the way Todoist does it: a single
// line in a gap between two rows. The vertical position picks the gap; how
// far the dragged item is pulled right or left picks the nesting depth,
// within the depths that make sense in that gap. Pure functions on row
// geometry, so they are tested without a browser (tests/js/dropgaps.test.mjs).
//
// rows: the visible rows of one list, top to bottom, without the dragged item
// and its descendants: [{ id, depth, top, bottom }].

/**
 * The gaps of a list: before the first row, between rows, after the last
 * row. Each gap has the y of its line and the depths allowed there: at most
 * one level under the row above, at least the depth of the row below (which
 * would otherwise change parent). An empty list has one gap at emptyY.
 */
export function listGaps(rows, emptyY) {
  if (!rows.length) return [{ index: 0, y: emptyY, min: 0, max: 0 }];
  const gaps = [];
  for (let i = 0; i <= rows.length; i++) {
    const above = rows[i - 1];
    const below = rows[i];
    const y = !above ? below.top : !below ? above.bottom : (above.bottom + below.top) / 2;
    gaps.push({ index: i, y, min: below ? below.depth : 0, max: above ? above.depth + 1 : 0 });
  }
  return gaps;
}

/** The gap whose line is closest to y. */
export function nearestGap(gaps, y) {
  let best = null;
  for (const g of gaps) {
    if (!best || Math.abs(g.y - y) < Math.abs(best.y - y)) best = g;
  }
  return best;
}

/**
 * Depth for a raw (fractional) depth, clamped to the gap. The current depth
 * is kept until the item is pulled well past the middle of the next level,
 * so the line does not flicker at the boundary.
 */
export function pickDepth(raw, gap, current = null) {
  let depth = Math.round(raw);
  if (current !== null && Math.abs(raw - current) < 0.65) depth = current;
  return Math.max(gap.min, Math.min(gap.max, depth));
}

/**
 * Turns a gap and a depth into a move: the new parent (null at depth 0) and
 * the sibling to go after, or before when the item becomes the first child.
 * Neither means "the only child" (or the only item of an empty list).
 */
export function resolveDrop(rows, index, depth) {
  let parentId = null;
  let afterId = null;
  for (let i = index - 1; i >= 0; i--) {
    const r = rows[i];
    if (r.depth === depth && afterId === null) afterId = r.id;
    if (r.depth < depth) {
      parentId = r.id;
      break;
    }
  }
  if (afterId) return { parentId, afterId };
  const next = rows[index];
  return next && next.depth === depth ? { parentId, beforeId: next.id } : { parentId };
}
