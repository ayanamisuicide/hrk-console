const { test, expect } = require("@playwright/test");
const path = require("path");
const { createJournalApi } = require("./journal-fixture");

// Мини-приложение — сама панель по адресу /tg/ со входом через Telegram. Проверяем на подменённом
// API: вход, те же разделы, подтверждение действий, сводку, «Управление» и отправку записи в Telegram.
const json = (route, body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
const INDEX = path.join(__dirname, "..", "..", "cmd", "hkc-web", "static", "index.html");
const LAUNCH = "/tg/#tgWebAppData=query_id%3DAAE%26user%3D%257B%2522id%2522%253A1%257D%26hash%3Dx";
const GiB = 1073741824;

const sampleLog = [
  "2026-10-03 10:00:00 [INFO] Core: started",
  "2026-10-03 10:00:01 [ERROR] weather: TimeoutError: api не ответил",
  "Traceback (most recent call last):",
  '  File "weather.py", line 3, in run',
  "2026-10-03 10:00:02 [WARNING] weather: slow response",
];

function digest(extra = {}) {
  return {
    label: "сутки",
    calm: false,
    running: true,
    uptime: "3ч 12м",
    crashes: 1,
    stops: [{ at: "2026-10-03T07:00:00Z", when: "вчера 10:00", crash: true, reason: "ImportError: cannot import name x", downSeconds: 180 }],
    restarts: [{ at: "2026-10-03T08:00:00Z", when: "11:00", who: "@anya из мини-приложения Telegram" }],
    recovers: 1,
    errors: 3,
    warnings: 1,
    prevErrors: 1,
    problems: [{ count: 2, message: "TimeoutError: api не ответил", module: "weather", level: "ERROR", query: "TimeoutError: api не ответил", when: "10:00" }],
    load: { peakCpu: 87, peakWhen: "03:12", maxMemory: 64, disk: 41, partial: false },
    modules: { live: true, ready: 12, failed: ["Weather"] },
    news: ["Вышла hrk-console v2.26.0"],
    updateTo: "v2.26.0",
    schedule: { enabled: true, at: "08:00" },
    telegram: true,
    ...extra,
  };
}

function telegramPage(extra = {}) {
  return {
    token: true,
    chat: true,
    admins: ["1"],
    webApp: "https://vps.example.ts.net/tg/",
    state: "running",
    lastPollAt: new Date().toISOString(),
    bot: { id: "42", username: "hrk_panel_bot", name: "HRK Panel" },
    settings: { controlEnabled: true, confirm: true },
    notifications: true,
    commands: [
      { name: "status", description: "Состояние бота и сервера", confirm: false },
      { name: "stop", description: "Остановить бота", action: "stop", confirm: true },
    ],
    unknown: [],
    activity: [{ time: "2026-10-10T09:12:00Z", actor: "telegram:@anya", action: "bot.restart", detail: "бот перезапущен", ip: "10.0.0.2" }],
    digest: { enabled: true, at: "08:00" },
    viewer: { telegram: true, name: "@anya" },
    mute: { available: true, active: false },
    watchdog: { available: true, enabled: true, state: "healthy", message: "Бот работает, основной цикл Python отвечает." },
    update: { available: true, installed: "v2.25.0", latest: "v2.26.0", note: "Сводка и мини-приложение" },
    ...extra,
  };
}

async function mockMiniApp(page, { forbidden = false, state = { disabled: false } } = {}) {
  // Мост Telegram в тестах недоступен: приложение берёт данные запуска из адреса.
  await page.route("https://telegram.org/**", (route) => route.abort());
  // Сервер отдаёт по /tg/ ту же страницу панели; статический сервер тестов её не знает.
  await page.route(/\/tg\/$/, (route) => route.fulfill({ path: INDEX, contentType: "text/html; charset=utf-8" }));
  const calls = { actions: [], posts: [], unauthorized: [], sessions: 0 };
  let running = true;
  let control = telegramPage();
  const journalApi = createJournalApi(sampleLog);
  await page.route("**/api/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const p = url.pathname;
    if (p === "/api/tg/session") {
      calls.sessions++;
      if (forbidden) return json(route, { message: "нет доступа", id: "777" }, 403);
      if (state.disabled) return json(route, { message: "управление из Telegram выключено в админке" }, 503);
      return json(route, { token: "session", user: { name: "Аня", username: "anya" } });
    }
    if (request.headers().authorization !== "Bearer session" || state.disabled) {
      calls.unauthorized.push(p);
      return json(route, { message: "требуется вход" }, 401);
    }
    if (request.method() !== "GET") calls.posts.push({ path: p, body: request.postDataJSON?.() ?? null });
    if (p === "/api/logs/stream") return route.abort();
    if (p.startsWith("/api/bot/")) {
      const action = p.split("/").pop();
      calls.actions.push(action);
      running = action !== "stop";
      return json(route, { ok: true, message: action === "stop" ? "бот остановлен" : "бот перезапущен" });
    }
    if (p === "/api/logs/share") return json(route, { ok: true, message: "запись отправлена в чат с ботом" });
    const journal = journalApi.handle(request.url(), { method: request.method(), body: request.postData() });
    if (journal !== undefined) return json(route, journal ?? {}, journal === null ? 404 : 200);
    if (p === "/api/telegram/mute") {
      const body = request.postDataJSON();
      control = { ...control, mute: body.off ? { available: true, active: false } : { available: true, active: true, all: body.all, until: new Date(Date.now() + 3600000).toISOString(), untilText: "13:00" } };
      return json(route, control);
    }
    if (p === "/api/telegram/watchdog") {
      control = { ...control, watchdog: { ...control.watchdog, enabled: request.postDataJSON().enabled } };
      return json(route, control);
    }
    if (p === "/api/telegram/update") return json(route, { ok: true, message: "обновление запущено" }, 202);
    if (p === "/api/telegram") return json(route, control);
    const responses = {
      "/api/auth/me": { username: "Аня", role: "operator", share: true, telegram: { id: "1", username: "anya" } },
      "/api/version": { version: "v2.25.0" },
      "/api/status": running
        ? { running: true, pid: 4821, uptime: "34м", version: "1.7.2", herokuDir: "/srv/Heroku", logReady: true }
        : {
            running: false,
            pid: 0,
            uptime: "—",
            version: "1.7.2",
            herokuDir: "/srv/Heroku",
            logReady: true,
            lastStop: {
              at: new Date().toISOString(),
              title: "Бот упал",
              severity: "critical",
              message: "ImportError: cannot import name x\nв модуле weather · weather.py:3, в run",
              trace: ["Traceback (most recent call last):", '  File "weather.py", line 3, in run', "ImportError: cannot import name x"],
            },
          },
      "/api/system": {
        supported: true, cpuPercent: 18.4, cpuCores: 2, load1: 0.42, load5: 0.4, load15: 0.3,
        memoryTotalBytes: 4 * GiB, memoryUsedBytes: 2 * GiB, memoryAvailableBytes: 2 * GiB,
        diskTotalBytes: 40 * GiB, diskUsedBytes: 25 * GiB, diskFreeBytes: 15 * GiB,
        os: "linux", arch: "amd64", hostname: "vps-1", kernel: "6.8.0", uptimeSeconds: 864000, sampledAt: new Date().toISOString(),
      },
      "/api/system/history": { points: [] },
      "/api/system/details": { processes: [], processesHidden: true },
      "/api/incidents": { incidents: [] },
      "/api/digest": digest(),
      "/api/modules": {
        status: "live",
        message: "Состояние загрузки обновляется каждую секунду.",
        modules: [
          { id: "core", name: "Loader", kind: "core", state: "ready", version: "2.1.0", error: "" },
          { id: "broken", name: "Weather", kind: "external", state: "error", version: "1.2", error: "TimeoutError" },
        ],
      },
    };
    return json(route, responses[p] || {});
  });
  return calls;
}

test("мини-приложение — это панель: вход через Telegram, те же разделы и подтверждение остановки", async ({ page }, testInfo) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.setViewportSize({ width: 390, height: 844 });
  const calls = await mockMiniApp(page);
  await page.goto(LAUNCH);
  await expect(page.locator("html")).toHaveAttribute("data-shell", "telegram");
  await expect(page.locator("#status-label")).toHaveText("бот запущен");
  // Открывается на «Системе» с теми же карточками, что в панели.
  await expect(page.locator("#system-view")).toBeVisible();
  await expect(page.locator(".resource-card")).toHaveCount(4);
  // Вместо админки — «Управление»; выхода и переключателя темы нет: вход и тема — от Telegram.
  await expect(page.locator('[data-view="control"]')).toBeVisible();
  await expect(page.locator("#admin-link")).toBeHidden();
  await expect(page.locator("#logout")).toBeHidden();
  await expect(page.locator("#theme-toggle")).toBeHidden();
  await expect(page.locator("#tg-gate")).not.toHaveAttribute("open", "");
  await page.screenshot({ path: testInfo.outputPath("miniapp-system.png"), fullPage: true });

  // Остановка спрашивает подтверждение: так настроено для Telegram.
  await page.locator('[data-action="stop"]').click();
  await expect(page.locator("#confirm-title")).toHaveText("Остановить бота?");
  await page.locator('#confirm-dialog button[value="cancel"]').click();
  expect(calls.actions).toEqual([]);
  await page.locator('[data-action="stop"]').click();
  await page.locator("#confirm-accept").click();
  await expect.poll(() => calls.actions).toEqual(["stop"]);
  await expect(page.locator("#status-label")).toHaveText("бот остановлен");

  // Почему бот не работает: причина и traceback на «Системе».
  await expect(page.locator("#stop-reason")).toBeVisible();
  await expect(page.locator("#stop-title")).toHaveText("Бот упал");
  await expect(page.locator("#stop-message")).toContainText("ImportError: cannot import name x");
  await expect(page.locator("#stop-trace")).toBeHidden();
  await page.locator("#stop-trace-toggle").click();
  await expect(page.locator("#stop-trace")).toContainText('File "weather.py", line 3');
  await page.screenshot({ path: testInfo.outputPath("miniapp-stopped.png"), fullPage: true });

  // Разделы панели работают и здесь.
  await page.locator('.side-nav [data-view="modules"]').click();
  await expect(page.locator("#modules-view")).toBeVisible();
  await expect(page.locator(".mods-card, [data-module-id], .mods-grid > *").first()).toBeVisible();
  await page.locator('.side-nav [data-view="logs"]').click();
  await expect(page.locator("#logs-view")).toBeVisible();
  await expect(page.locator(".jr-card").first()).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("miniapp-logs.png") });
  expect(calls.unauthorized).toEqual([]);
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)).toBe(false);
  expect(errors).toEqual([]);
});

test("сводка: падения с причинами, ошибки и переход в журнал", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await mockMiniApp(page);
  await page.goto(LAUNCH);
  await page.locator('.side-nav [data-view="digest"]').click();
  await expect(page.locator(".digest-summary strong")).toHaveText("Бот падал 1 раз, ошибок 3");
  await expect(page.locator(".digest-kpi").nth(0).locator("strong")).toHaveText("1");
  await expect(page.locator(".digest-kpi").nth(2)).toContainText("до этого 1");
  await expect(page.locator(".digest-row.bad").first()).toContainText("ImportError: cannot import name x");
  await expect(page.locator(".digest-row.bad").first()).toContainText("простоял 3 мин");
  await expect(page.locator(".digest-news")).toContainText("Вышла hrk-console v2.26.0");
  await expect(page.locator(".digest-note")).toContainText("в 08:00");
  await page.screenshot({ path: testInfo.outputPath("miniapp-digest.png"), fullPage: true });
  // Период меняется кнопками.
  const requested = page.waitForRequest((request) => request.url().includes("/api/digest?span=168h"));
  await page.locator('[data-digest-span="168h"]').click();
  await requested;
  // Частая ошибка ведёт в журнал с поиском по ней.
  await page.locator(".digest-card", { hasText: "Частые ошибки" }).locator(".digest-row").first().click();
  await expect(page.locator("#logs-view")).toBeVisible();
  await expect(page.locator("#filter")).toHaveValue("TimeoutError: api не ответил");
});

test("управление в мини-приложении — та же страница Telegram, что в админке", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  const calls = await mockMiniApp(page);
  await page.goto(LAUNCH);
  await page.locator('.side-nav [data-view="control"]').click();
  await expect(page.locator("#tg-title")).toHaveText("@hrk_panel_bot на связи");
  // Главный переключатель здесь только показывает состояние.
  await expect(page.locator("#tg-control")).toBeChecked();
  await expect(page.locator("#tg-control")).toBeDisabled();
  await expect(page.locator("#tg-master-note")).toBeVisible();
  const headings = await page.locator("#control-page h2").allTextContents();
  await page.screenshot({ path: testInfo.outputPath("miniapp-control.png"), fullPage: true });

  await page.locator('[data-mute="1h"]').click();
  await expect.poll(() => calls.posts.at(-1)).toEqual({ path: "/api/telegram/mute", body: { for: "1h", all: false } });
  await expect(page.locator("#tg-mute-state")).toHaveText("до 13:00");
  await expect(page.locator("#tg-unmute")).toBeVisible();
  await page.locator("#tg-unmute").click();
  await expect.poll(() => calls.posts.at(-1)?.body).toEqual({ off: true });

  await page.locator("#tg-watchdog").uncheck();
  await expect(page.locator("#confirm-title")).toHaveText("Выключить автовосстановление?");
  await page.locator("#confirm-accept").click();
  await expect.poll(() => calls.posts.at(-1)).toEqual({ path: "/api/telegram/watchdog", body: { enabled: false } });

  await expect(page.locator("#tg-update")).toHaveText("Обновить до v2.26.0");
  await page.locator("#tg-update").click();
  await page.locator("#confirm-accept").click();
  await expect.poll(() => calls.posts.at(-1)).toEqual({ path: "/api/telegram/update", body: { version: "v2.26.0" } });

  // Админка показывает ту же страницу: те же карточки в том же порядке.
  await page.addInitScript(() => {
    sessionStorage.setItem("hkc-admin-token", "test-token");
    localStorage.setItem("hkc-admin-view", "telegram");
  });
  await page.route("**/api/admin/**", (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/admin/telegram") return json(route, telegramPage({ viewer: { telegram: false, name: "admin" } }));
    return json(route, { users: [], invites: [], bot: { running: true, pid: 1 }, events: [], backups: [], settings: { enabled: true, timeoutSeconds: 180 }, status: { state: "healthy" } });
  });
  await page.goto("/admin/");
  await expect(page.locator("#tg-title")).toHaveText("@hrk_panel_bot на связи");
  expect(await page.locator("#admin-telegram h2").allTextContents()).toEqual(headings);
  await expect(page.locator("#tg-control")).toBeEnabled();
  await expect(page.locator("#tg-master-note")).toBeHidden();
});

test("запись журнала уходит в Telegram из её карточки", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  const calls = await mockMiniApp(page);
  await page.goto(LAUNCH);
  await page.locator('.side-nav [data-view="logs"]').click();
  await page.locator('.jr-card[data-level="ERROR"]').first().click();
  await page.locator(".jr-acts button", { hasText: "В Telegram" }).click();
  await expect.poll(() => calls.posts.at(-1)?.path).toBe("/api/logs/share");
  expect(calls.posts.at(-1).body).toMatchObject({ ts: expect.any(Number), id: expect.any(String) });
  await expect(page.locator("#notice")).toContainText("Запись отправлена в чат с ботом");
});

test("мини-приложение без доступа показывает ID для настройки", async ({ page }) => {
  await mockMiniApp(page, { forbidden: true });
  await page.goto(LAUNCH);
  await expect(page.locator("#tg-gate-title")).toHaveText("Нет доступа");
  await expect(page.locator("#tg-gate-copy")).toHaveText('HKC_TELEGRAM_ADMIN_IDS="777"');
});

test("мини-приложение вне Telegram просит открыть его из Telegram", async ({ page }) => {
  await mockMiniApp(page);
  await page.goto("/tg/");
  await expect(page.locator("#tg-gate-title")).toHaveText("Откройте из Telegram");
});

test("выключенное управление закрывает приложение окном с причиной и останавливает опрос", async ({ page }) => {
  const state = { disabled: false };
  const calls = await mockMiniApp(page, { state });
  await page.goto(LAUNCH);
  await expect(page.locator("#status-label")).toHaveText("бот запущен");
  state.disabled = true;
  await expect(page.locator("#tg-gate-title")).toHaveText("Приложение выключено");
  await expect(page.locator("#tg-gate-text")).toHaveText("Управление из Telegram выключено в админке.");
  // Окно открыто — панель не стучится каждую секунду.
  const sessions = calls.sessions;
  await page.waitForTimeout(2500);
  expect(calls.sessions - sessions).toBeLessThanOrEqual(1);
  state.disabled = false;
  await page.locator("#tg-gate-retry").click();
  await expect(page.locator("#tg-gate")).not.toHaveAttribute("open", "");
  await expect(page.locator("#status-label")).toHaveText("бот запущен");
});
