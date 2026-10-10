const { test, expect } = require("@playwright/test");

// Сценарий обновления на подменённом API: служба проходит этапы, панель «перезапускается»
// (запросы временно падают), затем установка завершается.
const releases = [
  { version: "v2.6.0", date: "2026-10-11", sections: [{ title: "Добавлено", items: ["Новая страница обновлений с `откатом`", "Красивые анимации"] }] },
  { version: "v2.5.1", date: "2026-10-10", sections: [{ title: "Изменено", items: ["Установщик с прогрессом"] }] },
  { version: "v2.5.0", date: "2026-10-10", sections: [{ title: "Добавлено", items: ["Вкладка «Система»"] }] },
];

function overview(extra = {}) {
  return {
    installed: { version: "v2.5.1", commit: "a".repeat(40), modified: false },
    latest: { version: "v2.6.0", commit: "b".repeat(40), checkedAt: "2026-10-11T09:00:00Z", checking: false },
    updateAvailable: true,
    enabled: true,
    releases,
    backups: [{ name: "20261010T000000-v2.5.0", version: "v2.5.0", commit: "c".repeat(40), createdAt: "2026-10-10T08:00:00Z", size: 9437184 }],
    job: null,
    running: false,
    blockers: [],
    sources: [{ configured: true, path: "/opt/hrk-console", commit: "a".repeat(40), branch: "main", dirty: false }],
    ...extra,
  };
}

async function mockAdmin(page, handlers) {
  await page.addInitScript(() => {
    sessionStorage.setItem("hkc-admin-token", "test-token");
    localStorage.setItem("hkc-admin-view", "updates");
  });
  await page.route("**/api/admin/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const handler = handlers[path];
    if (handler) return handler(route, request);
    const basic = {
      "/api/admin/overview": { users: [], invites: [], bot: { running: true, pid: 1 } },
      "/api/admin/audit": { events: [] },
      "/api/admin/backups": { backups: [] },
      "/api/admin/config/history": { history: [] },
      "/api/admin/security": { rateLimiter: {}, features: {} },
      "/api/admin/watchdog": { settings: { enabled: true, timeoutSeconds: 180 }, status: { state: "healthy" } },
    };
    return route.fulfill({ contentType: "application/json", body: JSON.stringify(basic[path] || {}) });
  });
}

const json = (route, body, status = 200) =>
  route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });

test("обновление: что нового, установка по шагам, перезапуск и готово", async ({ page }, testInfo) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  let installBody = null;
  let progressCalls = 0;
  let finished = false;
  const job = (step, progress, phase = "restarting", extra = {}) => ({
    phase, step, progress, action: "install", version: "v2.6.0", fromVersion: "v2.5.1",
    message: `Этап ${step}`, startedAt: "2026-10-11T09:00:00Z", updatedAt: new Date().toISOString(),
    events: [{ at: "2026-10-11T09:00:00Z", step: "prepare", phase: "checking", message: "Готовимся" }], ...extra,
  });
  await mockAdmin(page, {
    "/api/admin/updates": (route) =>
      json(route, finished
        ? overview({ installed: { version: "v2.6.0", commit: "b".repeat(40) }, updateAvailable: false, job: job("done", 100, "complete", { message: "Готово: установлена v2.6.0." }) })
        : overview()),
    "/api/admin/updates/install": (route, request) => {
      installBody = request.postDataJSON();
      return json(route, { ok: true, message: "Запущено." }, 202);
    },
    "/api/admin/updates/progress": (route) => {
      progressCalls++;
      if (progressCalls === 1) return json(route, { running: true, job: job("download", 30, "downloading", { downloaded: 3145728, total: 6291456 }) });
      if (progressCalls === 2) return route.abort(); // панель перезапускается
      if (progressCalls <= 6) return json(route, { running: true, job: job("health", 82) });
      finished = true;
      return json(route, { running: false, job: job("done", 100, "complete", { message: "Готово: установлена v2.6.0." }) });
    },
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/admin/");
  const hero = page.locator("#upd-hero");
  await expect(hero).toHaveAttribute("data-state", "available");
  await expect(page.locator("#upd-title")).toHaveText("Вышла v2.6.0");
  await expect(page.locator("#upd-install")).toHaveText("Обновить до v2.6.0");
  await expect(page.locator(".upd-release")).toHaveCount(1);
  await expect(page.locator(".upd-release code")).toHaveText("откатом");
  await page.locator("#upd-releases-more").click();
  await expect(page.locator(".upd-release")).toHaveCount(3);
  await expect(page.locator("#upd-backups li")).toHaveCount(1);
  await page.screenshot({ path: testInfo.outputPath("updates-available.png"), fullPage: true });

  await page.locator("#upd-install").click();
  await expect(page.locator("#confirm-title")).toHaveText("Установить v2.6.0?");
  await page.locator("#confirm-accept").click();
  await expect.poll(() => installBody).toEqual({ version: "v2.6.0" });
  await expect(hero).toHaveAttribute("data-state", "running");
  await expect(page.locator("#upd-progress")).toBeVisible();
  await expect(page.locator('#upd-steps li[data-step="download"]')).toHaveAttribute("data-status", "active");
  await expect(page.locator('#upd-steps li[data-step="download"] small')).toHaveText("3.0 МБ из 6.0 МБ");
  await page.screenshot({ path: testInfo.outputPath("updates-running.png"), fullPage: true });
  await expect(page.locator("#upd-reconnect")).toBeVisible();
  await expect(page.locator('#upd-steps li[data-step="health"]')).toHaveAttribute("data-status", "active");
  await expect(page.locator("#upd-reconnect")).toBeHidden();
  await expect(hero).toHaveAttribute("data-state", "done", { timeout: 10000 });
  await expect(page.locator("#upd-title")).toHaveText("Установлена v2.6.0");
  await expect(page.locator('#upd-steps li[data-status="done"]')).toHaveCount(8);
  await expect(page.locator("#admin-notice")).not.toContainText("Failed to fetch");
  await page.screenshot({ path: testInfo.outputPath("updates-done.png"), fullPage: true });
  await page.setViewportSize({ width: 390, height: 900 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)).toBe(false);
  await page.screenshot({ path: testInfo.outputPath("updates-mobile.png"), fullPage: true });
  expect(errors).toEqual([]);
});

test("обновление: причины блокировки, откат и выбор версии", async ({ page }) => {
  let rollbackBody = null;
  let installBody = null;
  await mockAdmin(page, {
    "/api/admin/updates": (route) =>
      json(route, overview({ updateAvailable: false, latest: { version: "v2.5.1", checkedAt: "2026-10-11T09:00:00Z" }, releases })),
    "/api/admin/updates/rollback": (route, request) => {
      rollbackBody = request.postDataJSON();
      return json(route, { ok: true, message: "Запущено." }, 202);
    },
    "/api/admin/updates/install": (route, request) => {
      installBody = request.postDataJSON();
      return json(route, { ok: true, message: "Запущено." }, 202);
    },
    "/api/admin/updates/progress": (route) => json(route, { running: true, job: { phase: "checking", step: "prepare", progress: 5, action: "rollback" } }),
  });
  await page.goto("/admin/");
  await expect(page.locator("#upd-hero")).toHaveAttribute("data-state", "current");
  await expect(page.locator("#upd-title")).toHaveText("У вас последняя версия");
  await expect(page.locator("#upd-install")).toBeHidden();
  await expect(page.locator("#upd-version option:checked")).toHaveText("v2.6.0");
  await page.locator("#upd-version").selectOption("v2.5.0");
  await page.locator("#upd-version-install").click();
  await expect(page.locator("#confirm-title")).toHaveText("Установить v2.5.0?");
  await page.locator("#confirm-dialog button[value=cancel]").click();
  expect(installBody).toBeNull();
  await page.locator("#upd-backups button").click();
  await expect(page.locator("#confirm-title")).toHaveText("Вернуть v2.5.0?");
  await page.locator("#confirm-accept").click();
  await expect.poll(() => rollbackBody).toEqual({ backup: "20261010T000000-v2.5.0" });
  await expect(page.locator('#upd-steps li')).toHaveCount(6);
});

test("обновление: понятная причина, если установка не настроена", async ({ page }) => {
  await mockAdmin(page, {
    "/api/admin/updates": (route) =>
      json(route, overview({ enabled: false, blockers: [{ text: "Установка из панели не настроена на этом сервере.", fix: "Запустите установщик." }] })),
  });
  await page.goto("/admin/");
  await expect(page.locator("#upd-blockers li")).toHaveCount(1);
  await expect(page.locator("#upd-blockers li span")).toHaveText("Запустите установщик.");
  await expect(page.locator("#upd-install")).toBeDisabled();
  await expect(page.locator("#upd-version-install")).toBeDisabled();
});

test("обновление: мини-консоль и отмена до замены сборки", async ({ page }, testInfo) => {
  let cancelled = false;
  let cancelCalls = 0;
  const events = [
    { at: "2026-10-11T09:00:00Z", step: "prepare", phase: "checking", message: "Готовимся к установке: проверяем окружение." },
    { at: "2026-10-11T09:00:01Z", step: "prepare", phase: "checking", kind: "cmd", message: "git fetch https://github.com/ayanamisuicide/hrk-console.git refs/tags/v2.6.0" },
    { at: "2026-10-11T09:00:02Z", step: "download", phase: "downloading", message: "Скачиваем hkc-web-v2.6.0-linux-amd64.tar.gz." },
    { at: "2026-10-11T09:00:02Z", step: "download", phase: "downloading", kind: "cmd", message: "GET https://github.com/ayanamisuicide/hrk-console/releases/download/v2.6.0/hkc-web-v2.6.0-linux-amd64.tar.gz" },
  ];
  const running = () => ({
    running: true,
    cancellable: true,
    job: { phase: "downloading", step: "download", progress: 30, action: "install", version: "v2.6.0", downloaded: 1572864, total: 3329466,
      message: "Скачиваем архив релиза.", startedAt: "2026-10-11T09:00:00Z", updatedAt: new Date().toISOString(), events },
  });
  const cancelledJob = () => ({ ...running().job, phase: "cancelled", step: "download", progress: 100,
    message: "Обновление отменено. Ничего не изменено, работает прежняя версия.",
    events: [...events, { at: "2026-10-11T09:00:05Z", step: "done", phase: "cancelled", message: "Обновление отменено. Ничего не изменено, работает прежняя версия." }] });
  await mockAdmin(page, {
    "/api/admin/updates": (route) => json(route, overview({ running: !cancelled, cancellable: !cancelled, job: cancelled ? cancelledJob() : running().job })),
    "/api/admin/updates/progress": (route) =>
      json(route, cancelled ? { running: false, cancellable: false, job: cancelledJob() } : running()),
    "/api/admin/updates/cancel": (route) => {
      cancelCalls++;
      cancelled = true;
      return json(route, { ok: true, message: "Отменяем — служба остановится на ближайшем шаге." }, 202);
    },
  });
  await page.setViewportSize({ width: 1440, height: 1000 });
  await page.goto("/admin/");
  await expect(page.locator("#upd-hero")).toHaveAttribute("data-state", "running");
  await expect(page.locator('#upd-console li[data-kind="cmd"]').first()).toContainText("git fetch");
  await expect(page.locator("#upd-console .upd-console-live")).toContainText("47%");
  await expect(page.locator("#upd-console .upd-console-cursor")).toHaveCount(1);
  await page.screenshot({ path: testInfo.outputPath("updates-console.png"), fullPage: true });
  const cancel = page.locator("#upd-cancel");
  await expect(cancel).toBeEnabled();
  await cancel.click();
  await expect(page.locator("#confirm-title")).toHaveText("Отменить обновление?");
  await page.locator("#confirm-accept").click();
  await expect.poll(() => cancelCalls).toBe(1);
  await expect(page.locator("#upd-hero")).toHaveAttribute("data-state", "cancelled");
  await expect(page.locator("#upd-title")).toHaveText("Обновление отменено");
  await expect(page.locator('#upd-steps li[data-step="download"]')).toHaveAttribute("data-status", "cancelled");
  await expect(cancel).toBeHidden();
  await expect(page.locator("#upd-console .upd-console-cursor")).toHaveCount(0);
  await expect(page.locator('#upd-console li[data-kind="cancelled"]')).toHaveCount(1);
});

test("обновление: после замены сборки отмена недоступна и объясняет почему", async ({ page }) => {
  await mockAdmin(page, {
    "/api/admin/updates": (route) => json(route, overview({ running: true, cancellable: false, job: { phase: "restarting", step: "health", progress: 82, action: "install", version: "v2.6.0", updatedAt: new Date().toISOString(), events: [] } })),
    "/api/admin/updates/progress": (route) => json(route, { running: true, cancellable: false, job: { phase: "restarting", step: "health", progress: 82, action: "install", version: "v2.6.0", updatedAt: new Date().toISOString(), events: [] } }),
  });
  await page.goto("/admin/");
  const cancel = page.locator("#upd-cancel");
  await expect(cancel).toBeVisible();
  await expect(cancel).toBeDisabled();
  await expect(cancel).toHaveAttribute("title", /прервать нельзя/);
});

test("перезапуск панели: предупреждение о боте, ожидание и переподключение", async ({ page }) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  let restarted = false;
  let downCalls = 0;
  await mockAdmin(page, {
    "/api/admin/updates": (route) => json(route, overview()),
    "/api/admin/panel": (route) => {
      if (!restarted) return json(route, { startedAt: "2026-10-11T09:00:00Z", pid: 10, restartable: true, keepsBot: false });
      // Пара неудачных опросов — панель ещё поднимается.
      if (++downCalls <= 2) return route.abort();
      return json(route, { startedAt: "2026-10-11T09:05:00Z", pid: 11, restartable: true, keepsBot: true });
    },
    "/api/admin/panel/restart": (route) => {
      restarted = true;
      return json(route, { ok: true, message: "панель перезапускается", startedAt: "2026-10-11T09:00:00Z" }, 202);
    },
  });
  await page.goto("/admin/");
  await page.locator("#panel-restart").click();
  await expect(page.locator("#confirm-title")).toHaveText("Перезапустить панель?");
  // Старый юнит: страница честно предупреждает, что бот остановится.
  await expect(page.locator("#confirm-message")).toContainText("бот остановится");
  await page.locator("#confirm-accept").click();
  await expect(page.locator("#upd-reconnect")).toBeVisible();
  await expect(page.locator("#admin-notice")).toHaveText("Панель перезапущена", { timeout: 10000 });
  await expect(page.locator("#upd-reconnect")).toBeHidden();
  await expect(page.locator("#panel-restart")).toBeEnabled();
  expect(errors).toEqual([]);
});
