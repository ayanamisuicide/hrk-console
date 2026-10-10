const { test, expect } = require("@playwright/test");

async function fixture(page) {
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/events") return route.abort();
    const data = path === "/api/auth/me" ? { username: "tester", role: "viewer" } : path === "/api/status" ? { running: true, pid: 42, uptime: "1ч" } : {};
    return route.fulfill({ contentType: "application/json", body: JSON.stringify(data) });
  });
}

test("секундный опрос сохраняет строки, раскрытие и фильтры", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await fixture(page);
  let hits = 0;
  let state = "loading";
  await page.route("**/api/modules", (route) => {
    hits++;
    return route.fulfill({ contentType: "application/json", body: JSON.stringify({
      status: "live", session: "one", message: "Каждую секунду",
      modules: [
        { id: "weather", name: "Weather", kind: "external", state, version: "1.0", error: "" },
        { id: "bad", name: "<img src=x onerror=alert(1)>", kind: "core", state: "error", error: "<script>oops</script>" },
      ],
    }) });
  });
  await page.clock.install();
  await page.goto("/");
  await expect(page.locator("#username")).toHaveText("tester");
  await page.locator('[data-view="modules"]').click();
  const row = page.locator('[data-id="weather"]');
  await expect(row).toBeVisible();
  expect(await row.evaluate((el) => getComputedStyle(el).animationName)).toBe("module-enter");
  await row.locator("summary").click();
  await row.evaluate((element) => { element.dataset.persistent = "yes"; });
  state = "ready";
  const before = hits;
  await page.clock.runFor(2200);
  await expect.poll(() => hits).toBeGreaterThanOrEqual(before + 2);
  await expect(row).toHaveAttribute("data-state", "ready");
  await expect(row).toHaveAttribute("open", "");
  await expect(row).toHaveAttribute("data-persistent", "yes");
  await expect(page.locator("#modules-list img, #modules-list script")).toHaveCount(0);
  await page.locator("#modules-only-problems").click();
  await expect(row).toBeHidden();
  await expect(page.locator('[data-id="bad"]')).toBeVisible();
  await page.locator("#modules-search").fill("missing");
  await expect(page.locator("#modules-empty")).toContainText("По этим фильтрам");
  await page.locator("#modules-search").press("/");
  await expect(page.locator("#modules-view")).toBeVisible();
  await page.locator('[data-view="logs"]').click();
  const idle = hits;
  await page.clock.runFor(2200);
  expect(hits).toBe(idle);
});

test("потеря связи и новый запуск не оставляют старые зелёные статусы", async ({ page }) => {
  await fixture(page);
  let session = "one";
  let fail = false;
  await page.route("**/api/modules", (route) => fail ? route.abort() : route.fulfill({ contentType: "application/json", body: JSON.stringify({
    status: "live", session, message: "Подключено", modules: session === "one" ? [{ id: "old", name: "Old", kind: "core", state: "ready" }] : [{ id: "new", name: "New", kind: "core", state: "loading" }],
  }) }));
  await page.clock.install();
  await page.goto("/");
  await expect(page.locator("#username")).toHaveText("tester");
  await page.locator('[data-view="modules"]').click();
  await expect(page.locator('[data-id="old"]')).toBeVisible();
  fail = true;
  await page.clock.runFor(1100);
  await expect(page.locator("#modules-connection")).toHaveAttribute("data-status", "stale");
  await expect(page.locator("#modules-list")).toHaveClass(/modules-stale/);
  fail = false; session = "two";
  await page.clock.runFor(1100);
  await expect(page.locator('[data-id="old"]')).toHaveCount(0);
  await expect(page.locator('[data-id="new"]')).toBeVisible();
  expect(await page.locator('[data-id="new"]').evaluate((el) => getComputedStyle(el).animationName)).toBe("none");
});
