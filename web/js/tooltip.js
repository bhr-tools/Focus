// A tooltip that follows the mouse (none on touch screens).

const canHover = window.matchMedia('(hover: hover) and (pointer: fine)').matches;
const $ = (id) => document.getElementById(id);

/** content: nodes or text, built by the caller (no HTML strings). */
export function attachTooltip(el, content) {
  if (!canHover) return;
  const tip = $('tooltip');
  const move = (e) => {
    const x = Math.min(e.clientX + 14, window.innerWidth - tip.offsetWidth - 8);
    const y = Math.min(e.clientY + 14, window.innerHeight - tip.offsetHeight - 8);
    tip.style.transform = `translate(${x}px, ${y}px)`;
  };
  el.addEventListener('mouseenter', (e) => {
    tip.replaceChildren(...[content].flat());
    tip.hidden = false;
    move(e);
  });
  el.addEventListener('mousemove', move);
  el.addEventListener('mouseleave', () => { tip.hidden = true; });
}

export function hideTooltip() {
  $('tooltip').hidden = true;
}
