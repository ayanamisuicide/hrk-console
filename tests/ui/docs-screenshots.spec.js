const { test, expect } = require("@playwright/test");
const { createJournalApi } = require("./journal-fixture");

// Скриншоты для README. Обычный прогон их пропускает; обновить картинки:
//   DOCS_SCREENSHOTS=1 npx playwright test tests/ui/docs-screenshots.spec.js
test.skip(!process.env.DOCS_SCREENSHOTS, "только по запросу: DOCS_SCREENSHOTS=1");

// Время записей без пояса, как у бота: фикстура читает его как местное.
const pad = (n) => String(n).padStart(2, "0");
const stamp = (ms) => {
  const d = new Date(ms);
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
};

// Журнал за последние пять часов: фоновые записи, всплеск предупреждений и ошибка с traceback.
function sampleLines(now) {
  const records = [];
  const add = (ago, level, module, message, trace = []) => {
    records.push({ at: now - ago * 1000, lines: [`${stamp(now - ago * 1000)} [${level}] ${module}: ${message}`, ...trace] });
  };
  for (let minutes = 290; minutes > 40; minutes -= 7) {
    add(minutes * 60, "INFO", ["Core", "loader", "weather", "reminders"][minutes % 4], ["сообщение обработано", "модуль отработал за 0.2 с", "кэш обновлён", "напоминание отправлено"][minutes % 4]);
  }
  for (let i = 0; i < 4; i++) add(3900 - i * 40, "WARNING", "net", "медленный ответ Telegram: 4.2 с");
  add(1500, "ERROR", "updates", "не удалось получить манифест обновлений", [
    "Traceback (most recent call last):",
    '  File "/opt/heroku/modules/updates.py", line 88, in fetch',
    "    async with session.get(MANIFEST_URL, timeout=10) as reply:",
    '  File "/opt/heroku/.venv/lib/python3.12/site-packages/aiohttp/client.py", line 1359, in __aenter__',
    "    self._resp = await self._coro",
    '  File "/opt/heroku/.venv/lib/python3.12/site-packages/aiohttp/client.py", line 690, in _request',
    "    await resp.start(conn)",
    "aiohttp.client_exceptions.ServerTimeoutError: Timeout on reading data from socket",
  ]);
  for (let minutes = 24; minutes > 0; minutes -= 6) add(minutes * 60, "INFO", "Core", "сообщение обработано");
  add(95, "WARNING", "weather", "ответ погодного сервиса задержался");
  add(20, "INFO", "Core", "модуль weather перезагружен");
  return records.sort((a, b) => a.at - b.at).flatMap((record) => record.lines);
}

test("скриншот: журнал", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 860 });
  const now = Date.now();
  const api = createJournalApi(sampleLines(now), { now });
  await page.route("**/api/**", async (route) => {
    const url = route.request().url();
    const path = new URL(url).pathname;
    if (path === "/api/events") return route.abort();
    if (path === "/api/logs/stream") return route.fulfill({ contentType: "text/event-stream", body: `event: state\ndata: {"alive":true}\n\n` });
    const journal = api.handle(url);
    if (journal !== undefined) return route.fulfill({ contentType: "application/json", body: JSON.stringify(journal ?? {}) });
    const data = path === "/api/auth/me" ? { username: "anya", role: "operator" } : path === "/api/status" ? { running: true, pid: 4242, uptime: "5ч 12м", logReady: true } : {};
    return route.fulfill({ contentType: "application/json", body: JSON.stringify(data) });
  });
  // Постоянное соединение с потоком на снимке: настоящее закрывается после ответа заглушки.
  await page.addInitScript(() => {
    window.EventSource = class {
      constructor() {
        setTimeout(() => this.onopen?.(), 30);
      }
      addEventListener() {}
      close() {}
    };
  });
  await page.goto("/");
  await page.locator('[data-view="logs"]').click();
  await expect(page.locator(".jr-card").first()).toBeVisible();
  await page.locator('#log .jr-card[data-level="ERROR"] summary').click();
  await page.waitForTimeout(600);
  await page.screenshot({ path: "docs/images/journal.png" });
});
