const { test, expect } = require("@playwright/test");
const { createJournalApi } = require("./journal-fixture");

const pageErrors = new WeakMap();

test.afterEach(async ({ page }) => {
  expect(pageErrors.get(page) || [], "Неожиданные ошибки браузера").toEqual([]);
});

// Подменяет API панели. Возвращает управление фикстурой журнала и потоком.
async function openJournal(page, lines, { stats, stream, role = "operator", levelsAvailable = true } = {}) {
  const errors = [];
  pageErrors.set(page, errors);
  page.on("pageerror", (error) => errors.push(error.message));
  const api = createJournalApi(lines, { levelsAvailable });
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
    const journal = api.handle(url, { method: route.request().method(), body: route.request().postData() });
    if (journal !== undefined)
      return route.fulfill({ status: journal === null ? 404 : 200, contentType: "application/json", body: JSON.stringify(journal ?? {}) });
    const data =
      path === "/api/auth/me"
        ? { username: "tester", role }
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
  await expect(error.locator(".jr-trace")).toContainText("Traceback");
  await expect(error.locator(".jr-trace")).toContainText("2 строки");
  // Разбор traceback строится при раскрытии, а не для каждой записи ленты.
  await expect(error.locator(".jr-tv")).toHaveCount(0);
  await error.locator(".jr-trace summary").click();
  await expect(error.locator(".jr-fr-loc")).toContainText("u.py");

  await page.locator("#filter").fill("manifest");
  await expect(page.locator("#log .jr-card")).toHaveCount(1);
  await expect(page.locator("#log .jr-message mark")).toHaveText("manifest");
  // Поиск находит и текст внутри traceback.
  await page.locator("#filter").fill("u.py");
  await expect(page.locator("#log .jr-card")).toHaveCount(1);
  await expect(page.locator("#log .jr-tv")).toBeVisible();
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
    const bucket = 300000;
    // Часы страницы стоят в середине 5-минутного интервала. В его начале текущий столбик шкалы ещё
    // в пару пикселей шириной, и замер у правого края попадал на соседний интервал.
    const current = Math.floor(Date.now() / bucket) * bucket;
    const now = current + bucket / 2;
    await page.clock.setFixedTime(now);
    await openJournal(page, [line(0)], {
      stats: () => {
        return {
          bucketMs: bucket,
          now,
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
      document.querySelector('#jr-stats [data-tile="err"]').click();
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

// Время записей без пояса, как у бота; фикстура читает его как местное.
const localStamp = (ms) => {
  const d = new Date(ms);
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
};

test("разбор traceback: кадры, цепочка исключений, библиотеки и итоговое исключение", async ({ page }) => {
  await openJournal(page, [line(0)]);
  const parsed = await page.evaluate(async () => {
    const { parseTrace, traceSummary } = await import("/modules/log-trace.js");
    const lines = [
      "Traceback (most recent call last):",
      '  File "/opt/bot/updates.py", line 88, in fetch',
      "    await session.get(URL)",
      "                      ^^^",
      '  File "/usr/lib/python3.12/site-packages/aiohttp/client.py", line 12, in get',
      "    raise OSError",
      '  File "/usr/lib/python3.12/site-packages/aiohttp/base.py", line 5, in _req',
      "ConnectionError: reset by peer",
      "",
      "During handling of the above exception, another exception occurred:",
      "",
      "Traceback (most recent call last):",
      '  File "/opt/bot/main.py", line 3, in <module>',
      "TimeoutError: Таймаут обновления",
    ];
    const result = parseTrace(lines);
    return {
      ok: result.ok,
      blocks: result.blocks.length,
      first: result.blocks[0].frames.map((f) => ({ func: f.func, line: f.line, own: f.own, code: f.code })),
      firstException: result.blocks[0].exception.type,
      note: result.blocks[1].note,
      summary: traceSummary(lines),
      notPython: parseTrace(["что-то", "другое"]).ok,
      longSummary: traceSummary(["Traceback (most recent call last):", '  File "a.py", line 1, in f', `ValueError: ${"x".repeat(300)}`]).length,
    };
  });
  expect(parsed.ok).toBe(true);
  expect(parsed.blocks).toBe(2);
  expect(parsed.first).toEqual([
    { func: "fetch", line: 88, own: true, code: "await session.get(URL)" },
    { func: "get", line: 12, own: false, code: "raise OSError" },
    { func: "_req", line: 5, own: false, code: "" },
  ]);
  expect(parsed.firstException).toBe("ConnectionError");
  expect(parsed.note).toMatch(/During handling/);
  expect(parsed.summary).toBe("TimeoutError: Таймаут обновления");
  expect(parsed.notPython).toBe(false);
  expect(parsed.longSummary).toBeLessThanOrEqual(110);
});

test("traceback в карточке: причина в заголовке, свои кадры видны, библиотеки свёрнуты, копирование", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  const lines = [
    line(0, "ERROR", "updates", "manifest failed"),
    "Traceback (most recent call last):",
    '  File "/opt/bot/updates.py", line 88, in fetch',
    "    await session.get(URL)",
    '  File "/usr/lib/python3.12/site-packages/aiohttp/client.py", line 12, in get',
    '  File "/usr/lib/python3.12/site-packages/aiohttp/base.py", line 5, in _req',
    "ServerTimeoutError: Timeout on reading data",
  ];
  await openJournal(page, lines);
  const card = page.locator('#log .jr-card[data-level="ERROR"]');
  await expect(card.locator(".jr-tr-reason")).toHaveText("ServerTimeoutError: Timeout on reading data");
  await card.locator("summary").click();
  await expect(card.locator(".jr-fr:not(.lib) .jr-fr-func").first()).toHaveText("fetch");
  await expect(card.locator(".jr-fr-code").first()).toHaveText("await session.get(URL)");
  await expect(card.locator(".jr-exc")).toContainText("ServerTimeoutError");
  // Два подряд идущих кадра библиотеки сложены в одну строку-раскрывашку.
  await expect(card.locator(".jr-lib summary")).toHaveText("2 вызова библиотек");
  await expect(card.locator(".jr-lib .jr-fr").first()).toBeHidden();
  await card.locator(".jr-lib summary").click();
  await expect(card.locator(".jr-lib .jr-fr").first()).toBeVisible();
  // Копирование берёт исходный текст traceback целиком; клик не открывает панель деталей.
  await card.locator(".jr-copy").click();
  expect(await page.evaluate(() => navigator.clipboard.readText())).toContain('File "/opt/bot/updates.py", line 88, in fetch');
  await expect(page.locator("#log-drawer")).toBeHidden();
  // Панель деталей показывает тот же разбор.
  await card.locator(".jr-message").click();
  await expect(page.locator("#log-drawer .jr-tv .jr-exc")).toContainText("ServerTimeoutError");
});

test("переходы между ошибками: стрелки, клавиши [ ], перескок по шкале и «Сейчас»", async ({ page }) => {
  const now = Date.now();
  const lines = [
    `${localStamp(now - 5 * 3600e3)} [ERROR] net: первая ошибка`,
    `${localStamp(now - 4.9 * 3600e3)} [INFO] Core: фон`,
    `${localStamp(now - 3 * 3600e3)} [ERROR] net: вторая ошибка`,
    `${localStamp(now - 2.9 * 3600e3)} [WARNING] net: предупреждение`,
    `${localStamp(now - 10 * 60e3)} [ERROR] net: третья ошибка`,
    `${localStamp(now - 5 * 60e3)} [INFO] Core: хвост`,
  ];
  await openJournal(page, lines);
  const navMessage = page.locator("#log .jr-card.nav .jr-message");
  // Итоги по окну на шкале.
  await expect(page.locator('#jr-stats [data-tile="err"] b')).toHaveText("3");
  await expect(page.locator('#jr-stats [data-tile="warn"] b')).toHaveText("1");
  await expect(page.locator('#jr-stats [data-tile="total"] b')).toHaveText("6");
  await expect(page.locator('#jr-stats [data-tile="mod"] b')).toHaveText("net");

  // От середины ленты: «назад» берёт ближайшую выше, повторное — ещё выше, «]» возвращает.
  await page.locator("#err-prev").click();
  await expect(navMessage).toHaveCount(1);
  const first = await navMessage.textContent();
  await page.locator("#err-prev").click();
  await expect(navMessage).not.toHaveText(first);
  await page.keyboard.press("]");
  await expect(navMessage).toHaveText(first);

  // Окно в последние 15 минут: прежних ошибок в ленте нет, переход идёт по шкале.
  await page.locator('[data-tl-range="15"]').click();
  await expect(page.locator("#log .jr-card")).toHaveCount(2);
  await page.locator("#err-prev").click();
  await expect(navMessage).toHaveText("третья ошибка");
  await page.locator("#err-prev").click();
  await expect(page.locator("#query-chips")).toContainText("время");
  await expect(navMessage).toHaveText("вторая ошибка");

  // «Сейчас» снимает диапазон и возвращает живой хвост.
  await page.locator("#tl-now").click();
  await expect(page.locator("#query-chips")).not.toContainText("время");
  await expect(page.locator("#log .jr-card").last()).toContainText("хвост");
  await expect(page.locator("#log .jr-card.nav")).toHaveCount(0);
});

test("у модуля цветная точка, у времени подсказка «сколько назад»", async ({ page }) => {
  const now = Date.now();
  await openJournal(page, [`${localStamp(now - 125 * 1000)} [INFO] weather: запись`, `${localStamp(now - 60 * 1000)} [INFO] net: запись`]);
  const hues = await page.locator("#log .jr-module").evaluateAll((nodes) => nodes.map((n) => n.style.getPropertyValue("--mh")));
  expect(hues).toHaveLength(2);
  expect(hues[0]).not.toBe(hues[1]);
  await page.locator("#log .jr-time").first().hover();
  await expect(page.locator("#log .jr-time").first()).toHaveAttribute("title", /мин назад/);
});

// Журнал за последний час: повторяющиеся таймауты net, одна ошибка updates и фон.
function recentLines(now) {
  const out = [];
  for (let i = 0; i < 40; i++) out.push(`${localStamp(now - (60 - i) * 60e3)} [INFO] ${["Core", "loader", "weather", "reminders", "tts", "db"][i % 6]}: фон ${i}`);
  for (let i = 0; i < 4; i++) out.push(`${localStamp(now - (30 - i * 5) * 60e3 + 500)} [ERROR] net: таймаут Telegram через ${10 + i} с`);
  out.push(`${localStamp(now - 12 * 60e3 + 700)} [ERROR] updates: не удалось получить манифест`);
  out.push(`${localStamp(now - 8 * 60e3 + 900)} [WARNING] net: медленный ответ`);
  const at = (s) => new Date(s.slice(0, 19).replace(" ", "T")).getTime();
  return out.sort((a, b) => at(a) - at(b));
}

test("частые ошибки: группы со счётчиком, клик фильтрует ленту, «Последняя» открывает запись", async ({ page }) => {
  await openJournal(page, recentLines(Date.now()));
  await expect(page.locator("#log .jr-card").first()).toBeVisible();
  await page.locator("#problems-open").click();
  const rows = page.locator("#log-drawer .jr-prob-row");
  await expect(rows).toHaveCount(2);
  await expect(rows.first().locator(".jr-prob-n")).toHaveText("×4");
  await expect(rows.first()).toContainText("таймаут Telegram через 13 с");

  // Клик по группе показывает ровно эти записи; список при этом остаётся.
  await rows.first().click();
  await expect(page.locator("#query-chips")).toContainText("level:error");
  await expect(page.locator("#query-chips")).toContainText("module:net");
  await expect(page.locator("#filter")).toHaveValue("таймаут Telegram через");
  await expect(page.locator("#log .jr-card")).toHaveCount(4);
  await expect(rows).toHaveCount(2);

  // «Последняя» открывает запись, «‹ Частые ошибки» возвращает список.
  await rows.first().locator(".jr-prob-open").click();
  await expect(page.locator("#log-drawer .jr-full")).toContainText("через 13 с");
  await page.locator("#log-drawer .jr-back").click();
  await expect(rows).toHaveCount(2);

  // Вкладка предупреждений и закрытие повторным нажатием.
  await page.locator("#log-drawer .jr-prob-tabs button", { hasText: "Предупреждения" }).click();
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("медленный ответ");
  await page.locator("#problems-open").click();
  await expect(page.locator("#log-drawer")).toBeHidden();
});

test("запрос и открытая запись живут в адресе: перезагрузка и ссылка их восстанавливают", async ({ page, context }) => {
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await openJournal(page, recentLines(Date.now()));
  await page.locator("#filter").fill("level:error module:net таймаут");
  await page.locator("#filter").press("Enter");
  await expect(page.locator("#log .jr-card")).toHaveCount(4);
  await expect(page).toHaveURL(/#logs\?level=ERROR&module=net&q=/);

  await page.reload();
  await expect(page.locator("#query-chips")).toContainText("module:net");
  await expect(page.locator("#filter")).toHaveValue("таймаут");
  await expect(page.locator("#log .jr-card")).toHaveCount(4);

  // Ссылка на запись из панели деталей ведёт прямо к ней, даже без фильтров.
  await page.locator("#log .jr-card .jr-message").nth(1).click();
  await page.locator("#log-drawer .jr-acts button", { hasText: "Ссылка" }).click();
  const link = await page.evaluate(() => navigator.clipboard.readText());
  expect(link).toMatch(/#logs\?entry=\d+~/);
  await page.goto("about:blank");
  await page.goto(link);
  await expect(page.locator("#log-drawer .jr-full")).toContainText("таймаут Telegram через 11 с");
  await expect(page.locator("#log .jr-card.selected .jr-message")).toHaveText("таймаут Telegram через 11 с");
  await expect(page).toHaveURL(/entry=/);

  // Закрытие панели убирает запись из адреса; другой раздел убирает и запрос журнала.
  await page.keyboard.press("Escape");
  await expect(page).not.toHaveURL(/entry=/);
  await page.locator('[data-view="system"]').click();
  await expect(page).not.toHaveURL(/#logs/);
});

test("шапка сворачивается при чтении истории и разворачивается кнопкой или у живого хвоста", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 860 });
  await openJournal(page, Array.from({ length: 120 }, (_, i) => line(i, i % 17 ? "INFO" : "ERROR")));
  await expect(page.locator("#log .jr-card").first()).toBeVisible();
  const view = page.locator("#logs-view");
  await expect(view).not.toHaveClass(/compact/);
  const feedBefore = await page.locator("#log").evaluate((f) => f.clientHeight);
  await page.locator("#log").hover();
  await page.mouse.wheel(0, -900);
  await expect(view).toHaveClass(/compact/);
  await expect(page.locator(".jr-mini")).toBeVisible();
  await expect(page.locator("#jr-stats")).toBeHidden();
  expect(await page.locator("#log").evaluate((f) => f.clientHeight)).toBeGreaterThan(feedBefore + 150);
  await expect(page.locator('.jr-mini [data-tile="err"] b')).toHaveText(await page.locator('#jr-stats [data-tile="err"] b').textContent());

  // «Развернуть» возвращает шапку и не даёт ей тут же свернуться снова.
  await page.locator("#jr-expand").click();
  await expect(view).not.toHaveClass(/compact/);
  await page.locator("#log").hover();
  await page.mouse.wheel(0, -300);
  // Прокрутка колесом плавная: даём ей закончиться, прежде чем проверять и двигать ленту дальше.
  await page.waitForTimeout(400);
  await expect(view).not.toHaveClass(/compact/);

  // Снова свернуть и вернуться вниз — шапка разворачивается сама.
  // Ждём кадр: иначе прокрутка вниз и колесо сольются в одно событие и низ не будет замечен.
  await page.locator("#log").evaluate((f) => {
    f.scrollTop = f.scrollHeight;
    return new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
  });
  await page.locator("#log").hover();
  await page.mouse.wheel(0, -900);
  await expect(view).toHaveClass(/compact/);
  await page.locator("#log").evaluate((f) => (f.scrollTop = f.scrollHeight));
  await expect(view).not.toHaveClass(/compact/);
});

test("окружение записи: «Раньше» и «Позже» дозагружают, клик открывает соседнюю, панель не уезжает", async ({ page }) => {
  await openJournal(page, Array.from({ length: 120 }, (_, i) => line(i)));
  await expect(page.locator("#log .jr-card").first()).toBeVisible();
  await page.locator("#log .jr-card", { hasText: "событие 100" }).click();
  const rows = page.locator("#log-drawer .jr-ctx-row");
  // 20 записей до, сама запись и 19 после — до конца файла.
  await expect(rows).toHaveCount(40);
  await expect(page.locator("#log-drawer .jr-ctx-row.cur")).toContainText("событие 100");
  // Заголовок панели остаётся видимым: прокручивается только список окружения.
  expect(await page.locator("#log-drawer").evaluate((d) => d.scrollTop)).toBe(0);

  await page.locator("#log-drawer .jr-ctx-more", { hasText: "Раньше" }).click();
  await expect(rows).toHaveCount(60);
  await expect(page.locator("#log-drawer .jr-ctx-more", { hasText: "Конец файла" })).toBeDisabled();

  await rows.filter({ hasText: "событие 99" }).first().click();
  await expect(page.locator("#log-drawer .jr-full")).toContainText("событие 99");
  await expect(page.locator("#log-drawer .jr-ctx-row.cur")).toContainText("событие 99");
});

test("модули: самые шумные кнопками, остальные в «Ещё модули» без повторов; уровни — плитками", async ({ page }) => {
  await openJournal(page, recentLines(Date.now()));
  await expect(page.locator("#log .jr-card").first()).toBeVisible();
  const quick = await page.locator("#module-chips button").allTextContents();
  expect(quick).toHaveLength(4);
  const rest = await page.locator("#module-filter option").evaluateAll((nodes) => nodes.slice(1).map((n) => n.value));
  expect(rest.length).toBeGreaterThan(0);
  expect(rest.filter((name) => quick.includes(name))).toEqual([]);
  await expect(page.locator(".filter-chip")).toHaveCount(0);

  // Редкий модуль из списка выбран там же, плитка «Записей» снимает фильтр уровня.
  await page.locator("#module-filter").selectOption(rest[0]);
  await expect(page.locator("#query-chips")).toContainText(`module:${rest[0]}`);
  await expect(page.locator("#module-filter")).toHaveClass(/on/);
  await page.locator('#jr-stats [data-tile="err"]').click();
  await expect(page.locator("#query-chips")).toContainText("level:error");
  await page.locator('#jr-stats [data-tile="total"]').click();
  await expect(page.locator("#query-chips")).not.toContainText("level:");
});

test("разделитель дня — строка ленты, а не липкая плашка поверх записей", async ({ page }) => {
  await openJournal(page, [line(1), line(2)]);
  await expect(page.locator("#log .jr-day")).toHaveCount(1);
  expect(await page.locator("#log .jr-day").evaluate((n) => getComputedStyle(n).position)).toBe("static");
});

test("шторка на телефоне: заголовок виден, затемнение позади закрывает её", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await openJournal(page, Array.from({ length: 80 }, (_, i) => line(i, i === 60 ? "ERROR" : "INFO")));
  await page.locator("#log .jr-card", { hasText: "событие 60" }).click();
  await expect(page.locator("#log-drawer .jr-dh strong")).toBeInViewport();
  await expect(page.locator("#log-drawer .jr-ctx-row.cur")).toBeVisible();
  expect(await page.locator("#log-drawer").evaluate((d) => d.scrollTop)).toBe(0);
  await expect(page.locator("#log-backdrop")).toBeVisible();
  await page.mouse.click(195, 40);
  await expect(page.locator("#log-drawer")).toBeHidden();
  await expect(page.locator("#log-backdrop")).toBeHidden();
});

test("правила: «Скрыть» убирает группу из ленты, «Скрытые» показывает её, правило удаляется из списка", async ({ page }) => {
  const api = await openJournal(page, recentLines(Date.now()));
  await expect(page.locator("#log .jr-card").first()).toBeVisible();
  await expect(page.locator("#hidden-toggle")).toBeHidden();
  await page.locator("#problems-open").click();
  const timeouts = page.locator("#log-drawer .jr-prob-row", { hasText: "таймаут" });
  await timeouts.locator(".jr-rule .jr-link", { hasText: "Скрыть" }).click();
  await expect(page.locator("#log .jr-card", { hasText: "таймаут" })).toHaveCount(0);
  await expect(page.locator("#hidden-toggle")).toHaveText("Скрытые · 1");
  await expect(timeouts.locator(".jr-rule-badge")).toHaveText("скрыто");
  await expect(page.locator('#jr-stats [data-tile="err"] b')).toHaveText("1");

  await page.locator("#hidden-toggle").click();
  await expect(page.locator("#log .jr-card", { hasText: "таймаут" })).toHaveCount(4);
  await expect(page).toHaveURL(/hidden=1/);
  await page.locator("#hidden-toggle").click();
  await expect(page.locator("#log .jr-card", { hasText: "таймаут" })).toHaveCount(0);

  // Уведомление для той же группы, затем удаление правила целиком из списка правил.
  await timeouts.locator(".jr-rule .jr-link", { hasText: "Уведомл" }).click();
  await timeouts.locator(".jr-rule-form input").first().fill("3");
  await timeouts.locator(".jr-rule-form button", { hasText: "Сохранить" }).click();
  await expect(timeouts.locator(".jr-rule-badge.bell")).toContainText("3 за 10 мин");
  expect(api.rules[0]).toMatchObject({ hide: true, notify: { count: 3, minutes: 10 } });
  await page.locator("#log-drawer .jr-rules summary").click();
  await page.locator("#log-drawer .jr-rules-row .jr-x").click();
  await expect(page.locator("#log-drawer .jr-rules")).toHaveCount(0);
  await expect(page.locator("#log .jr-card", { hasText: "таймаут" })).toHaveCount(4);
  await expect(page.locator("#hidden-toggle")).toBeHidden();
});

test("«Скрыть такие» в деталях записи и пометка hidden у записи из потока", async ({ page }) => {
  const now = Date.now();
  const lines = recentLines(now);
  const fresh = { id: "s1", ts: now - 1000, time: localStamp(now - 1000), level: "WARNING", module: "net", message: "медленный ответ", hidden: true };
  await openJournal(page, lines, { stream: () => `event: state\ndata: {"alive":true}\n\ndata: ${JSON.stringify(fresh)}\n\n` });
  await expect(page.locator("#log .jr-card").first()).toBeVisible();
  // Запись из потока с пометкой hidden не появляется в ленте.
  await page.waitForTimeout(300);
  await expect(page.locator("#log .jr-card", { hasText: "медленный ответ" })).toHaveCount(1);
  await page.locator("#log .jr-card", { hasText: "медленный ответ" }).click();
  await page.locator("#log-drawer .jr-acts button", { hasText: "Скрыть такие" }).click();
  await expect(page.locator("#log-drawer")).toBeHidden();
  await expect(page.locator("#log .jr-card", { hasText: "медленный ответ" })).toHaveCount(0);
  await expect(page.locator("#hidden-toggle")).toHaveText("Скрытые · 1");
});

test("уровень журнала модуля: применить из деталей, строка уровней, снятие; недоступный мост объясняется", async ({ page }) => {
  await openJournal(page, recentLines(Date.now()), { levelsAvailable: false });
  await page.locator("#log .jr-card", { hasText: "медленный ответ" }).click();
  const section = page.locator("#log-drawer .jr-lvl");
  await expect(section).toContainText("net пишет в журнал как настроено в боте");
  await expect(section.locator(".jr-lvl-note.warn")).toContainText("перезапустите бота");
  await section.locator("select").first().selectOption("WARNING");
  await section.locator("select").nth(1).selectOption("60");
  await section.locator("button", { hasText: "Применить" }).click();
  const chip = page.locator("#jr-levels .jr-level-chip");
  await expect(chip).toContainText("net только предупреждения · до");
  await expect(page.locator("#log-drawer .jr-lvl-note").first()).toContainText("только предупреждения");
  await chip.locator("button").click();
  await expect(page.locator("#jr-levels")).toBeHidden();
});

test("наблюдатель видит правила и уровни, но не меняет их", async ({ page }) => {
  await openJournal(page, recentLines(Date.now()), { role: "viewer" });
  await page.locator("#log .jr-card", { hasText: "медленный ответ" }).click();
  await expect(page.locator("#log-drawer .jr-acts button", { hasText: "Скрыть такие" })).toHaveCount(0);
  await expect(page.locator("#log-drawer .jr-lvl select")).toHaveCount(0);
  await page.locator("#problems-open").click();
  await expect(page.locator("#log-drawer .jr-prob-row").first()).toBeVisible();
  await expect(page.locator("#log-drawer .jr-rule .jr-link", { hasText: "Скрыть" })).toHaveCount(0);
});

test("записи журнала панели: вывод процесса и действия панели отмечены, архив подписан", async ({ page }) => {
  const now = Date.now();
  await openJournal(page, [
    `${localStamp(now - 120e3)} [INFO] panel: Перезапуск бота · anya`,
    `${localStamp(now - 60e3)} [ERROR] process: RuntimeError: Event loop is closed`,
    "Traceback (most recent call last):",
    '  File "/opt/heroku/heroku/main.py", line 9, in main',
    "RuntimeError: Event loop is closed",
  ]);
  const process = page.locator('#log .jr-card[data-source="process"]');
  await expect(process.locator(".jr-module")).toHaveText("вывод процесса");
  await expect(process.locator(".jr-where")).toHaveText("main.py:9 в main");
  await expect(page.locator('#log .jr-card[data-source="panel"] .jr-module')).toHaveText("панель");
  await page.locator('#log .jr-card[data-source="process"] .jr-module').click();
  await expect(page.locator("#query-chips")).toContainText("module:process");
  await expect(page.locator("#archive-span")).toContainText("история с");
});
