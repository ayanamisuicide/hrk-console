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
  // Страница Telegram общая с мини-приложением и сохраняет настройки через /api/telegram.
  await page.route(/\/api\/telegram(\/|$)/, async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/telegram" && request.method() === "PUT") {
      const body = request.postDataJSON();
      saved.push(body);
      current = { ...current, settings: body, state: body.controlEnabled ? "running" : "disabled" };
      if ("digestOff" in body) current.digest = { ...current.digest, enabled: !body.digestOff, at: body.digestAt || current.digest?.at };
    }
    return json(route, current);
  });
  await page.route("**/api/admin/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (path === "/api/admin/telegram") {
      if (request.method() === "PUT") {
        const body = request.postDataJSON();
        saved.push(body);
        current = { ...current, settings: body, state: body.controlEnabled ? "running" : "disabled" };
        if ("digestOff" in body) current.digest = { ...current.digest, enabled: !body.digestOff, at: body.digestAt || current.digest?.at };
      }
      return json(route, current);
    }
    const basic = {
      "/api/admin/overview": { users: [], invites: [], bot: { running: true, pid: 1 } },
      "/api/admin/audit": { events: [] },
      "/api/admin/backups": { backups: [] },
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

test("telegram в админке: утренняя сводка включается, время меняется", async ({ page }) => {
  const saved = await mockAdmin(page, telegramState({ digest: { enabled: true, at: "08:00", lastSent: "2026-10-14T05:00:00Z" } }));
  await page.goto("/admin/");
  await expect(page.locator("#tg-digest")).toBeChecked();
  await expect(page.locator("#tg-digest-at")).toHaveValue("08:00");
  await expect(page.locator("#tg-digest-note")).toContainText("Последняя пришла");

  await page.locator("#tg-digest-at").fill("08:30");
  await page.locator("#tg-digest-form button[type=submit]").click();
  await expect.poll(() => saved.at(-1)).toMatchObject({ digestOff: false, digestAt: "08:30", controlEnabled: true });
  await expect(page.locator("#tg-digest-at")).toHaveValue("08:30");

  await page.locator("#tg-digest").uncheck();
  await page.locator("#tg-digest-form button[type=submit]").click();
  await expect.poll(() => saved.at(-1)?.digestOff).toBe(true);
  await expect(page.locator("#tg-digest-note")).toContainText("Утренняя сводка выключена");
});

test("telegram в админке: бот занят другой программой", async ({ page }) => {
  await mockAdmin(page, telegramState({ state: "conflict", error: "Этого бота уже опрашивает другая программа.", bot: null }));
  await page.goto("/admin/");
  await expect(page.locator("#tg-hero")).toHaveAttribute("data-state", "conflict");
  await expect(page.locator("#tg-subtitle")).toHaveText("Этого бота уже опрашивает другая программа.");
});
