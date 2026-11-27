// DOM helpers. Every element is built with h(): user data is always set as
// text, never parsed as HTML.

import { Icons } from './icons.js';

/**
 * Creates an element.
 *   h('button', { class: 'btn', onclick: fn, dataset: { id: 1 } }, 'Label', child)
 * Keys starting with "on" add event listeners; "class", "dataset", "style"
 * and "attrs" are handled specially; anything else is set as a property.
 * "style" accepts custom properties (--name) too.
 */
export function h(tag, props = {}, ...children) {
  const el = document.createElement(tag);
  for (const [key, value] of Object.entries(props || {})) {
    if (value === undefined || value === null || value === false) continue;
    if (key === 'class') el.className = Array.isArray(value) ? value.filter(Boolean).join(' ') : value;
    else if (key === 'dataset') Object.assign(el.dataset, value);
    else if (key === 'style') {
      for (const [prop, v] of Object.entries(value)) {
        if (prop.startsWith('--')) el.style.setProperty(prop, v);
        else el.style[prop] = v;
      }
    }
    else if (key === 'attrs') for (const [a, v] of Object.entries(value)) el.setAttribute(a, v);
    else if (key.startsWith('on')) el.addEventListener(key.slice(2), value);
    else el[key] = value;
  }
  append(el, children);
  return el;
}

function append(el, children) {
  for (const child of children) {
    if (child === null || child === undefined || child === false) continue;
    if (Array.isArray(child)) append(el, child);
    else el.append(child instanceof Node ? child : String(child));
  }
}

const icons = { ...Icons };
const iconCache = new Map();

/** Adds an app's icons (name → trusted SVG markup) to the kit's own. */
export function registerIcons(map) {
  Object.assign(icons, map);
  for (const name of Object.keys(map)) iconCache.delete(name);
}

/** Returns a fresh copy of a registered SVG icon wrapped in a span. */
export function icon(name, className = '') {
  let tpl = iconCache.get(name);
  if (!tpl) {
    tpl = document.createElement('template');
    tpl.innerHTML = icons[name] || ''; // trusted constant markup
    iconCache.set(name, tpl);
  }
  const span = document.createElement('span');
  span.className = `icon icon-${name} ${className}`.trim();
  span.append(tpl.content.cloneNode(true));
  return span;
}

/** Empties an element, then appends the given children. */
export function clear(el, ...children) {
  el.replaceChildren();
  append(el, children);
  return el;
}

// Same breakpoint as the phone rules of base.css.
const mobileQuery = typeof window !== 'undefined' ? window.matchMedia('(max-width: 768px)') : null;

/** True on phone-sized screens, where dialogs become pages and popovers sheets. */
export function isMobile() {
  return !!mobileQuery?.matches;
}

export function isTextInput(el) {
  return !!el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || !!el.isContentEditable);
}

/** Grows a textarea to fit its content. */
export function autoGrow(textarea, minHeight = 0) {
  textarea.style.height = 'auto';
  // scrollHeight leaves out the borders, which a border-box height includes:
  // without them the last line hides behind a scroll bar.
  const borders = textarea.offsetHeight - textarea.clientHeight;
  textarea.style.height = `${Math.max(minHeight, textarea.scrollHeight + borders)}px`;
}

/** Calls fn once no call has come for `ms` milliseconds. */
export function debounce(fn, ms) {
  let timer = 0;
  return (...args) => {
    clearTimeout(timer);
    timer = setTimeout(() => fn(...args), ms);
  };
}
