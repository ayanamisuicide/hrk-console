const { test, expect } = require("@playwright/test");

async function fixture(page) {
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/events") return route.abort();
    const data =
      path === "/api/auth/me"
        ? { username: "tester", role: "viewer" }
        : path === "/api/status"
          ? { running: true, pid: 42, uptime: "1ч" }
          : {};
    return route.fulfill({ contentType: "application/json", body: JSON.stringify(data) });
  });
}

const serve = (page, body) =>
  page.route("**/api/modules", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify(body()) }));

function catalog() {
  return Array.from({ length: 48 }, (_, index) => ({
    id: `module-${index}`,
    name: ["Weather", "HerokuSettings", "AccountSwitcher", "TelegramTranslator", "MediaDownloader", "AutoResponder"][index % 6] + index,
    kind: index % 2 ? "external" : "core",
    version: "1.2.0",
    state: index % 9 === 0 ? "error" : index % 7 === 0 ? "loading" : "ready",
    error:
      index % 9 === 0
        ? "Traceback (most recent call last):\n  File \"weather.py\", line 4\nModuleNotFoundError: No module named 'missing_dependency'"
        : "",
  }));
}

for (const width of [390, 1440])
  test(`модули: сводка, проблемы, плитки и карточка ${width}px`, async ({ page }, testInfo) => {
    await page.setViewportSize({ width, height: 900 });
    await page.emulateMedia({ reducedMotion: "no-preference" });
    await fixture(page);
    const modules = catalog();
    await serve(page, () => ({ status: "live", session: "grid", message: "Состояние загрузки обновляется каждую секунду.", modules }));
    await page.goto("/");
    await page.locator('[data-view="modules"]').click();

    // Главное — в одной фразе: 6 модулей упали.
    await expect(page.locator("#mods-headline")).toHaveText("6 модулей не загрузились");
    await expect(page.locator("#modules-view")).toHaveAttribute("data-health", "problems");
    await expect(page.locator("#mods-attention")).toBeVisible();
    // Длинный список упавших свёрнут до четырёх, остальные — по кнопке.
    await expect(page.locator(".mods-issue:visible")).toHaveCount(4);
    await expect(page.locator("#mods-issues-more")).toHaveText("Показать ещё 2");
    await page.locator("#mods-issues-more").click();
    await expect(page.locator(".mods-issue:visible")).toHaveCount(6);
    await page.locator("#mods-issues-more").click();
    await expect(page.locator(".mods-issue").first().locator(".mods-issue-error")).toHaveText("ModuleNotFoundError: No module named 'missing_dependency'");
    // Все модули на месте без постраничного переключения, разделены на группы.
    await expect(page.locator(".mods-tile:visible")).toHaveCount(48);
    await expect(page.locator('[data-group-count="external"]')).toHaveText("24");
    // Внутри группы упавшие идут первыми.
    await expect(page.locator('[data-grid="core"] .mods-tile').first()).toHaveAttribute("data-category", "problems");
    expect(await page.locator('[data-grid="core"]').evaluate((el) => getComputedStyle(el).gridTemplateColumns.split(" ").length)).toBe(width === 390 ? 1 : 4);
    // Пустые редкие категории не показываются в легенде.
    await expect(page.locator('[data-modules-state="unloaded"]')).toBeHidden();
    await expect(page.locator('[data-count="problems"]')).toHaveText("6");
    expect(await page.locator('.mods-tile[data-category="loading"]').first().evaluate((el) => getComputedStyle(el, "::after").animationName)).toBe("mods-shimmer");
    await page.waitForTimeout(500);
    // Рабочая область прокручивается сама, поэтому для снимка целиком вытягиваем окно.
    await page.setViewportSize({ width, height: width === 390 ? 4200 : 1900 });
    await page.screenshot({ path: testInfo.outputPath(`modules-${width}.png`) });
    await page.setViewportSize({ width, height: 900 });

    // Карточка модуля открывается по плитке и показывает полный текст ошибки.
    await page.locator('.mods-tile[data-id="module-9"]').click();
    const inspector = page.locator("#mods-inspector");
    await expect(inspector).toBeVisible();
    await expect(page.locator("#mods-inspector-name")).toHaveText("TelegramTranslator9");
    await expect(page.locator("#mods-inspector-error")).toContainText("missing_dependency");
    await expect(page.locator("#mods-inspector-log")).toHaveText("Найти ошибку в журнале ↗");
    await page.waitForTimeout(450);
    await page.screenshot({ path: testInfo.outputPath(`modules-inspector-${width}.png`) });
    await page.keyboard.press("Escape");
    await expect(inspector).toBeHidden();

    await page.locator("#modules-search").fill("TelegramTranslator");
    await expect(page.locator(".mods-tile:visible")).toHaveCount(8);
    await expect(page.locator("#modules-reset")).toBeVisible();
    await page.locator("#modules-reset").click();
    await expect(page.locator(".mods-tile:visible")).toHaveCount(48);
    expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)).toBe(false);
  });

test("секундный опрос сохраняет плитки, ведёт ленту изменений и не исполняет разметку", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await fixture(page);
  let hits = 0;
  let state = "loading";
  await page.route("**/api/modules", (route) => {
    hits++;
    return route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        status: "live",
        session: "one",
        message: "Каждую секунду",
        modules: [
          { id: "weather", name: "Weather", kind: "external", state, version: "1.0", error: "" },
          { id: "bad", name: "<img src=x onerror=alert(1)>", kind: "core", state: "error", error: "<script>oops</script>" },
        ],
      }),
    });
  });
  await page.clock.install();
  await page.goto("/");
  await expect(page.locator("#username")).toHaveText("tester");
  await page.locator('[data-view="modules"]').click();
  const tile = page.locator('.mods-tile[data-id="weather"]');
  await expect(tile).toHaveAttribute("data-state", "loading");
  await tile.evaluate((element) => (element.dataset.persistent = "yes"));
  state = "ready";
  const before = hits;
  await page.clock.runFor(2200);
  await expect.poll(() => hits).toBeGreaterThanOrEqual(before + 2);
  await expect(tile).toHaveAttribute("data-state", "ready");
  await expect(tile).toHaveAttribute("data-persistent", "yes");
  await expect(page.locator("#mods-feed")).toBeVisible();
  await expect(page.locator("#mods-feed-list li").first()).toContainText("Загружается → Работает");
  await expect(page.locator("#modules-view img, #modules-view script")).toHaveCount(0);
  await expect(page.locator('.mods-tile[data-id="bad"] strong')).toHaveText("<img src=x onerror=alert(1)>");

  await page.locator('[data-modules-state="problems"]').click();
  await expect(tile).toBeHidden();
  await expect(page.locator('.mods-tile[data-id="bad"]')).toBeVisible();
  await page.locator("#modules-search").fill("missing");
  await expect(page.locator("#modules-empty")).toContainText("По этим фильтрам");
  await page.locator("#modules-search").press("/");
  await expect(page.locator("#modules-view")).toBeVisible();
  await page.locator('[data-view="logs"]').click();
  const idle = hits;
  await page.clock.runFor(2200);
  expect(hits).toBe(idle);
});

test("сводка учитывает выгруженные модули и неизвестные состояния", async ({ page }) => {
  await fixture(page);
  let modules = Array.from({ length: 40 }, (_, index) => ({
    id: `inventory-${index}`,
    name: `Module${index}`,
    kind: "external",
    state: index < 38 ? "ready" : "unloaded",
  }));
  await serve(page, () => ({ status: "live", session: "inventory", message: "Подключено", modules }));
  await page.clock.install();
  await page.goto("/");
  await expect(page.locator("#username")).toHaveText("tester");
  await page.locator('[data-view="modules"]').click();
  await expect(page.locator("#mods-headline")).toHaveText("38 из 40 модулей работают");
  await expect(page.locator('[data-count="ready"]')).toHaveText("38");
  await expect(page.locator('[data-count="unloaded"]')).toHaveText("2");
  await expect(page.locator("#mods-attention")).toBeHidden();
  await page.locator('[data-modules-state="unloaded"]').click();
  await expect(page.locator(".mods-tile:visible")).toHaveCount(2);
  await page.locator("#modules-reset").click();
  await expect(page.locator(".mods-tile:visible")).toHaveCount(40);

  modules = ["ready", "loading", "error", "suspended", "unloaded", "future-state"].map((state, index) => ({
    id: `mixed-${index}`,
    name: `Mixed${index}`,
    kind: "core",
    state,
  }));
  await page.clock.runFor(1100);
  await expect(page.locator('[data-count="problems"]')).toHaveText("2");
  await expect(page.locator('[data-count="unknown"]')).toHaveText("1");
  const sum = await page.locator(".mods-legend strong").evaluateAll((elements) => elements.reduce((value, element) => value + Number(element.textContent), 0));
  expect(sum).toBe(6);
  await page.locator('[data-modules-state="unknown"]').click();
  await expect(page.locator(".mods-tile:visible")).toHaveCount(1);
  await expect(page.locator(".mods-tile:visible .mods-state span")).toHaveText("Неизвестно");

  modules = Array.from({ length: 12 }, (_, index) => ({ id: `ok-${index}`, name: `Ok${index}`, kind: "external", state: "ready" }));
  await page.clock.runFor(1100);
  await expect(page.locator("#mods-headline")).toHaveText("Все 12 модулей работают");
  await expect(page.locator("#modules-view")).toHaveAttribute("data-health", "ok");
});

test("потеря связи и новый запуск не оставляют старые зелёные статусы", async ({ page }) => {
  await fixture(page);
  let session = "one";
  let fail = false;
  await page.route("**/api/modules", (route) =>
    fail
      ? route.abort()
      : route.fulfill({
          contentType: "application/json",
          body: JSON.stringify({
            status: "live",
            session,
            message: "Подключено",
            modules: session === "one" ? [{ id: "old", name: "Old", kind: "core", state: "ready" }] : [{ id: "new", name: "New", kind: "core", state: "loading" }],
          }),
        }),
  );
  await page.clock.install();
  await page.goto("/");
  await expect(page.locator("#username")).toHaveText("tester");
  await page.locator('[data-view="modules"]').click();
  await expect(page.locator('.mods-tile[data-id="old"]')).toBeVisible();
  fail = true;
  await page.clock.runFor(1100);
  await expect(page.locator("#modules-connection")).toHaveAttribute("data-status", "stale");
  await expect(page.locator("#mods-catalog")).toHaveClass(/modules-stale/);
  await expect(page.locator("#mods-subline")).toContainText("Последний снимок");
  fail = false;
  session = "two";
  await page.clock.runFor(1100);
  await expect(page.locator('.mods-tile[data-id="old"]')).toHaveCount(0);
  await expect(page.locator('.mods-tile[data-id="new"]')).toBeVisible();
  // Первый снимок нового запуска рисуется сразу, без анимации появления.
  await expect(page.locator('.mods-tile[data-id="new"]')).not.toHaveClass(/entering/);
  await expect(page.locator("#mods-feed")).toBeHidden();
});

test("модуль с ошибкой открывает журнал на своём логгере и последней ошибке", async ({ page }) => {
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/events") return route.abort();
    const data =
      path === "/api/auth/me"
        ? { username: "tester", role: "viewer" }
        : path === "/api/status"
          ? { running: true, pid: 42, uptime: "1ч" }
          : path === "/api/logs"
            ? {
                lines: [
                  "2026-10-10 10:00:00 [INFO] heroku.modules.weather: loading",
                  "2026-10-10 10:00:01 [ERROR] heroku.modules.weather: init failed",
                  "Traceback (most recent call last):",
                  "2026-10-10 10:00:02 [INFO] heroku.core: ready",
                ],
              }
            : path === "/api/modules"
              ? {
                  status: "live",
                  session: "s",
                  modules: [
                    { id: "w", name: "Weather", kind: "external", state: "error", error: "ValueError: bad" },
                    { id: "n", name: "Notes", kind: "external", state: "ready", error: "" },
                  ],
                }
              : {};
    return route.fulfill({ contentType: "application/json", body: JSON.stringify(data) });
  });
  await page.goto("/");
  await page.locator('[data-view="modules"]').click();
  // Однострочная ошибка не дублируется раскрывашкой.
  await expect(page.locator(".mods-issue .mods-issue-trace")).toBeHidden();
  await page.locator('.mods-issue [data-issue="log"]').click();
  await expect(page.locator("#logs-view")).toBeVisible();
  await expect(page.locator("#module-filter")).toHaveValue("heroku.modules.weather");
  await expect(page.locator("#filter")).toHaveValue("");
  await expect(page.locator("#log .line")).toHaveCount(3);
  await expect(page.locator("#log .line.continuation")).toHaveText("Traceback (most recent call last):");
  await expect(page.locator('#log .line[data-level="ERROR"]')).toHaveClass(/focused/);

  // Исправный модуль открывает журнал из своей карточки без поиска ошибки.
  await page.locator('[data-view="modules"]').click();
  await page.locator('.mods-tile[data-id="n"]').click();
  await expect(page.locator("#mods-inspector-log")).toHaveText("Открыть журнал ↗");
  await page.locator("#mods-inspector-log").click();
  await expect(page.locator("#logs-view")).toBeVisible();
  await expect(page.locator("#mods-inspector")).toBeHidden();
});
