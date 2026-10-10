const { test, expect } = require("@playwright/test");
// Ошибки JavaScript собираются отдельно для каждой страницы и проверяются после сценария.
const pageErrors = new WeakMap();

test.afterEach(async ({ page }) => {
  expect(pageErrors.get(page) || [], "Неожиданные ошибки браузера").toEqual([]);
});

// Английский текст ниже — входные данные журнала чужого бота, а не пояснения интерфейса.
const sampleLog = [
  "2026-10-03 10:00:00 [INFO] Core: started",
  "2026-10-03 10:00:01 [ERROR] Core: failed request",
  "2026-10-03 10:00:02 [ERROR] Core: retry failed",
];

const GiB = 1073741824;
// Пять минут истории с плавной нагрузкой, перезапуском и событиями ленты.
const historyStart = Date.parse("2026-10-03T09:55:00Z");
const systemHistory = {
  range: "5m0s",
  rangeSeconds: 300,
  intervalSeconds: 1,
  sampleCount: 300,
  now: "2026-10-03T10:00:00Z",
  points: Array.from({ length: 300 }, (_, index) => ({
    at: new Date(historyStart + index * 1000).toISOString(),
    cpu: 22 + 14 * Math.sin(index / 18) + (index % 37 === 0 ? 30 : 0),
    memory: 48 + index / 40,
    disk: 31,
    pid: index < 150 ? 1 : 2,
    botCpu: 2 + Math.abs(Math.sin(index / 9)) * 3,
    botRss: (180 + index / 10) * 1048576,
    rx: 4096 + 2048 * Math.sin(index / 7),
    tx: 1024,
    tgMs: index < 2 ? 0 : 80 + 10 * Math.sin(index / 20),
  })),
  restarts: ["2026-10-03T09:57:30Z"],
  events: [
    { event: "bot.stopped", severity: "critical", title: "Бот остановлен", message: "Процесс Heroku не найден.", time: "2026-10-03T09:57:29Z" },
    { event: "watchdog.recovered", severity: "warning", title: "Бот перезапущен автоматически", message: "", time: "2026-10-03T09:57:31Z" },
  ],
  thresholds: { cpu: 90, memory: 90, disk: 90 },
  stats: {
    cpu: { min: 8, avg: 22, max: 66, p95: 40 },
    memory: { min: 48, avg: 52, max: 55, p95: 55 },
    disk: { min: 31, avg: 31, max: 31, p95: 31 },
  },
};
const systemDetails = {
  summary: { level: "warn", title: "Есть на что посмотреть", items: [{ level: "warn", text: "Память бота растёт на 9.4 МБ в час — возможна утечка" }] },
  bot: { running: true, pid: 123, uptime: "1ч", cpuPercent: 2.5, rssBytes: 209715200, threads: 9, children: 1, openFiles: 24, rssTrendPerHour: 0, rssTrendFit: 0 },
  network: { rxRate: 5120, txRate: 1024, rxTotal: 73400320, txTotal: 4194304 },
  probes: [
    { name: "DC2 · Амстердам", address: "149.154.167.51:443", ok: true, latencyMs: 81 },
    { name: "DC5 · Сингапур", address: "91.108.56.130:443", ok: true, latencyMs: 229 },
    { name: "Bot API", address: "api.telegram.org:443", ok: false, latencyMs: 0, error: "нет ответа за 4 с" },
  ],
  disk: { root: "/srv/Heroku", totalBytes: 230686720, partial: false, scannedAt: "2026-10-03T09:59:00Z", entries: [
    { name: ".venv", bytes: 181403648, dir: true },
    { name: ".git", bytes: 42362880, dir: true },
    { name: "heroku.log", bytes: 7130317, dir: false },
  ] },
  inodesTotal: 1000000,
  inodesFree: 900000,
  forecast: { growthPerDay: 2147483648, daysToFull: 4.5, basisHours: 6, fit: 0.9 },
  wsl: { detected: true, configPath: "/mnt/c/Users/test/.wslconfig", memory: "8GB" },
  processes: [
    { pid: 123, name: "python3", cpuPercent: 2.5, rssBytes: 209715200, threads: 9 },
    { pid: 77, name: "hkc-web", cpuPercent: 0.4, rssBytes: 18874368, threads: 8 },
  ],
  processesHidden: false,
};

// Подменяем серверные ответы: проверки интерфейса не запускают и не останавливают настоящий процесс.
test.beforeEach(async ({ page }) => {
  const errors = [];
  pageErrors.set(page, errors);
  page.on("pageerror", (error) => errors.push(error.message));
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const response = {
      "/api/auth/me": { username: "tester", role: "operator" },
      "/api/version": { version: "v2.2.17" },
      "/api/status": {
        running: true,
        pid: 123,
        uptime: "1ч",
        version: "2.2.0",
        herokuDir: "/srv/Heroku",
        logReady: true,
      },
      "/api/logs": { lines: sampleLog },
      "/api/insights": { logCounts: { error: 2, warning: 0 } },
      "/api/metrics": { rssBytes: 104857600 },
      "/api/system": {
        supported: true,
        cpuPercent: 18.4,
        cpuCores: 8,
        load1: 0.62,
        load5: 0.48,
        load15: 0.41,
        memoryTotalBytes: 16 * GiB,
        memoryUsedBytes: 6.2 * GiB,
        memoryAvailableBytes: 9.8 * GiB,
        memoryCachedBytes: 3.1 * GiB,
        swapTotalBytes: 4 * GiB,
        swapUsedBytes: 0,
        diskTotalBytes: 512 * GiB,
        diskUsedBytes: 160 * GiB,
        diskFreeBytes: 352 * GiB,
        os: "linux",
        arch: "amd64",
        hostname: "vps-amsterdam",
        kernel: "6.8.0-45-generic",
        uptimeSeconds: 1209600,
        sampledAt: new Date().toISOString(),
      },
      "/api/system/history": systemHistory,
      "/api/system/details": systemDetails,
      "/api/incidents": {
        incidents: [
          {
            start: "2026-10-03 10:00:01",
            end: "2026-10-03 10:00:02",
            level: "ERROR",
            module: "Core",
            title: "failed request",
            count: 2,
            context: sampleLog[0],
          },
        ],
      },
      "/api/modules": {
        status: "live", message: "Состояние загрузки обновляется каждую секунду.", session: "test-run",
        modules: [
          { id: "core", name: "Loader", kind: "core", state: "ready", version: "2.1.0", error: "" },
          { id: "external", name: "Weather", kind: "external", state: "loading", version: "1.2.0", error: "" },
          { id: "broken", name: "Music", kind: "external", state: "error", version: "", error: "ImportError: missing dependency" },
        ],
      },
    };
    if (path === "/api/events") return route.abort();
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(response[path] || {}),
    });
  });
});

for (const width of [390, 1440])
  for (const theme of ["dark"]) {
    test(`экраны и взаимодействия ${width}px ${theme}`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize({ width, height: 900 });
      await page.goto("/");
      await expect(page.locator("#version")).toContainText("Панель v2.2.17");
      await expect(
        page.locator('[data-view="overview"], #overview-view'),
      ).toHaveCount(0);
      await expect(page.locator("#logs-view")).toBeVisible();
      for (const view of ["logs", "incidents", "modules", "system"]) {
        if (
          view === "incidents" &&
          (await page.locator("#journal-nav-toggle").isVisible()) &&
          (await page
            .locator("#journal-nav-toggle")
            .getAttribute("aria-expanded")) === "false"
        )
          await page.locator("#journal-nav-toggle").click();
        await page.locator(`[data-view="${view}"]`).click();
        await expect(page.locator(`#${view}-view`)).toBeVisible();
        if (view === "incidents")
          await expect(page.locator(".incident-card")).toBeVisible();
        await page.screenshot({
          path: testInfo.outputPath(`${view}-${width}-${theme}.png`),
          fullPage: true,
          animations: "disabled",
        });
      }
      await expect(page.locator("#history-chart canvas")).toBeVisible();
      await page.locator('[data-view="logs"]').click();
      await page.locator(".line-bookmark").first().click();
      await page.locator("#bookmarks-only").click();
      await expect(page.locator("#log .line")).toHaveCount(1);
      if (
        (await page.locator("#journal-nav-toggle").isVisible()) &&
        (await page
          .locator("#journal-nav-toggle")
          .getAttribute("aria-expanded")) === "false"
      )
        await page.locator("#journal-nav-toggle").click();
      await page.locator('[data-view="incidents"]').click();
      await page.locator(".incident-card").first().click();
      await expect(page.locator("#logs-view")).toBeVisible();
      await expect(page.locator("#time-from")).not.toHaveValue("");
      const overflow = await page.evaluate(
        () => document.documentElement.scrollWidth > innerWidth + 1,
      );
      expect(overflow).toBe(false);
    });
  }

test("проверка снимков основных разделов", async ({ page }) => {
  await page.clock.install({ time: new Date("2026-10-03T10:00:00Z") });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await expect(page.locator("#version")).toContainText("v2.2.17");
  for (const view of ["logs", "incidents", "system"]) {
    if (
      view === "incidents" &&
      (await page
        .locator("#journal-nav-toggle")
        .getAttribute("aria-expanded")) === "false"
    )
      await page.locator("#journal-nav-toggle").click();
    await page.locator(`[data-view="${view}"]`).click();
    await expect(page.locator(`#${view}-view`)).toBeVisible();
    if (view === "incidents")
      await expect(page.locator(".incident-card")).toBeVisible();
    await expect(page.locator(`#${view}-view`)).toHaveScreenshot(
      `${view}.png`,
      { animations: "disabled", maxDiffPixelRatio: 0.01 },
    );
  }
});

// Только этот сценарий использует настоящий service worker и временно отключает сеть.
test.describe("PWA", () => {
  test.use({ serviceWorkers: "allow" });
  test("загружает все зависимости модулей без сети", async ({
    page,
    context,
  }) => {
    await page.goto("/");
    await page.evaluate(() => navigator.serviceWorker.ready);
    await expect
      .poll(() =>
        page.evaluate(async () => {
          const assets = [
            "/app.js",
            "/live-chart.js",
            "/modules/journal.js",
            "/modules/auth.js",
            "/theme.js",
            "/css/base.css",
            "/modules/modules.js",
            "/css/console.css",
          ];
          return (
            await Promise.all(
              assets.map(async (url) => Boolean(await caches.match(url))),
            )
          ).every(Boolean);
        }),
      )
      .toBe(true);
    await expect
      .poll(() =>
        page.evaluate(() => Boolean(navigator.serviceWorker.controller)),
      )
      .toBe(true);
    try {
      await context.setOffline(true);
      await page.reload();
      await expect(page.locator("#logs-view")).toBeVisible();
      await page.locator("#journal-nav-toggle").click();
      await expect(page.locator("#journal-nav-toggle")).toHaveAttribute(
        "aria-expanded",
        "true",
      );
    } finally {
      await context.setOffline(false);
    }
  });
});

test("админка настраивает автоматическое восстановление", async ({ page }, testInfo) => {
  let settings = { enabled: true, timeoutSeconds: 180 };
  const oldRequests = [];
  await page.addInitScript(() => {
    sessionStorage.setItem("hkc-admin-token", "test-token");
    localStorage.setItem("hkc-admin-tree", JSON.stringify(["operations"]));
  });
  await page.route("**/api/admin/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const status = 200;
    let body =
      path === "/api/admin/overview"
        ? {
            users: [],
            invites: [],
            bot: { running: false, herokuDir: "/srv/Heroku" },
          }
        : path === "/api/admin/audit"
          ? { events: [] }
          : path === "/api/admin/backups"
            ? { backups: [] }
            : path === "/api/admin/config/history"
              ? { history: [] }
              : path === "/api/admin/security"
                ? { rateLimiter: {}, features: {} }
                : path === "/api/admin/watchdog"
                  ? { settings, status: { state: settings.enabled ? "waiting" : "disabled", message: "Процесс бота остановлен.", remainingSeconds: settings.enabled ? 120 : 0, attempts: 2, lastAttempt: "2026-10-10T09:00:00Z", lastResult: "Heroku запущен заново." } }
                  : {};
    if (path.includes("maintenance") || path.includes("schedules")) oldRequests.push(path);
    if (path === "/api/admin/watchdog" && request.method() === "PUT") {
      settings = request.postDataJSON();
      body = { settings, status: { state: settings.enabled ? "waiting" : "disabled", remainingSeconds: 0, attempts: 2, message: "Настройки сохранены." } };
    }
    return route.fulfill({
      status,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });
  await page.goto("/admin/");
  await expect(page.locator("#watchdog-enabled")).toBeChecked();
  await expect(page.locator("#watchdog-countdown")).toHaveText("120 с");
  await expect(page.locator('#maintenance-form, #schedule-form')).toHaveCount(0);
  await expect(page.locator('[data-bot-action="start"]')).toBeEnabled();
  await page.locator("#watchdog-timeout").fill("240");
  await page.locator("#admin-refresh").click();
  await expect(page.locator("#watchdog-timeout")).toHaveValue("240");
  await page.screenshot({ path: testInfo.outputPath("watchdog-desktop.png"), fullPage: true });
  await page.setViewportSize({ width: 390, height: 900 });
  await page.screenshot({ path: testInfo.outputPath("watchdog-mobile.png"), fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)).toBe(false);
  await page.locator("#watchdog-enabled").uncheck();
  await page.locator('#watchdog-form button[type="submit"]').click();
  await expect(page.locator("#watchdog-state")).toHaveText("Выключено");
  expect(settings).toEqual({ enabled: false, timeoutSeconds: 240, maxAttempts: 5 });
  expect(oldRequests).toEqual([]);
});

test("настройки проверяются предварительно без раскрытия значений", async ({
  page,
}) => {
  let savedPayload;
  await page.addInitScript(() => {
    sessionStorage.setItem("hkc-admin-token", "test-token");
    localStorage.setItem("hkc-admin-view", "settings");
  });
  await page.route("**/api/admin/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let body =
      path === "/api/admin/overview"
        ? { users: [], invites: [], bot: { running: false } }
        : path === "/api/admin/audit"
          ? { events: [] }
          : path === "/api/admin/backups"
            ? { backups: [] }
            : path === "/api/admin/config/history"
              ? { history: [] }
              : path === "/api/admin/security"
                ? { rateLimiter: {}, features: {} }
                : path === "/api/admin/watchdog"
                  ? { settings: { enabled: true, timeoutSeconds: 180 }, status: { state: "healthy" } }
                  : path === "/api/admin/config" && request.method() === "GET"
                      ? {
                          configured: {
                            api_id: true,
                            api_hash: true,
                            redis_uri: false,
                            db_uri: false,
                            app_name: true,
                          },
                        }
                      : path === "/api/admin/config/validate"
                        ? {
                            valid: true,
                            changes: [{ key: "api_hash", change: "changed" }],
                            restartRequired: true,
                          }
                        : {};
    if (path === "/api/admin/config" && request.method() === "PATCH") {
      savedPayload = request.postDataJSON();
      body = { ok: true, message: "saved" };
    }
    return route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });
  await page.goto("/admin/");
  await expect(page.locator("#config-status")).toContainText("API hash");
  await page
    .locator('[name="api_hash"]')
    .fill("0123456789abcdef0123456789abcdef");
  await page.locator("#config-preview").click();
  await expect(page.locator("#config-preview-output")).toContainText(
    "api_hash: будет изменён",
  );
  await expect(page.locator("body")).not.toContainText(
    "0123456789abcdef0123456789abcdef",
  );
  await page.locator('#config-form button[type="submit"]').click();
  await expect(page.locator("#confirm-dialog")).toBeVisible();
  await page.locator("#confirm-accept").click();
  await expect
    .poll(() => savedPayload)
    .toEqual({ api_hash: "0123456789abcdef0123456789abcdef" });
  await expect(page.locator('[name="api_hash"]')).toHaveValue("");
});

test("ресурсы обновляются каждую секунду", async ({ page }) => {
  const hits = { status: 0, system: 0, history: 0 };
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const key =
      path === "/api/system/history" ? "history" : path.slice("/api/".length);
    if (key in hits) hits[key]++;
    return route.fallback();
  });
  await page.clock.install({ time: new Date("2026-10-03T10:00:00Z") });
  await page.goto("/");
  await expect(page.locator("#status-label")).toContainText("бот запущен");
  await page.locator('[data-view="system"]').click();
  await expect(page.locator("#history-chart canvas")).toBeVisible();
  await page.locator("#history-chart canvas").evaluate((canvas) => {
    canvas.dataset.identity = "persistent";
  });
  const before = { ...hits };
  await page.clock.runFor(2200);
  await expect.poll(() => hits.history).toBeGreaterThan(before.history);
  for (const key of Object.keys(hits))
    expect(hits[key], key).toBeGreaterThan(before[key]);
  await expect(
    page.locator('#history-chart canvas[data-identity="persistent"]'),
  ).toHaveCount(1);
});

test("группа журнала раскрывается, сохраняется и открывает вложенный раздел", async ({
  page,
}) => {
  await page.goto("/");
  await expect(page.locator("#journal-nav-toggle")).toHaveAttribute(
    "aria-expanded",
    "false",
  );
  await page.locator("#journal-nav-toggle").click();
  await expect(page.locator("#journal-nav-toggle")).toHaveAttribute(
    "aria-expanded",
    "true",
  );
  await expect(page.locator(".nav-subitem")).toBeVisible();
  await page.locator(".nav-subitem").click();
  await expect(page.locator("#incidents-view")).toBeVisible();
  await expect(page.locator("#journal-nav")).toHaveClass(/section-active/);
  await page.reload();
  await expect(page.locator("#journal-nav-toggle")).toHaveAttribute(
    "aria-expanded",
    "true",
  );
});

test("админка показывает приостановку и настраивает уведомления", async ({ page }, testInfo) => {
  let alerts = { botState: true, watchdog: true, modules: true, cpuPercent: 90, memoryPercent: 90, diskPercent: 90, sustainSeconds: 300 };
  let resumed = false;
  let tested = false;
  await page.addInitScript(() => {
    sessionStorage.setItem("hkc-admin-token", "test-token");
    localStorage.setItem("hkc-admin-view", "operations");
  });
  await page.route("**/api/admin/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    let body = {};
    if (path === "/api/admin/overview") body = { users: [], invites: [], bot: { running: false } };
    if (path === "/api/admin/security") body = { rateLimiter: {}, features: {} };
    if (path === "/api/admin/watchdog/resume") resumed = true;
    if (path.startsWith("/api/admin/watchdog"))
      body = {
        settings: { enabled: true, timeoutSeconds: 60, maxAttempts: 3 },
        status: resumed
          ? { state: "waiting", remainingSeconds: 60, attempts: 3, streak: 0, message: "Ожидаем." }
          : { state: "suspended", attempts: 3, streak: 3, message: "Бот не заработал после 3 перезапусков подряд." },
      };
    if (path === "/api/admin/alerts") {
      if (request.method() === "PUT") alerts = request.postDataJSON();
      body = { channels: { telegram: true, webhook: false }, settings: alerts };
    }
    if (path === "/api/admin/alerts/test") {
      tested = true;
      body = { ok: true, message: "Пробное уведомление доставлено.", results: [{ channel: "telegram", ok: true }] };
    }
    return route.fulfill({ contentType: "application/json", body: JSON.stringify(body) });
  });
  await page.goto("/admin/");
  await expect(page.locator("#watchdog-state")).toHaveText("Приостановлено");
  await expect(page.locator("#watchdog-max-attempts")).toHaveValue("3");
  await page.screenshot({ path: testInfo.outputPath("alerts-desktop.png"), fullPage: true });
  await page.locator("#watchdog-resume").click();
  await expect(page.locator("#watchdog-suspended")).toBeHidden();
  await expect(page.locator('[data-channel="telegram"]')).toHaveAttribute("data-active", "true");
  await expect(page.locator('[data-channel="webhook"]')).toHaveAttribute("data-active", "false");
  await expect(page.locator("#alert-disk")).toHaveValue("90");
  await page.locator("#alert-disk").fill("85");
  await page.locator("#alert-modules").uncheck();
  await page.locator('#alerts-form button[type="submit"]').click();
  await expect.poll(() => alerts.diskPercent).toBe(85);
  expect(alerts).toEqual({ botState: true, watchdog: true, modules: false, cpuPercent: 90, memoryPercent: 90, diskPercent: 85, sustainSeconds: 300 });
  await page.locator("#alert-test").click();
  await expect.poll(() => tested).toBe(true);
  await page.setViewportSize({ width: 390, height: 900 });
  await page.screenshot({ path: testInfo.outputPath("alerts-mobile.png"), fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)).toBe(false);
});

test("вкладка системы: сводка, график, легенда, масштаб и подробности", async ({ page }, testInfo) => {
  await page.clock.install({ time: new Date("2026-10-03T10:00:00Z") });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/");
  await page.locator('[data-view="system"]').click();
  await expect(page.locator("#system-summary")).toHaveAttribute("data-level", "warn");
  await expect(page.locator("#system-summary-title")).toHaveText("Есть на что посмотреть");
  await expect(page.locator("#system-summary-items li")).toHaveText(["Память бота растёт на 9.4 МБ в час — возможна утечка"]);
  await expect(page.locator("#history-legend .legend-chip")).toHaveCount(3);
  await expect(page.locator('#history-legend [data-series="cpu"] small')).toContainText("макс 66%");
  // Скрытие серии — отжатая кнопка легенды.
  const cpuChip = page.locator('#history-legend [data-series="cpu"]');
  await cpuChip.click();
  await expect(cpuChip).toHaveAttribute("aria-pressed", "false");
  await cpuChip.click();
  await expect(cpuChip).toHaveAttribute("aria-pressed", "true");
  // Подсказка показывает значения всех серий в точке под курсором.
  const chart = page.locator("#history-chart canvas");
  const box = await chart.boundingBox();
  await page.mouse.move(box.x + box.width * 0.5, box.y + box.height * 0.4);
  await expect(page.locator(".live-chart-tooltip")).toBeVisible();
  await expect(page.locator(".live-chart-tooltip .live-chart-row")).not.toHaveCount(0);
  // Выделение мышью приближает участок, кнопка возвращает масштаб.
  await page.mouse.move(box.x + box.width * 0.3, box.y + box.height * 0.4);
  await page.mouse.down();
  await page.mouse.move(box.x + box.width * 0.6, box.y + box.height * 0.4, { steps: 5 });
  await page.mouse.up();
  await expect(page.locator("#history-zoom-reset")).toBeVisible();
  await page.locator("#history-zoom-reset").click();
  await expect(page.locator("#history-zoom-reset")).toBeHidden();
  // Виды графика меняют легенду и запоминаются.
  await page.locator('[data-history-view="network"]').click();
  await expect(page.locator("#history-legend .legend-chip span")).toHaveText(["Приём", "Отправка", "Задержка Telegram"]);
  expect(await page.evaluate(() => localStorage.getItem("hkc-history-view"))).toBe("network");
  await page.locator('[data-history-view="resources"]').click();
  // Подробности.
  await expect(page.locator("#bot-pid")).toHaveText("123");
  await expect(page.locator("#bot-rss")).toHaveText("200.0 МБ");
  await expect(page.locator("#probe-list li")).toHaveCount(3);
  await expect(page.locator('#probe-list li[data-level="bad"] strong')).toHaveText("нет ответа");
  await expect(page.locator("#network-state")).toHaveText("доступно 2 из 3");
  await expect(page.locator("#disk-list li")).toHaveCount(3);
  await expect(page.locator("#disk-forecast")).toContainText("через 4.5 дн");
  await expect(page.locator("#memory-wsl")).toContainText("память 8GB");
  await expect(page.locator("#process-rows tr")).toHaveCount(2);
  await expect(page.locator("#process-rows tr.is-bot td").first()).toHaveText("python3");
  await page.mouse.move(0, 0);
  await page.screenshot({ path: testInfo.outputPath("system-desktop.png"), fullPage: true });
  await page.setViewportSize({ width: 390, height: 900 });
  await page.screenshot({ path: testInfo.outputPath("system-mobile.png"), fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)).toBe(false);
});

test("наблюдатель не видит процессы хоста", async ({ page }) => {
  await page.route("**/api/system/details", (route) =>
    route.fulfill({ contentType: "application/json", body: JSON.stringify({ ...systemDetails, processes: [], processesHidden: true }) }),
  );
  await page.goto("/");
  await page.locator('[data-view="system"]').click();
  await expect(page.locator("#processes-hidden")).toBeVisible();
  await expect(page.locator(".process-table")).toBeHidden();
});

// Скриншоты для README на тестовых данных: README_SHOTS=1 npx playwright test -g "скриншоты для README".
test("скриншоты для README", async ({ page }) => {
  test.skip(!process.env.README_SHOTS, "только по запросу");
  await page.addInitScript(() => localStorage.setItem("hkc-theme", "dark"));
  await page.clock.install({ time: new Date("2026-10-03T10:00:00Z") });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("/");
  await page.locator('[data-view="system"]').click();
  await expect(page.locator("#system-summary")).toHaveAttribute("data-level", "warn");
  await page.waitForTimeout(400);
  await page.screenshot({ path: "docs/images/system.png" });
  await page.locator('[data-view="modules"]').click();
  await expect(page.locator(".module-row").first()).toBeVisible();
  await page.screenshot({ path: "docs/images/modules.png" });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.locator('[data-view="system"]').click();
  await page.waitForTimeout(400);
  await page.screenshot({ path: "docs/images/mobile.png" });
});

test("спарклайны прокручиваются плавно, как большой график", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await page.goto("/");
  await page.locator('[data-view="system"]').click();
  const spark = page.locator('[data-spark="cpu"]');
  await expect.poll(() => spark.evaluate((canvas) => canvas.width)).not.toBe(300);
  const frames = await spark.evaluate(async (canvas) => {
    const first = canvas.toDataURL();
    await new Promise((resolve) => setTimeout(resolve, 250));
    return [first, canvas.toDataURL()];
  });
  // Между опросами сервера кадр меняется: время непрерывно сдвигает кривую.
  expect(frames[0]).not.toBe(frames[1]);
});
