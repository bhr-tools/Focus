// The bottom tab bar of phones (hidden on a desktop, where the app has its
// own sidebar or tabs). Tabs show an icon and a label (Meals) or only text
// (Inventory: class "text" on the bar). Styles: css/nav.css.
//
//   const bar = tabBar(document.getElementById('tabbar'), [
//     { id: 'plan', label: 'Plan', icon: 'calendar' }, …
//   ], { onSelect: (id) => app.go(id) });
//   bar.setActive('plan'); bar.setBadge('shopping', 3);

import { h, icon, clear } from './dom.js';

export function tabBar(nav, tabs, { onSelect, text = false }) {
  nav.classList.add('tabbar');
  nav.classList.toggle('text', text);
  nav.setAttribute('aria-label', 'Navigation');
  const buttons = new Map();
  clear(nav, tabs.map((tab) => {
    const badge = h('span', { class: 'badge' });
    const b = h('button', { type: 'button', class: 'tab', dataset: { tab: tab.id }, onclick: () => onSelect(tab.id) },
      text ? null : h('span', { class: 'tab-icon' }, tab.icon ? icon(tab.icon) : null, badge),
      h('span', { class: 'tab-label' }, tab.label, text ? badge : null));
    buttons.set(tab.id, { b, badge });
    return b;
  }));
  return {
    setActive(id) {
      for (const [key, { b }] of buttons) {
        b.classList.toggle('active', key === id);
        if (key === id) b.setAttribute('aria-current', 'page');
        else b.removeAttribute('aria-current');
      }
    },
    /** A count on a tab (0 or empty hides it); alert colours it. */
    setBadge(id, n, { alert = false } = {}) {
      const t = buttons.get(id);
      if (!t) return;
      t.badge.textContent = n ? String(n) : '';
      t.badge.classList.toggle('alert', alert);
    },
  };
}
