// Раздел «Сводка»: что было за ночь, сутки или неделю. Те же данные сервер присылает в Telegram
// утренней сводкой; раздел одинаков в панели и мини-приложении.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
export function createDigest(ctx) {
  let span = "24h";
  let busy = false;
  let signature = "";

  // Узел с классом и текстом; текст всегда через textContent.
  function el(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text != null) node.textContent = text;
    return node;
  }

  const plural = (n, one, few, many) => {
    const m10 = n % 10;
    const m100 = n % 100;
    if (m10 === 1 && m100 !== 11) return one;
    if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few;
    return many;
  };

  function duration(seconds) {
    if (!(seconds > 0)) return "";
    const hours = Math.floor(seconds / 3600);
    const minutes = Math.max(1, Math.round((seconds % 3600) / 60));
    return hours ? `${hours} ч ${minutes} мин` : `${minutes} мин`;
  }

  // Заголовок сводки: спокойно, падения или ошибки — как первая строка сообщения в Telegram.
  function headline(data) {
    if (data.calm) return ["ok", `За ${data.label} всё спокойно`, "Бот работал без перерывов, ошибок нет."];
    const parts = [];
    if (data.crashes) parts.push(`бот падал ${data.crashes} ${plural(data.crashes, "раз", "раза", "раз")}`);
    const manual = data.stops.length - data.crashes;
    if (manual > 0) parts.push(`остановок вручную ${manual}`);
    if (data.errors) parts.push(`ошибок ${data.errors}`);
    const title = parts.length ? parts.join(", ") : "Были перезапуски";
    const level = data.crashes || !data.running ? "bad" : data.errors ? "warn" : "ok";
    const now = data.running ? `Сейчас бот работает${data.uptime ? ` · ${data.uptime}` : ""}.` : "Сейчас бот не работает.";
    return [level, title[0].toUpperCase() + title.slice(1), now];
  }

  function kpi(label, value, note, tone = "") {
    const card = el("article", `card digest-kpi${tone ? ` ${tone}` : ""}`);
    card.append(el("span", "", label), el("strong", "", String(value)), el("small", "", note || ""));
    return card;
  }

  // Карточка раздела в стиле страницы «Система»: заголовок, список или пустая строка.
  function section(title, meta) {
    const card = el("article", "surface-card detail-card digest-card");
    const head = el("div", "card-heading");
    head.append(el("h3", "", title));
    if (meta) head.append(el("span", "muted", meta));
    card.append(head);
    return card;
  }

  function row(className, when, text, note, onClick) {
    const item = el(onClick ? "button" : "div", `digest-row ${className}`);
    if (onClick) {
      item.type = "button";
      item.addEventListener("click", onClick);
    }
    item.append(el("i"));
    if (when) item.append(el("time", "", when));
    else item.classList.add("no-time");
    const body = el("span", "digest-row-text");
    body.append(el("strong", "", text));
    if (note) body.append(el("small", "", note));
    item.append(body);
    return item;
  }

  function render(data) {
    const body = ctx.$("#digest-body");
    const [level, title, subtitle] = headline(data);
    const summary = el("section", "system-summary digest-summary");
    summary.dataset.level = level;
    const orb = el("span", "summary-orb");
    orb.setAttribute("aria-hidden", "true");
    orb.append(el("i"));
    const text = el("div", "summary-text");
    text.append(el("strong", "", title), el("p", "", subtitle));
    summary.append(orb, text, el("span", "muted", `за ${data.label}`));

    const compare = data.prevErrors === data.errors ? "как в прошлый такой же период" : `до этого ${data.prevErrors}`;
    const kpis = el("div", "digest-kpis");
    kpis.append(
      kpi("Падения", data.crashes, data.stops.length > data.crashes ? `и остановок вручную ${data.stops.length - data.crashes}` : "", data.crashes ? "bad" : ""),
      kpi("Перезапуски", data.restarts.length + data.recovers, data.recovers ? `автовосстановлением ${data.recovers}` : "вручную", ""),
      kpi("Ошибки", data.errors, compare, data.errors > data.prevErrors ? "bad" : ""),
      kpi("Предупреждения", data.warnings, "в журнале бота", data.warnings ? "warn" : ""),
    );

    const grid = el("div", "digest-grid");

    const bot = section("Бот", data.running ? "работает" : "не работает");
    const list = el("div", "digest-list");
    for (const stop of data.stops) {
      const down = duration(stop.downSeconds);
      const at = Date.parse(stop.at);
      list.append(
        row(stop.crash ? "bad" : "manual", stop.when, stop.reason, `${stop.crash ? "Упал" : "Остановлен"}${down ? ` · простоял ${down}` : ""}`, () =>
          ctx.openJournal({ from: at - 300000, to: at + 300000 }),
        ),
      );
    }
    for (const action of data.restarts) list.append(row("restart", action.when, "Перезапуск", action.who));
    if (data.recovers) list.append(row("restart", "", `Автовосстановление перезапускало бота ${data.recovers} ${plural(data.recovers, "раз", "раза", "раз")}`, ""));
    if (!list.children.length) list.append(el("p", "digest-empty", "Бот работал без перерывов, перезапусков не было."));
    bot.append(list);

    const problems = section("Частые ошибки", data.errors + data.warnings ? `${data.errors} ош. · ${data.warnings} пред.` : "");
    const plist = el("div", "digest-list");
    for (const problem of data.problems) {
      plist.append(
        row(problem.level === "WARNING" ? "warn" : "bad", problem.when, problem.message, `×${problem.count}${problem.module ? ` · ${problem.module}` : ""}`, () =>
          ctx.openJournal({ moduleHint: problem.module, text: problem.query, level: problem.level === "WARNING" ? "WARNING" : "ERROR" }),
        ),
      );
    }
    if (!plist.children.length) plist.append(el("p", "digest-empty", "Ошибок не было ✓"));
    problems.append(plist);

    const server = section("Сервер", data.load?.partial ? "нагрузка — за последние сутки" : "");
    if (data.load) {
      const facts = el("dl", "detail-list");
      facts.append(
        el("dt", "", "Пик CPU"),
        el("dd", "", `${Math.round(data.load.peakCpu)}% в ${data.load.peakWhen}`),
        el("dt", "", "Память"),
        el("dd", "", `до ${Math.round(data.load.maxMemory)}%`),
        el("dt", "", "Диск"),
        el("dd", "", `${Math.round(data.load.disk)}%`),
      );
      server.append(facts);
    } else server.append(el("p", "digest-empty", "Истории нагрузки пока нет."));

    const modules = section("Модули", data.modules.live ? `${data.modules.ready} работают` : "нет данных");
    if (data.modules.failed.length) {
      const failed = el("div", "digest-list");
      for (const name of data.modules.failed) failed.append(row("bad", "", name, "С ошибкой", () => ctx.openJournal({ moduleHint: name, focusError: true })));
      modules.append(failed);
    } else modules.append(el("p", "digest-empty", data.modules.live ? "Все модули загрузились." : "Загрузчик не отдал состояние модулей."));

    grid.append(bot, problems, server, modules);

    const extra = [];
    if (data.news.length) {
      const news = section("Новое");
      const items = el("ul", "digest-news");
      for (const item of data.news) items.append(el("li", "", item));
      news.append(items);
      extra.push(news);
    }
    const note = el("p", "digest-note");
    const schedule = data.schedule || {};
    note.textContent = !data.telegram
      ? "Каждое утро такая сводка может приходить в Telegram — подключите бота в админке."
      : schedule.enabled
        ? `Каждое утро в ${schedule.at} эта сводка приходит в Telegram — с прошлой утренней.${data.muteUntil ? ` Уведомления выключены до ${data.muteUntil}.` : ""}`
        : "Утренняя сводка в Telegram выключена.";
    body.replaceChildren(summary, kpis, grid, ...extra, note);
  }

  async function refreshDigest() {
    if (busy) return;
    busy = true;
    const body = ctx.$("#digest-body");
    try {
      const data = await ctx.request(`/api/digest?span=${span}`);
      const next = JSON.stringify({ ...data, to: undefined, from: undefined, schedule: undefined }) + span;
      if (next !== signature) {
        signature = next;
        render(data);
      }
    } catch (error) {
      signature = "";
      body.replaceChildren(el("p", "empty-state", `Не удалось собрать сводку: ${error.message}`));
    } finally {
      busy = false;
    }
  }

  function bindDigest() {
    document.querySelectorAll("[data-digest-span]").forEach((button) =>
      button.addEventListener("click", () => {
        span = button.dataset.digestSpan;
        document.querySelectorAll("[data-digest-span]").forEach((item) => {
          const active = item === button;
          item.classList.toggle("active", active);
          item.setAttribute("aria-pressed", String(active));
        });
        signature = "";
        ctx.$("#digest-body").replaceChildren(el("p", "empty-state", "Собираем сводку…"));
        refreshDigest();
      }),
    );
  }

  return { refreshDigest, bindDigest };
}
