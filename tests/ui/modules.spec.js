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
    return route.fulfill({
      contentType: "application/json",
      body: JSON.stringify(data),
    });
  });
}

for (const width of [390, 1440])
  for (const theme of ["dark", "light"]) {
    test(`сетка модулей, страницы и плавное раскрытие ${width}px ${theme}`, async ({
      page,
    }, testInfo) => {
      await page.setViewportSize({ width, height: 900 });
      await page.emulateMedia({ reducedMotion: "no-preference" });
      await fixture(page);
      await page.addInitScript(
        (value) => localStorage.setItem("hkc-theme", value),
        theme,
      );
      const modules = Array.from({ length: 48 }, (_, index) => ({
        id: `module-${index}`,
        name:
          [
            "Weather",
            "HerokuSettings",
            "AccountSwitcher",
            "TelegramTranslator",
            "MediaDownloader",
            "AutoResponder",
          ][index % 6] + index,
        kind: index % 2 ? "external" : "core",
        version: "1.2.0",
        state:
          index % 9 === 0 ? "error" : index % 7 === 0 ? "loading" : "ready",
        error:
          index % 9 === 0
            ? "ModuleNotFoundError: missing_dependency\nНе удалось завершить инициализацию модуля."
            : "",
      }));
      await page.route("**/api/modules", (route) =>
        route.fulfill({
          contentType: "application/json",
          body: JSON.stringify({
            status: "live",
            session: "grid",
            message: "Состояние загрузчика · обновление раз в секунду",
            modules,
          }),
        }),
      );
      await page.goto("/");
      await page.locator('[data-view="modules"]').click();
      await expect(page.locator(".module-row:visible")).toHaveCount(
        width === 390 ? 6 : 12,
      );
      await expect(page.locator(".modules-health")).toBeVisible();
      await expect(page.locator("#modules-page")).toContainText(
        width === 390 ? "1 / 8" : "1 / 4",
      );
      expect(
        await page.locator("#modules-list").evaluate((el) =>
          getComputedStyle(el).gridTemplateColumns.split(" ").length,
        ),
      ).toBe(width === 390 ? 1 : 2);
      const row = page.locator('[data-id="module-0"]');
      await expect(row).toHaveAttribute("data-category", "problems");
      await row.scrollIntoViewIfNeeded();
      const collapsed = await row.evaluate(
        (el) => el.getBoundingClientRect().height,
      );
      await row.locator("summary").click();
      await expect(row.locator("summary")).toHaveAttribute(
        "aria-expanded",
        "true",
      );
      expect(
        await row.evaluate((el) =>
          el
            .getAnimations()
            .some((animation) =>
              animation.effect.getKeyframes().some((frame) => frame.height),
            ),
        ),
      ).toBe(true);
      await expect
        .poll(() =>
          row.evaluate(
            (el) =>
              el
                .getAnimations()
                .filter((animation) =>
                  animation.effect.getKeyframes().some((frame) => frame.height),
                ).length,
          ),
        )
        .toBe(0);
      const expanded = await row.evaluate(
        (el) => el.getBoundingClientRect().height,
      );
      expect(expanded).toBeGreaterThan(collapsed + 30);
      expect(
        await page
          .locator('.modules-matrix [data-state="loading"]')
          .first()
          .evaluate((el) => getComputedStyle(el).animationName),
      ).toContain("module-loading-wave");
      await page.screenshot({
        path: testInfo.outputPath(`modules-grid-${width}-${theme}.png`),
        fullPage: true,
      });
      await row.locator("summary").click();
      await expect(row.locator("summary")).toHaveAttribute(
        "aria-expanded",
        "false",
      );
      expect(
        await row.evaluate((el) =>
          el
            .getAnimations()
            .some((animation) =>
              animation.effect.getKeyframes().some((frame) => frame.height),
            ),
        ),
      ).toBe(true);
      await expect(row).not.toHaveAttribute("open", "");
      expect(
        await row.evaluate((el) => el.getBoundingClientRect().height),
      ).toBeCloseTo(collapsed, 0);
      await page.locator("#modules-next").click();
      await expect(page.locator("#modules-page")).toContainText(
        width === 390 ? "2 / 8" : "2 / 4",
      );
      await page.locator("#modules-search").fill("TelegramTranslator");
      await expect(page.locator("#modules-page")).toContainText("1 /");
      expect(
        await page.evaluate(
          () => document.documentElement.scrollWidth > innerWidth + 1,
        ),
      ).toBe(false);
      await page.emulateMedia({ reducedMotion: "reduce" });
      const remaining = page.locator(".module-row:visible").first();
      await remaining.locator("summary").click();
      await expect(remaining).toHaveAttribute("open", "");
      expect(await remaining.evaluate((el) => el.getAnimations().length)).toBe(
        0,
      );
    });
  }

test("секундный опрос сохраняет строки, раскрытие и фильтры", async ({
  page,
}) => {
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
          {
            id: "weather",
            name: "Weather",
            kind: "external",
            state,
            version: "1.0",
            error: "",
          },
          {
            id: "bad",
            name: "<img src=x onerror=alert(1)>",
            kind: "core",
            state: "error",
            error: "<script>oops</script>",
          },
        ],
      }),
    });
  });
  await page.clock.install();
  await page.goto("/");
  await expect(page.locator("#username")).toHaveText("tester");
  await page.locator('[data-view="modules"]').click();
  const row = page.locator('[data-id="weather"]');
  await expect(row).toBeVisible();
  expect(await row.evaluate((el) => getComputedStyle(el).animationName)).toBe(
    "module-enter",
  );
  await row.locator("summary").click();
  await row.evaluate((element) => {
    element.dataset.persistent = "yes";
  });
  await page
    .locator("#modules-matrix i")
    .first()
    .evaluate((element) => {
      element.dataset.persistent = "yes";
    });
  state = "ready";
  const before = hits;
  await page.clock.runFor(2200);
  await expect.poll(() => hits).toBeGreaterThanOrEqual(before + 2);
  await expect(row).toHaveAttribute("data-state", "ready");
  await expect(row).toHaveAttribute("open", "");
  await expect(row).toHaveAttribute("data-persistent", "yes");
  await expect(page.locator("#modules-matrix i").first()).toHaveAttribute(
    "data-persistent",
    "yes",
  );
  await expect(page.locator("#modules-matrix i").first()).toHaveAttribute(
    "data-state",
    "ready",
  );
  await expect(
    page.locator("#modules-list img, #modules-list script"),
  ).toHaveCount(0);
  await page.locator("#modules-only-problems").click();
  await expect(row).toBeHidden();
  await expect(page.locator('[data-id="bad"]')).toBeVisible();
  await page.locator("#modules-search").fill("missing");
  await expect(page.locator("#modules-empty")).toContainText(
    "По этим фильтрам",
  );
  await page.locator("#modules-search").press("/");
  await expect(page.locator("#modules-view")).toBeVisible();
  await page.locator('[data-view="logs"]').click();
  const idle = hits;
  await page.clock.runFor(2200);
  expect(hits).toBe(idle);
});

test("сводка учитывает выгруженные модули и неизвестные состояния", async ({
  page,
}) => {
  await fixture(page);
  let modules = Array.from({ length: 40 }, (_, index) => ({
    id: `inventory-${index}`,
    name: `Module${index}`,
    kind: "external",
    state: index < 38 ? "ready" : "unloaded",
  }));
  await page.route("**/api/modules", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        status: "live",
        session: "inventory",
        message: "Подключено",
        modules,
      }),
    }),
  );
  await page.clock.install();
  await page.goto("/");
  await expect(page.locator("#username")).toHaveText("tester");
  await page.locator('[data-view="modules"]').click();
  await expect(page.locator("#modules-total")).toHaveText("40");
  await expect(page.locator("#modules-ready")).toHaveText("38");
  await expect(page.locator("#modules-unloaded")).toHaveText("2");
  await expect(page.locator("#modules-summary")).toHaveText(
    "38 из 40 готовы к работе",
  );
  await expect(
    page.locator('.modules-matrix [data-state="unloaded"]'),
  ).toHaveCount(2);
  await page.locator('[data-modules-state="unloaded"]').click();
  await expect(page.locator(".module-row:visible")).toHaveCount(2);
  await expect(page.locator('[data-id="inventory-38"]')).toBeVisible();
  await page.locator("#modules-reset").click();
  await expect(page.locator(".module-row:visible")).toHaveCount(12);
  modules = [
    "ready",
    "loading",
    "error",
    "suspended",
    "unloaded",
    "future-state",
  ].map((state, index) => ({
    id: `mixed-${index}`,
    name: `Mixed${index}`,
    kind: "core",
    state,
  }));
  await page.clock.runFor(1100);
  await expect(page.locator("#modules-total")).toHaveText("6");
  await expect(page.locator("#modules-problems")).toHaveText("2");
  await expect(page.locator("#modules-unknown")).toHaveText("1");
  const sum = await page
    .locator(".modules-counters strong")
    .evaluateAll((elements) =>
      elements.reduce(
        (value, element) => value + Number(element.textContent),
        0,
      ),
    );
  expect(sum).toBe(6);
  await page.locator('[data-modules-state="unknown"]').click();
  await expect(page.locator(".module-row:visible")).toHaveCount(1);
  await expect(
    page.locator(".module-row:visible .module-state span"),
  ).toHaveText("Неизвестно");
  await page.locator('[data-modules-state="unknown"]').click();
  await expect(page.locator(".module-row:visible")).toHaveCount(6);
  modules = Array.from({ length: 81 }, (_, index) => ({
    id: `large-${index}`,
    name: `Large${index}`,
    kind: "external",
    state: index === 80 ? "unloaded" : "ready",
  }));
  await page.clock.runFor(1100);
  await expect(page.locator("#modules-total")).toHaveText("81");
  await expect(page.locator("#modules-ready")).toHaveText("80");
  await expect(page.locator("#modules-unloaded")).toHaveText("1");
  await expect(page.locator("#modules-matrix i")).toHaveCount(60);
  await expect(page.locator("#modules-map-caption")).toHaveText(
    "Первые 60 из 81",
  );
});

test("потеря связи и новый запуск не оставляют старые зелёные статусы", async ({
  page,
}) => {
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
            modules:
              session === "one"
                ? [{ id: "old", name: "Old", kind: "core", state: "ready" }]
                : [{ id: "new", name: "New", kind: "core", state: "loading" }],
          }),
        }),
  );
  await page.clock.install();
  await page.goto("/");
  await expect(page.locator("#username")).toHaveText("tester");
  await page.locator('[data-view="modules"]').click();
  await expect(page.locator('[data-id="old"]')).toBeVisible();
  fail = true;
  await page.clock.runFor(1100);
  await expect(page.locator("#modules-connection")).toHaveAttribute(
    "data-status",
    "stale",
  );
  await expect(page.locator("#modules-list")).toHaveClass(/modules-stale/);
  await expect(page.locator("#modules-summary")).toContainText(
    "Последний снимок",
  );
  fail = false;
  session = "two";
  await page.clock.runFor(1100);
  await expect(page.locator('[data-id="old"]')).toHaveCount(0);
  await expect(page.locator('[data-id="new"]')).toBeVisible();
  expect(
    await page
      .locator('[data-id="new"]')
      .evaluate((el) => getComputedStyle(el).animationName),
  ).toBe("none");
});
