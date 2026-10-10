const { test, expect } = require("@playwright/test");

// Раздел «Telegram» в админке и мини-приложение на подменённом API.
const json = (route, body, status = 200) =>
  route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });

function telegramState(extra = {}) {
  return {
    token: true,
    tokenIssue: "",
    chat: true,
    chatIssue: "",
    admins: ["123456789"],
    adminsIssue: "",
    webApp: "",
    webAppIssue: "",
    state: "running",
    error: "",
    lastPollAt: new Date().toISOString(),
    bot: { id: "42", username: "hrk_panel_bot", name: "HRK Panel" },
    settings: { controlEnabled: true, confirm: true },
    notifications: true,
    commands: [
      { name: "status", description: "Состояние бота и сервера", confirm: false },
      { name: "run", description: "Запустить бота", action: "start", confirm: false },
      { name: "restart", description: "Перезапустить бота", action: "restart", confirm: true },
      { name: "stop", description: "Остановить бота", action: "stop", confirm: true },
      { name: "app", description: "Открыть мини-приложение", confirm: false },
      { name: "help", description: "Список команд", confirm: false },
    ],
    unknown: [],
    activity: [
      { time: "2026-10-10T09:12:00Z", actor: "telegram:@anya", action: "bot.restart", detail: "бот перезапущен", ip: "telegram" },
      { time: "2026-10-10T08:40:00Z", actor: "admin", action: "telegram.settings", detail: "control=true confirm=true", ip: "127.0.0.1" },
    ],
    ...extra,
  };
}

async function mockAdmin(page, telegram) {
  await page.addInitScript(() => {
    sessionStorage.setItem("hkc-admin-token", "test-token");
    localStorage.setItem("hkc-admin-view", "telegram");
  });
  let current = telegram;
  const saved = [];
  await page.route("**/api/admin/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/admin/telegram") {
      if (request.method() === "PUT") {
        const body = request.postDataJSON();
        saved.push(body);
        current = { ...current, settings: body, state: body.controlEnabled ? "running" : "disabled" };
      }
      return json(route, current);
    }
    const basic = {
      "/api/admin/overview": { users: [], invites: [], bot: { running: true, pid: 1 } },
      "/api/admin/audit": { events: [] },
      "/api/admin/backups": { backups: [] },
      "/api/admin/config/history": { history: [] },
      "/api/admin/security": { rateLimiter: {}, features: {} },
      "/api/admin/watchdog": { settings: { enabled: true, timeoutSeconds: 180 }, status: { state: "healthy" } },
    };
    return json(route, basic[path] || {});
  });
  return saved;
}

test("telegram в админке: состояние, шаги, команды и главный переключатель", async ({ page }, testInfo) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.setViewportSize({ width: 1440, height: 1200 });
  const saved = await mockAdmin(page, telegramState({ webApp: "", unknown: [{ id: "777", name: "Гость", username: "guest", chat: "private", at: "2026-10-10T09:00:00Z" }] }));
  await page.goto("/admin/");
  await expect(page.locator("#admin-page-title")).toHaveText("Telegram");
  await expect(page.locator("#tg-title")).toHaveText("@hrk_panel_bot на связи");
  await expect(page.locator('[data-chip="control"]')).toHaveAttribute("data-active", "true");
  await expect(page.locator('[data-chip="app"]')).toHaveAttribute("data-active", "false");
  // Шаг мини-приложения ещё не сделан и подсказывает Tailscale.
  await expect(page.locator("#tg-steps li").nth(2)).toHaveAttribute("data-state", "todo");
  await expect(page.locator("#tg-steps li").nth(2)).toContainText("tailscale serve");
  await expect(page.locator("#tg-strangers")).toBeVisible();
  await expect(page.locator(".tg-stranger")).toContainText("ID 777");
  // /app без мини-приложения не показывается.
  await expect(page.locator(".tg-command code")).toHaveText(["/status", "/run", "/restart", "/stop", "/help"]);
  await expect(page.locator(".tg-event")).toHaveCount(2);
  await page.waitForTimeout(3600);
  await page.screenshot({ path: testInfo.outputPath("telegram-admin.png"), fullPage: true });

  await page.locator("#tg-control").uncheck();
  await expect.poll(() => saved.at(-1)?.controlEnabled).toBe(false);
  await expect(page.locator("#tg-title")).toHaveText("Управление выключено");
  // Включение спрашивает подтверждение.
  await page.locator("#tg-control").check();
  await expect(page.locator("#confirm-title")).toHaveText("Включить управление из Telegram?");
  await page.locator("#confirm-accept").click();
  await expect.poll(() => saved.at(-1)?.controlEnabled).toBe(true);

  await page.setViewportSize({ width: 390, height: 900 });
  await page.screenshot({ path: testInfo.outputPath("telegram-admin-mobile.png"), fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)).toBe(false);
  expect(errors).toEqual([]);
});

test("telegram в админке: бот занят другой программой", async ({ page }) => {
  await mockAdmin(page, telegramState({ state: "conflict", error: "Этого бота уже опрашивает другая программа.", bot: null }));
  await page.goto("/admin/");
  await expect(page.locator("#tg-hero")).toHaveAttribute("data-state", "conflict");
  await expect(page.locator("#tg-subtitle")).toHaveText("Этого бота уже опрашивает другая программа.");
});

function historyPoints() {
  const now = Date.now();
  return Array.from({ length: 120 }, (_, index) => ({
    at: new Date(now - (119 - index) * 1000).toISOString(),
    cpu: 20 + 15 * Math.sin(index / 9),
    memory: 48 + 4 * Math.sin(index / 20),
    disk: 63,
  }));
}

async function mockMiniApp(page, { forbidden = false } = {}) {
  // Мост Telegram в тестах недоступен: приложение берёт данные запуска из адреса.
  await page.route("https://telegram.org/**", (route) => route.abort());
  const actions = [];
  let running = true;
  await page.route("**/api/tg/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/tg/session") {
      if (forbidden) return json(route, { message: "нет доступа", id: "777" }, 403);
      return json(route, { token: "session", user: { name: "Аня", username: "anya" } });
    }
    if (request.headers().authorization !== "Bearer session") return json(route, { message: "сессия истекла" }, 401);
    if (path === "/api/tg/overview")
      return json(route, {
        bot: running
          ? { running: true, pid: 4821, uptime: "34м 05с", version: "1.7.2", rssBytes: 312 * 1048576, cpuPercent: 4.2 }
          : { running: false, pid: 0, uptime: "—", version: "1.7.2" },
        watchdog: { enabled: true, state: "healthy", message: "Бот отвечает" },
        host: "vps-1",
        panel: { version: "v2.10.0" },
        system: { supported: true, cpuPercent: 23, memoryPercent: 49, diskPercent: 63, memoryUsedBytes: 2e9, memoryTotalBytes: 4e9, diskUsedBytes: 25e9, diskTotalBytes: 40e9, load1: 0.42, cores: 2, uptimeSeconds: 864000 },
        user: { name: "Аня", username: "anya" },
        confirm: true,
      });
    if (path === "/api/tg/system/history") return json(route, { points: historyPoints() });
    if (path === "/api/tg/incidents")
      return json(route, { incidents: [{ start: "09:00", end: "09:02", level: "ERROR", module: "weather", title: "TimeoutError: api не ответил", count: 3 }] });
    if (path === "/api/tg/modules")
      return json(route, {
        status: "live",
        message: "Состояние загрузки обновляется каждую секунду.",
        modules: [
          { id: "1", name: "Weather", kind: "external", state: "error", version: "1.2", error: "Traceback (most recent call last):\n  ...\nTimeoutError" },
          { id: "2", name: "Help", kind: "core", state: "ready" },
          { id: "3", name: "Notes", kind: "external", state: "ready", version: "3.0" },
          { id: "4", name: "Loader", kind: "core", state: "loading" },
        ],
      });
    if (path === "/api/tg/logs")
      return json(route, {
        lines: [
          "2026-10-10 09:00:00 [INFO] heroku: started",
          "2026-10-10 09:00:01 [WARNING] weather: slow response",
          "2026-10-10 09:00:02 [ERROR] weather: TimeoutError",
          "2026-10-10 09:00:03 [INFO] notes: saved",
        ],
      });
    if (path.startsWith("/api/tg/bot/")) {
      const action = path.split("/").pop();
      actions.push(action);
      running = action !== "stop";
      return json(route, { ok: true, message: action === "stop" ? "бот остановлен" : "бот перезапущен" });
    }
    return json(route, {});
  });
  return actions;
}

test("мини-приложение: главная, шторки, модули, журнал и действие с подтверждением", async ({ page }, testInfo) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ reducedMotion: "no-preference" });
  const actions = await mockMiniApp(page);
  await page.goto("/tg/#tgWebAppData=query_id%3DAAE%26user%3D%257B%2522id%2522%253A1%257D%26hash%3Dx");
  await expect(page.locator("#hero")).toHaveAttribute("data-state", "running");
  await expect(page.locator("#skeleton")).toBeHidden();
  await expect(page.locator("#bot-title")).toHaveText("Работает");
  await expect(page.locator("#bot-rss")).toHaveText("312 МБ");
  await expect(page.locator("#watchdog-pill")).toHaveText("Вкл");
  await expect(page.locator("#bar-sub")).toHaveText("vps-1 · v2.10.0");
  await expect(page.locator(".incident")).toHaveCount(1);
  await expect(page.locator("#metric-cpu")).toHaveText(/^\d+%$/);
  // Секунды времени работы идут между опросами.
  const first = await page.locator("#bot-subtitle").textContent();
  await page.waitForTimeout(1300);
  expect(await page.locator("#bot-subtitle").textContent()).not.toBe(first);
  await page.waitForTimeout(700);
  await page.screenshot({ path: testInfo.outputPath("miniapp-home.png"), fullPage: true });

  // Метрика открывает шторку с большим числом и графиком; шторка закрывается по затемнению.
  await page.locator('[data-metric="cpu"]').click();
  await expect(page.locator("#sheet-layer")).toHaveAttribute("data-open", "");
  await expect(page.locator("#sheet-title")).toHaveText("CPU");
  await expect(page.locator(".bigvalue")).toHaveText(/^\d+%$/);
  await expect(page.locator(".fact")).toHaveCount(3);
  await page.waitForTimeout(600);
  await page.screenshot({ path: testInfo.outputPath("miniapp-metric.png") });
  await page.locator("#sheet-backdrop").click({ position: { x: 190, y: 40 } });
  await expect(page.locator("#sheet-layer")).toBeHidden();

  // Остановка спрашивает подтверждение шторкой; отмена ничего не делает.
  await page.locator('[data-action="stop"]').click();
  await expect(page.locator("#sheet-title")).toHaveText("Остановить бота?");
  await page.locator(".sbtn", { hasText: "Отмена" }).click();
  await expect(page.locator("#sheet-layer")).toBeHidden();
  expect(actions).toEqual([]);
  await page.locator('[data-action="stop"]').click();
  await page.waitForTimeout(550);
  await page.screenshot({ path: testInfo.outputPath("miniapp-confirm.png") });
  await page.locator(".sbtn.danger").click();
  await expect.poll(() => actions).toEqual(["stop"]);
  await expect(page.locator("#toast")).toHaveText("Бот остановлен");
  await expect(page.locator("#hero")).toHaveAttribute("data-state", "stopped");
  await expect(page.locator('[data-action="start"]')).toBeEnabled();
  await expect(page.locator('[data-action="stop"]')).toBeDisabled();

  // Происшествие открывает шторку и ведёт в журнал с фильтром по модулю.
  await page.locator(".incident").click();
  await expect(page.locator("#sheet-title")).toHaveText("TimeoutError: api не ответил");
  await page.locator(".sbtn.primary", { hasText: "Показать в журнале" }).click();
  await expect(page.locator("#log-chip")).toBeVisible();
  await expect(page.locator("#log-chip span")).toHaveText("модуль: weather");
  // Происшествие уровня ERROR открывает журнал уже с фильтром «Ошибки» и модулем.
  await expect(page.locator("#log p")).toHaveCount(1);
  await expect(page.locator('[data-logs-level="ERROR"]')).toHaveAttribute("aria-selected", "true");
  await page.locator("#log-chip").click();
  await expect(page.locator("#log-chip")).toBeHidden();
  await page.locator('[data-logs-level="all"]').click();
  await expect(page.locator("#log p")).toHaveCount(4);

  await page.locator('[data-tab="modules"]').click();
  await expect(page.locator(".mod")).toHaveCount(4);
  await expect(page.locator(".mod").first()).toHaveAttribute("data-state", "error");
  await expect(page.locator("#modules-badge")).toHaveText("1");
  await page.waitForTimeout(700);
  // Бегунок стоит под выбранной кнопкой, а не сбит анимацией появления.
  expect(await page.locator('[aria-label="Фильтр модулей"]').evaluate((el) => getComputedStyle(el.querySelector(".seg-thumb")).transform)).toMatch(/^(none|matrix\(1, 0, 0, 1, 0, 0\))$/);
  await page.screenshot({ path: testInfo.outputPath("miniapp-modules.png"), fullPage: true });
  await page.locator(".mod").first().click();
  await expect(page.locator("#sheet-title")).toHaveText("Weather");
  await expect(page.locator(".code.bad")).toContainText("TimeoutError");
  await page.waitForTimeout(550);
  await page.screenshot({ path: testInfo.outputPath("miniapp-module.png") });
  await page.keyboard.press("Escape");
  await expect(page.locator("#sheet-layer")).toBeHidden();
  await page.locator('[data-modules-filter="problems"]').click();
  await expect(page.locator(".mod")).toHaveCount(1);
  await expect(page.locator(".segmented").first().evaluate((el) => el.style.getPropertyValue("--i"))).resolves.toBe("1");

  await page.locator('[data-tab="logs"]').click();
  await expect(page.locator("#log p")).toHaveCount(4);
  await expect(page.locator('[data-log-count="ERROR"]')).toHaveText("1");
  await page.locator('[data-logs-level="ERROR"]').click();
  await expect(page.locator("#log p")).toHaveCount(1);
  await expect(page.locator("#log p")).toHaveAttribute("data-level", "ERROR");
  await page.screenshot({ path: testInfo.outputPath("miniapp-logs.png"), fullPage: true });
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)).toBe(false);
  expect(errors).toEqual([]);
});

test("мини-приложение: новые строки журнала дописываются, не сбивая прокрутку", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await mockMiniApp(page);
  let lines = Array.from({ length: 60 }, (_, index) => `2026-10-10 09:00:${String(index).padStart(2, "0")} [INFO] app: line ${index}`);
  await page.route("**/api/tg/logs*", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ lines }) }));
  await page.clock.install();
  await page.goto("/tg/#tgWebAppData=hash%3Dx");
  await page.locator('[data-tab="logs"]').click();
  await expect(page.locator("#log p")).toHaveCount(60);
  const marker = page.locator("#log p").nth(30);
  await marker.evaluate((element) => (element.dataset.persistent = "yes"));
  // Пользователь ушёл вверх по журналу: новые строки не сдёргивают его вниз, а копятся в счётчике.
  await page.locator("#log").evaluate((element) => {
    element.style.scrollBehavior = "auto";
    element.scrollTop = 0;
  });
  await expect(page.locator("#log-jump")).toBeVisible();
  lines = [...lines.slice(3), ...Array.from({ length: 3 }, (_, index) => `2026-10-10 09:01:0${index} [ERROR] app: fresh ${index}`)];
  await page.clock.runFor(3200);
  await expect(page.locator("#log p")).toHaveCount(60);
  await expect(page.locator("#log p").last()).toContainText("fresh 2");
  await expect(page.locator("#log p.fresh")).toHaveCount(3);
  await expect(page.locator('[data-persistent="yes"]')).toHaveCount(1);
  await expect(page.locator("#log-unread")).toHaveText("3");
  expect(await page.locator("#log").evaluate((element) => element.scrollTop)).toBeLessThan(40);
});

test("мини-приложение: без доступа показывает ID для настройки", async ({ page }) => {
  await mockMiniApp(page, { forbidden: true });
  await page.goto("/tg/#tgWebAppData=hash%3Dx");
  await expect(page.locator("#gate")).toHaveAttribute("data-state", "forbidden");
  await expect(page.locator("#gate-copy")).toHaveText('HKC_TELEGRAM_ADMIN_IDS="777"');
});

test("мини-приложение: вне Telegram просит открыть из Telegram", async ({ page }) => {
  await page.route("https://telegram.org/**", (route) => route.abort());
  await page.goto("/tg/");
  await expect(page.locator("#gate")).toHaveAttribute("data-state", "outside");
});
