const { test, expect } = require("@playwright/test");
const { createJournalApi } = require("./journal-fixture");

const pageErrors = new WeakMap();

test.afterEach(async ({ page }) => {
  expect(pageErrors.get(page) || [], "Неожиданные ошибки браузера").toEqual([]);
});

// Подменяет API панели. Возвращает управление фикстурой журнала и потоком.
async function openJournal(page, lines, { stats, stream } = {}) {
  const errors = [];
  pageErrors.set(page, errors);
  page.on("pageerror", (error) => errors.push(error.message));
  const api = createJournalApi(lines);
  await page.route("**/api/**", async (route) => {
    const url = route.request().url();
    const path = new URL(url).pathname;
    if (path === "/api/events") return route.abort();
    if (path === "/api/logs/stream") {
      if (!stream) return route.abort();
      return route.fulfill({ status: 200, contentType: "text/event-stream", body: stream() });
    }
    if (path === "/api/logs/stats" && stats) {
      api.calls.push(`${path}`);
      return route.fulfill({ contentType: "application/json", body: JSON.stringify(stats(api)) });
    }
    const journal = api.handle(url);
    if (journal !== undefined)
      return route.fulfill({ status: journal === null ? 404 : 200, contentType: "application/json", body: JSON.stringify(journal ?? {}) });
    const data =
      path === "/api/auth/me"
        ? { username: "tester", role: "operator" }
        : path === "/api/status"
          ? { running: true, pid: 1, uptime: "1ч", logReady: true }
          : {};
    return route.fulfill({ contentType: "application/json", body: JSON.stringify(data) });
  });
  await page.goto("/");
  await page.locator('[data-view="logs"]').click();
  return api;
}

const line = (i, level = "INFO", module = "Core", message = `событие ${i}`) => {
  const t = new Date(Date.UTC(2026, 9, 3, 10, 0, 0) + i * 1000);
  return `${t.toISOString().slice(0, 19).replace("T", " ")} [${level}] ${module}: ${message}`;
};

test("записи собраны: traceback внутри, повторы схлопнуты, поиск подсвечивает", async ({ page }) => {
  const lines = [
    line(0, "INFO", "Core", "started"),
    line(1, "ERROR", "updates", "manifest failed"),
    "Traceback (most recent call last):",
    '  File "u.py", line 3',
    line(2, "WARNING", "net", "slow reply"),
    line(3, "WARNING", "net", "slow reply"),
    line(4, "WARNING", "net", "slow reply"),
  ];
  await openJournal(page, lines);
  // Три одинаковых предупреждения — одна карточка со счётчиком.
  await expect(page.locator("#log .jr-card")).toHaveCount(3);
  await expect(page.locator('#log .jr-card[data-level="WARNING"] .jr-rep')).toHaveText(/×3/);
  await expect(page.locator("#line-count")).toHaveText("5");
  // Traceback — часть записи, а не отдельная строка ленты.
  const error = page.locator('#log .jr-card[data-level="ERROR"]');
  await expect(error.locator(".jr-trace")).toContainText("Traceback · 2 строки");
  await expect(error.locator(".jr-trace pre")).toBeHidden();
  await error.locator(".jr-trace summary").click();
  await expect(error.locator(".jr-trace pre")).toContainText('File "u.py"');

  await page.locator("#filter").fill("manifest");
  await expect(page.locator("#log .jr-card")).toHaveCount(1);
  await expect(page.locator("#log .jr-message mark")).toHaveText("manifest");
  // Поиск находит и текст внутри traceback.
  await page.locator("#filter").fill("u.py");
  await expect(page.locator("#log .jr-card")).toHaveCount(1);
  await expect(page.locator("#log .jr-trace pre")).toBeVisible();
});

test("запрос level:/module: превращается в чипы, Backspace и Esc снимают их", async ({ page }) => {
  await openJournal(page, [line(0, "INFO", "Core", "a"), line(1, "ERROR", "updates", "b"), line(2, "ERROR", "Core", "c")]);
  await page.locator("#filter").fill("level:error ");
  await expect(page.locator("#query-chips .jr-chip")).toHaveCount(1);
  await expect(page.locator("#filter")).toHaveValue("");
  await expect(page.locator("#log .jr-card")).toHaveCount(2);
  await page.locator("#filter").pressSequentially("module:Core ");
  await expect(page.locator("#query-chips .jr-chip")).toHaveCount(2);
  await expect(page.locator("#log .jr-card")).toHaveCount(1);
  // Клик по модулю в карточке — тот же фильтр; Backspace в пустом поле снимает последний чип.
  await page.locator("#filter").press("Backspace");
  await expect(page.locator("#query-chips .jr-chip")).toHaveCount(1);
  await page.locator("#filter").press("Backspace");
  await expect(page.locator("#query-chips .jr-chip")).toHaveCount(0);
  await expect(page.locator("#log .jr-card")).toHaveCount(3);
  await page.locator(".jr-module", { hasText: "updates" }).click();
  await expect(page.locator("#log .jr-card")).toHaveCount(1);
  await page.locator("#query-chips .jr-chip button").click();
  await expect(page.locator("#log .jr-card")).toHaveCount(3);
  await page.locator("#filter").fill("zzz");
  await expect(page.locator(".log-empty")).toContainText("Ничего не найдено");
  await page.locator("#filter").press("Escape");
  await expect(page.locator("#log .jr-card")).toHaveCount(3);
});

test("панель деталей показывает окружение записи и закрывается по Esc", async ({ page }) => {
  await openJournal(page, [line(0, "INFO", "Core", "первая"), line(1, "ERROR", "Core", "ошибка"), line(2, "INFO", "Core", "третья")]);
  await page.locator('#log .jr-card[data-level="ERROR"]').click();
  const drawer = page.locator("#log-drawer");
  await expect(drawer).toBeVisible();
  await expect(drawer.locator(".jr-full")).toContainText("[ERROR] Core: ошибка");
  await expect(drawer.locator(".jr-ctx-row")).toHaveCount(3);
  await expect(drawer.locator(".jr-ctx-row.cur")).toContainText("ошибка");
  await page.keyboard.press("Escape");
  await expect(drawer).toBeHidden();
});

test("старые записи подгружаются при прокрутке без потерь и дублей, даже с одинаковой секундой", async ({ page }) => {
  const lines = [];
  // 460 записей, каждая третья секунда повторяется: курсор не должен терять границу страницы.
  for (let i = 0; i < 460; i++) lines.push(line(Math.floor(i / 2), "INFO", `m${i % 7}`, `запись ${i}`));
  const api = await openJournal(page, lines);
  await expect(page.locator("#line-count")).toHaveText("200+");
  const feed = page.locator("#log");
  for (let attempt = 0; attempt < 6; attempt++) {
    await feed.evaluate((node) => (node.scrollTop = 0));
    if ((await page.locator("#line-count").textContent()) === "460") break;
    await page.waitForTimeout(150);
  }
  await expect(page.locator("#line-count")).toHaveText("460");
  expect(api.calls.filter((call) => call.startsWith("/api/logs/entries")).length).toBeGreaterThanOrEqual(3);
  const texts = await page.locator("#log .jr-message").allTextContents();
  expect(new Set(texts).size).toBe(460);
  expect(texts[0]).toBe("запись 0");
  expect(texts.at(-1)).toBe("запись 459");
});

test("запись потока не дублирует историю и добавляется карточкой", async ({ page }) => {
  const lines = [line(0, "INFO", "Core", "старая"), line(1, "INFO", "Core", "последняя в истории")];
  // Поток начинается с записи, уже попавшей в историю, — как при гонке подключения и загрузки.
  const entry = (i, message) => ({ id: `s${i}`, ts: Date.parse(`2026-10-03T10:00:0${i}Z`), time: `2026-10-03 10:00:0${i}`, level: "INFO", module: "Core", message });
  await openJournal(page, lines, {
    stream: () => `event: state\ndata: {"alive":true,"reason":""}\n\ndata: ${JSON.stringify({ ...entry(1, "последняя в истории") })}\n\ndata: ${JSON.stringify(entry(2, "новая из потока"))}\n\n`,
  });
  await expect(page.locator("#log .jr-message").last()).toHaveText("новая из потока");
  await expect(page.locator("#log .jr-card")).toHaveCount(3);
});

test("диапазон на шкале: быстрые кнопки, чип времени и сброс", async ({ page }) => {
  await openJournal(page, [line(0), line(1)]);
  await expect(page.locator("#tl-reset")).toBeHidden();
  await page.locator('[data-tl-range="60"]').click();
  await expect(page.locator("#query-chips")).toContainText("сейчас");
  await expect(page.locator("#tl-reset")).toBeVisible();
  // Записи прошлого года вне диапазона последнего часа.
  await expect(page.locator(".log-empty")).toContainText("Ничего не найдено");
  await page.locator("#tl-reset").click();
  await expect(page.locator("#query-chips .jr-chip")).toHaveCount(0);
  await expect(page.locator("#log .jr-card")).toHaveCount(2);
});

test("закладки сохраняются и показываются отдельным режимом", async ({ page }) => {
  await openJournal(page, [line(0, "INFO", "Core", "a"), line(1, "ERROR", "Core", "b")]);
  await page.locator('#log .jr-card[data-level="ERROR"]').hover();
  await page.locator('#log .jr-card[data-level="ERROR"] .jr-mark').click();
  await page.locator("#bookmarks-only").click();
  await expect(page.locator("#log .jr-card")).toHaveCount(1);
  await expect(page.locator("#log .jr-message")).toHaveText("b");
  await page.reload();
  await page.locator('[data-view="logs"]').click();
  await page.locator("#bookmarks-only").click();
  await expect(page.locator("#log .jr-card")).toHaveCount(1);
  await expect(page.locator("#log .jr-message")).toHaveText("b");
});

test("«Очистить» скрывает записи до текущего момента, файл не меняется, чип возвращает всё", async ({ page }) => {
  const api = await openJournal(page, [line(0), line(1, "ERROR", "Core", "ошибка")]);
  await expect(page.locator("#log .jr-card")).toHaveCount(2);
  await page.locator("#clear-log").click();
  await expect(page.locator("#log .jr-card")).toHaveCount(0);
  await expect(page.locator(".log-empty")).toContainText("Экран очищен");
  await expect(page.locator("#query-chips")).toContainText("очищено");
  // Очистка запоминается между перезагрузками страницы.
  await page.reload();
  await page.locator('[data-view="logs"]').click();
  await expect(page.locator("#query-chips")).toContainText("очищено");
  await expect(page.locator("#log .jr-card")).toHaveCount(0);
  // Запросы к серверу несут нижнюю границу времени, а не удаляют данные.
  expect(api.calls.some((call) => call.startsWith("/api/logs/entries") && call.includes("from="))).toBe(true);
  await page.locator("#query-chips .jr-chip button").click();
  await expect(page.locator("#query-chips .jr-chip")).toHaveCount(0);
  await expect(page.locator("#log .jr-card")).toHaveCount(2);
});

test("«Очистить» отправляет целые миллисекунды даже при подстроенных под сервер часах", async ({ page }) => {
  // Часы шкалы сдвинуты на дробное число миллисекунд, как после сглаживания расхождения с сервером.
  const api = await openJournal(page, [line(0), line(1)], {
    stats: () => ({ bucketMs: 300000, now: Date.now() + 100.7, modules: { Core: 2 }, total: 0, buckets: [] }),
  });
  await page.waitForTimeout(300);
  await page.locator("#clear-log").click();
  await expect(page.locator("#log .jr-card")).toHaveCount(0);
  const withFrom = api.calls.filter((call) => call.startsWith("/api/logs/entries") && call.includes("from="));
  expect(withFrom.length).toBeGreaterThan(0);
  for (const call of withFrom) expect(call, call).toMatch(/from=\d+(&|$)/);
});

test("страница журнала не уходит за ширину экрана на телефоне", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 800 });
  await openJournal(page, [line(0), line(1, "ERROR", "updates", "длинное сообщение ".repeat(12))]);
  await expect(page.locator("#log .jr-card")).toHaveCount(2);
  expect(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth + 1)).toBe(false);
  await page.locator('#log .jr-card[data-level="ERROR"]').click();
  await expect(page.locator("#log-drawer")).toBeVisible();
});

test.describe("плавность шкалы", () => {
  // В остальных тестах движение отключено; здесь нужна настоящая анимация.
  test.use({ reducedMotion: "no-preference" });

  test("столбик растёт по кадрам, а не прыгает", async ({ page }) => {
    // Сервер отдаёт «1 ошибку», пока тест не включит рост: число опросов при загрузке не фиксировано.
    let boost = false;
    const now = Date.now();
    const bucket = 300000;
    const current = Math.floor(now / bucket) * bucket;
    await openJournal(page, [line(0)], {
      stats: () => {
        return {
          bucketMs: bucket,
          now: Date.now(),
          modules: { Core: 1 },
          total: 0,
          buckets: [
            { t: current - bucket, info: 3, warn: 0, err: 0 },
            // Одна ошибка, затем двенадцать: столбик должен подтянуться, а не перескочить.
            { t: current, info: 0, warn: 0, err: boost ? 12 : 1 },
          ],
        };
      },
    });
    await expect(page.locator("#timeline canvas")).toBeVisible();
    await page.waitForTimeout(900);
    boost = true;

    // Высота правого столбика в пикселях canvas по кадрам после нового ответа сервера.
    const heights = await page.evaluate(async () => {
      const canvas = document.querySelector("#timeline canvas");
      const g = canvas.getContext("2d", { willReadFrequently: true });
      const x = canvas.width - 3;
      const top = () => {
        const column = g.getImageData(x, 0, 1, canvas.height).data;
        for (let y = 0; y < canvas.height; y++) if (column[y * 4 + 3] > 40) return canvas.height - y;
        return 0;
      };
      document.querySelector('.filter-chip[data-level="ERROR"]').click();
      const samples = [];
      const started = performance.now();
      await new Promise((resolve) => {
        const tick = () => {
          samples.push(top());
          if (performance.now() - started > 1200) resolve();
          else requestAnimationFrame(tick);
        };
        requestAnimationFrame(tick);
      });
      return samples;
    });
    const distinct = [...new Set(heights)];
    // Было несколько промежуточных значений, и ряд не убывал: рост плавный.
    expect(distinct.length, `высоты по кадрам: ${heights.join(",")}`).toBeGreaterThan(6);
    for (let i = 1; i < heights.length; i++) expect(heights[i], `кадр ${i}: ${heights.join(",")}`).toBeGreaterThanOrEqual(heights[i - 1] - 1);
    // Самый большой скачок между соседними кадрами — малая доля всей высоты.
    const final = heights.at(-1);
    const maxStep = Math.max(...heights.slice(1).map((value, i) => value - heights[i]));
    expect(maxStep).toBeLessThan(final * 0.4);
  });
});
