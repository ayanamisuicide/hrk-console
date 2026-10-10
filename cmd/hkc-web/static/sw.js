// Версию меняют вместе с ресурсами оболочки, чтобы старые зависимости не пережили обновление.
const CACHE = "hkc-shell-v24-watchdog";
// Явный список включает всю цепочку ES-модулей и CSS-импортов пользовательской панели.
// Административные страницы и ответы API здесь не сохраняются.
const ASSETS = [
  "/",
  "/style.css",
  "/motion.css",
  "/logs.css",
  "/motion.js",
  "/history-chart.js",
  "/app.js",
  "/icon.svg",
  "/manifest.webmanifest",
  "/styles/controls.css",
  "/styles/diagnostics.css",
  "/styles/dialogs.css",
  "/styles/foundation.css",
  "/styles/interactions.css",
  "/styles/journal-controls.css",
  "/styles/journal-frame.css",
  "/styles/log-controls.css",
  "/styles/log-lines.css",
  "/styles/log-motion.css",
  "/styles/log-responsive.css",
  "/styles/navigation.css",
  "/styles/shell.css",
  "/styles/system.css",
  "/styles/workspace.css",
  "/modules/auth.js",
  "/modules/incidents.js",
  "/modules/journal.js",
  "/modules/navigation.js",
  "/modules/service.js",
  "/modules/system.js",
  "/modules/modules.js",
  "/styles/modules.css",
];
// Установка кеширует весь набор целиком: неполная цепочка модулей не считается готовой.
self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(CACHE).then((cache) => cache.addAll(ASSETS)));
  self.skipWaiting();
});
// Удаляем старые кеши этой оболочки и берём открытые страницы под управление.
self.addEventListener("activate", (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) =>
        Promise.all(
          keys.filter((key) => key !== CACHE).map((key) => caches.delete(key)),
        ),
      ),
  );
  self.clients.claim();
});
// Для перечисленной статики сначала пробуем сеть; кеш нужен лишь при сетевой ошибке.
// Ошибка HTTP возвращается как есть, а не заменяется скрыто устаревшим ответом.
self.addEventListener("fetch", (event) => {
  if (
    event.request.method !== "GET" ||
    new URL(event.request.url).pathname.startsWith("/api/")
  )
    return;
  if (!ASSETS.includes(new URL(event.request.url).pathname)) return;
  event.respondWith(
    fetch(event.request)
      .then((response) => {
        if (response.ok) {
          const copy = response.clone();
          caches.open(CACHE).then((cache) => cache.put(event.request, copy));
        }
        return response;
      })
      .catch(() => caches.match(event.request)),
  );
});
