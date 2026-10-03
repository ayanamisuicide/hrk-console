const CACHE = 'hkc-shell-v16-site-audit';
const ASSETS = ['/', '/style.css', '/motion.css', '/logs.css', '/motion.js', '/app.js', '/icon.svg', '/manifest.webmanifest'];
self.addEventListener('install', (event) => { event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll(ASSETS))); self.skipWaiting(); });
self.addEventListener('activate', (event) => { event.waitUntil(caches.keys().then((keys) => Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key))))); self.clients.claim(); });
self.addEventListener('fetch', (event) => {
  if (event.request.method !== 'GET' || new URL(event.request.url).pathname.startsWith('/api/')) return;
  if (!ASSETS.includes(new URL(event.request.url).pathname)) return;
  event.respondWith(fetch(event.request).then((response) => { if (response.ok) { const copy = response.clone(); caches.open(CACHE).then((cache) => cache.put(event.request, copy)); } return response; }).catch(() => caches.match(event.request)));
});
