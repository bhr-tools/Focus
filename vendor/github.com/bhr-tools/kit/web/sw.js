// Service worker: keeps the files of the web app so that it starts without
// network. The server prepends VERSION and FILES (kit/server,
// serviceWorker); VERSION changes with any file, which replaces the whole
// cache at once, so a page never mixes two releases.
const CACHE = `app-${VERSION}`;

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(CACHE)
    .then((cache) => cache.addAll(FILES))
    .then(() => self.skipWaiting()));
});

self.addEventListener('activate', (event) => {
  event.waitUntil(caches.keys()
    .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
    .then(() => self.clients.claim()));
});

self.addEventListener('fetch', (event) => {
  const req = event.request;
  const url = new URL(req.url);
  if (req.method !== 'GET' || url.origin !== self.location.origin) return;
  if (url.pathname.startsWith('/api/') || url.pathname === '/sw.js' || url.pathname === '/healthz') return;
  // Every page is the same single-page app.
  const key = req.mode === 'navigate' ? '/' : url.pathname;
  event.respondWith(caches.open(CACHE)
    .then((cache) => cache.match(key))
    .then((hit) => hit || fetch(req)));
});
