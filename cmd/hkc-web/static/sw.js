const CACHE = 'hkc-shell-v4-plates';
const ASSETS = ['/', '/style.css', '/motion.css', '/logs.css', '/motion.js', '/app.js', '/calibration.svg', '/ticks.svg', '/motion-study.svg', '/fonts/archivo-black.ttf', '/fonts/source-serif-4.ttf', '/fonts/source-serif-4-semibold.ttf', '/fonts/space-mono.ttf', '/fonts/space-mono-bold.ttf', '/icon.svg', '/manifest.webmanifest'];
self.addEventListener('install', (event) => { event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll(ASSETS))); self.skipWaiting(); });
self.addEventListener('activate', (event) => { event.waitUntil(caches.keys().then((keys) => Promise.all(keys.filter((key) => key !== CACHE).map((key) => caches.delete(key))))); self.clients.claim(); });
self.addEventListener('fetch', (event) => {
  if (event.request.method !== 'GET' || new URL(event.request.url).pathname.startsWith('/api/')) return;
  if (!ASSETS.includes(new URL(event.request.url).pathname)) return;
  event.respondWith(fetch(event.request).then((response) => { if (response.ok) { const copy = response.clone(); caches.open(CACHE).then((cache) => cache.put(event.request, copy)); } return response; }).catch(() => caches.match(event.request)));
});
