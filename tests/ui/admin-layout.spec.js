const { test, expect } = require("@playwright/test");

async function mockAdmin(page) {
  await page.addInitScript(() =>
    sessionStorage.setItem("hkc-admin-token", "test-token"),
  );
  const events = Array.from({ length: 30 }, (_, index) => ({
    actor: index % 2 ? "admin" : "watchdog",
    action: index % 3 ? "config.update" : "bot.recover",
    detail: `Событие ${index}: подробности операции, которые не должны растягивать весь список.`,
    time: new Date(Date.UTC(2026, 9, 10, 9, 0, 30 - index)).toISOString(),
    ip: "127.0.0.1",
  }));
  events.splice(1, 0, { ...events[0], time: "2026-10-10T08:59:00Z" });
  await page.route("**/api/admin/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    const data = {
      "/api/admin/overview": {
        users: [
          {
            username: "test",
            role: "operator",
            online: true,
            createdAt: "2026-10-01T10:00:00Z",
            lastSeen: "2026-10-10T09:00:00Z",
          },
        ],
        invites: [],
        bot: {
          running: true,
          pid: 42,
          uptime: "2ч 14м",
          version: "2.1.0",
          herokuDir: "/srv/Heroku",
        },
      },
      "/api/admin/audit": { events },
      "/api/admin/backups": {
        backups: Array.from({ length: 30 }, (_, index) => ({
          name: `web-auth-2026-10-10-${index}.json`,
          createdAt: "2026-10-10T09:00:00Z",
          size: 2048,
        })),
      },
      "/api/admin/config/history": {
        history: Array.from({ length: 30 }, (_, index) => ({
          name: `config-2026-10-10-${index}.json`,
          createdAt: "2026-10-10T09:00:00Z",
          size: 1024,
        })),
      },
      "/api/admin/config": {
        configured: {
          api_id: true,
          api_hash: true,
          redis_uri: false,
          db_uri: false,
          app_name: true,
        },
      },
      "/api/admin/security": {
        users: 1,
        sessions: 2,
        apiTokens: 1,
        rateLimiter: { blockedClients: 0 },
        features: { terminal: false, trustedProxy: true },
      },
      "/api/admin/watchdog": {
        settings: { enabled: true, timeoutSeconds: 180 },
        status: {
          state: "healthy",
          attempts: 0,
          message: "Бот работает, основной цикл Python отвечает.",
        },
      },
    };
    return route.fulfill({
      contentType: "application/json",
      body: JSON.stringify(data[path] || {}),
    });
  });
}

for (const width of [390, 1440])
  for (const theme of ["dark", "light"]) {
    test(`компактные рабочие области админки ${width}px ${theme}`, async ({
      page,
    }, testInfo) => {
      const errors = [];
      page.on("pageerror", (error) => errors.push(error.message));
      await mockAdmin(page);
      await page.setViewportSize({ width, height: 900 });
      await page.addInitScript(
        (value) => localStorage.setItem("hkc-theme", value),
        theme,
      );
      await page.goto("/admin/");
      await expect(page.locator("#admin-bot-status")).toHaveText(
        "Heroku работает",
      );
      for (const view of [
        "operations",
        "access",
        "settings",
        "history",
        "service",
        "updates",
      ]) {
        await page.locator(`[data-admin-view="${view}"]`).click();
        await expect(page.locator(`[data-admin-page="${view}"]`)).toBeVisible();
        await expect(page.locator("[data-admin-page]:visible")).toHaveCount(1);
        await page.screenshot({
          path: testInfo.outputPath(`admin-${view}-${width}-${theme}.png`),
          fullPage: true,
          animations: "disabled",
        });
        expect(
          await page.evaluate(
            () => document.documentElement.scrollWidth > innerWidth + 1,
          ),
          view,
        ).toBe(false);
      }
      await page.locator('[data-admin-view="history"]').click();
      await expect(page.locator(".audit-group")).toHaveCount(8);
      await page.locator(".audit-group summary").first().click();
      await expect(page.locator(".audit-detail").first()).toBeVisible();
      await page.locator("#admin-refresh").click();
      await expect(page.locator(".audit-group").first()).toHaveAttribute(
        "open",
        "",
      );
      await page.locator("#audit-search").fill("watchdog");
      await expect(page.locator("#audit-count")).toContainText("15 групп");
      await page.locator("#history-tab-backups").click();
      await expect(page.locator("#history-backups")).toBeVisible();
      await expect(page.locator("#history-audit")).toBeHidden();
      const listHeight = await page
        .locator("#backup-list")
        .evaluate((el) => el.getBoundingClientRect().height);
      expect(listHeight).toBeLessThanOrEqual(410);
      await page.locator("#history-tab-backups").press("ArrowRight");
      await expect(page.locator("#history-config")).toBeVisible();
      await expect(page.locator("#history-tab-config")).toBeFocused();
      await page.reload();
      await expect(page.locator("#admin-history")).toBeVisible();
      await expect(page.locator("#history-config")).toBeVisible();
      expect(errors).toEqual([]);
    });
  }

test("навигация сохраняет несохранённые поля настроек", async ({ page }) => {
  await mockAdmin(page);
  await page.goto("/admin/");
  await page.locator('[data-admin-view="settings"]').click();
  await page.locator('[name="app_name"]').fill("draft-name");
  await page.locator('[data-admin-view="history"]').click();
  await page.locator('[data-admin-view="settings"]').click();
  await expect(page.locator('[name="app_name"]')).toHaveValue("draft-name");
});
