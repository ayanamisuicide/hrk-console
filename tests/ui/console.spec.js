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
        cpuPercent: 20,
        cpuCores: 8,
        memoryTotalBytes: 1000,
        memoryUsedBytes: 500,
        memoryAvailableBytes: 500,
        diskTotalBytes: 1000,
        diskUsedBytes: 300,
        diskFreeBytes: 700,
        os: "linux",
        arch: "amd64",
        hostname: "test",
        kernel: "test",
        uptimeSeconds: 1000,
        sampledAt: new Date().toISOString(),
      },
      "/api/system/history": {
        points: [
          { at: "2026-10-03T10:00:00Z", cpu: 20, memory: 50, disk: 30, pid: 1 },
          { at: "2026-10-03T10:00:30Z", cpu: 40, memory: 55, disk: 30, pid: 2 },
        ],
      },
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
  for (const theme of ["dark", "light"]) {
    test(`экраны и взаимодействия ${width}px ${theme}`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize({ width, height: 900 });
      await page.addInitScript(
        (selectedTheme) => localStorage.setItem("hkc-theme", selectedTheme),
        theme,
      );
      await page.goto("/");
      await expect(page.locator("#version")).toContainText("Панель v2.2.17");
      await expect(
        page.locator('[data-view="overview"], #overview-view'),
      ).toHaveCount(0);
      await expect(page.locator("#logs-view")).toBeVisible();
      for (const view of ["logs", "incidents", "modules", "system"]) {
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
        await page.screenshot({
          path: testInfo.outputPath(`${view}-${width}-${theme}.png`),
          fullPage: true,
          animations: "disabled",
        });
      }
      await expect(page.locator("#history-chart svg")).toBeVisible();
      await page.locator('[data-view="logs"]').click();
      await page.locator(".line-bookmark").first().click();
      await page.locator("#bookmarks-only").click();
      await expect(page.locator("#log .line")).toHaveCount(1);
      if (
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
            "/history-chart.js",
            "/modules/journal.js",
            "/modules/auth.js",
            "/styles/foundation.css",
            "/styles/navigation.css",
            "/modules/modules.js",
            "/styles/modules.css",
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
  expect(settings).toEqual({ enabled: false, timeoutSeconds: 240 });
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
  await expect(page.locator("#history-chart svg")).toBeVisible();
  await page.locator("#history-chart svg").evaluate((svg) => {
    svg.dataset.identity = "persistent";
  });
  const before = { ...hits };
  await page.clock.runFor(2200);
  await expect.poll(() => hits.history).toBeGreaterThan(before.history);
  for (const key of Object.keys(hits))
    expect(hits[key], key).toBeGreaterThan(before[key]);
  await expect(
    page.locator('#history-chart svg[data-identity="persistent"]'),
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
