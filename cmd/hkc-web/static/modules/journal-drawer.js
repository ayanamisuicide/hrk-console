// Журнал: панель деталей записи с окружением и список частых ошибок.
import { renderTrace, traceSummary } from "./log-trace.js";
import { el, entryKey, hms, moduleHue, rawText, WINDOW_MS } from "./journal-format.js";

// Окружение записи: сколько соседей грузить сразу и сколько добавлять кнопками «Раньше» и «Позже».
const CONTEXT_STEP = 20;
const CONTEXT_MAX = 200;

export function createJournalDrawer(J) {
  const { ctx, S, $ } = J;
  // Части журнала вызывают друг друга через J: функции других частей появляются в нём
  // при сборке журнала, а вызываются уже после неё.

  /* ---------- Панель деталей ---------- */

  // Готовит панель под новое содержимое: на телефоне это шторка с затемнением позади.
  function showDrawer(mode) {
    S.drawerMode = mode;
    J.dom.drawer.hidden = false;
    J.dom.backdrop.hidden = false;
    J.dom.drawer.replaceChildren();
    J.dom.drawer.scrollTop = 0;
    $("#problems-open").setAttribute("aria-pressed", String(mode === "problems"));
  }

  function drawerHead(title, back) {
    const head = el("div", "jr-dh");
    if (back) {
      const button = el("button", "jr-back", "‹ Частые ошибки");
      button.type = "button";
      button.addEventListener("click", openProblems);
      head.append(button);
    }
    head.append(el("strong", "", title));
    const close = el("button", "jr-x", "×");
    close.type = "button";
    close.setAttribute("aria-label", "Закрыть панель");
    close.addEventListener("click", closeDrawer);
    head.append(close);
    return head;
  }

  // Отправляет запись в Telegram: из мини-приложения — себе в чат с ботом, из панели — в чат уведомлений.
  async function shareEntry(e) {
    try {
      const result = await ctx.request("/api/logs/share", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ ts: e.ts, id: e.id }),
      });
      ctx.showNotice(result.message[0].toUpperCase() + result.message.slice(1));
    } catch (error) {
      ctx.showNotice(error.message, "error");
    }
  }

  function openDrawer(e, { fromProblems = false } = {}) {
    S.selected = entryKey(e);
    S.selectedEntry = e;
    S.fromProblems = fromProblems;
    markSelected();
    showDrawer("entry");
    const drawer = J.dom.drawer;
    // В рамке — сама запись; traceback ниже разобран по кадрам, а «Копировать» берёт всё целиком.
    const full = el("pre", "jr-full", rawText({ ...e, trace: [] }));
    const kv = el("dl", "jr-kv");
    for (const [k, v] of [["Время", e.time], ["Уровень", e.level], ["Модуль", e.module || "—"]]) kv.append(el("dt", "", k), el("dd", "", v));
    const actions = el("div", "jr-acts");
    const act = (label, fn) => {
      const b = el("button", "compact", label);
      b.type = "button";
      b.addEventListener("click", fn);
      actions.append(b);
    };
    act("Копировать", () => J.copyText(rawText(e), "Запись скопирована"));
    act("Ссылка", () => J.copyText(J.entryLink(e), "Ссылка на запись скопирована"));
    if (ctx.canShare && e.ts) act("В Telegram", () => shareEntry(e));
    if (e.module) act(`Только ${e.module}`, () => J.setQuery({ module: e.module }));
    act(J.isMarked(e) ? "Убрать закладку" : "В закладки", () => {
      J.toggleBookmark(e);
      openDrawer(e, { fromProblems });
    });
    if (J.canControl() && e.ts) {
      act("Скрыть такие", async () => {
        if (await J.saveRule({ ts: e.ts, id: e.id, hide: true }, "Такие записи скрыты. Показать их — кнопка «Скрытые» под поиском")) closeDrawer();
      });
    }
    drawer.append(drawerHead(`${e.module || "Запись"} · ${hms(e)}`, fromProblems), full);
    if (e.trace?.length) drawer.append(renderTrace(e.trace));
    drawer.append(kv, actions);
    if (e.module && e.module !== "panel" && e.module !== "process") drawer.append(J.levelSection(e.module));
    const list = el("div", "jr-ctx");
    drawer.append(el("div", "jr-ctx-title", "Окружение"), list);
    J.writeUrl();
    loadContext(e, list, CONTEXT_STEP, CONTEXT_STEP);
  }

  // Окружение записи: соседи из того же файла. «Раньше» и «Позже» дозагружают по CONTEXT_STEP
  // в свою сторону, и список при этом не прыгает. Клик по строке открывает ту запись.
  async function loadContext(e, list, before, after, keep = "center") {
    const key = entryKey(e);
    if (!list.childElementCount) list.append(el("div", "jr-ctx-note", "Загрузка…"));
    let data;
    try {
      data = await ctx.request(`/api/logs/context?ts=${e.ts}&id=${encodeURIComponent(e.id)}&before=${before}&after=${after}`);
    } catch (_) {
      if (S.selected === key) list.replaceChildren(el("div", "jr-ctx-note", "Окружение недоступно: файл мог ротироваться."));
      return;
    }
    if (S.selected !== key || S.drawerMode !== "entry") return;
    const fromBottom = list.scrollHeight - list.scrollTop;
    list.replaceChildren();
    const more = (label, edge, enabled, fn) => {
      const button = el("button", "jr-ctx-more", enabled ? label : edge);
      button.type = "button";
      button.disabled = !enabled;
      button.addEventListener("click", fn);
      return button;
    };
    const hasOlder = data.index >= before && before < CONTEXT_MAX;
    const hasNewer = data.entries.length - 1 - data.index >= after && after < CONTEXT_MAX;
    list.append(more("↑ Раньше", "Начало файла", hasOlder, () => loadContext(e, list, Math.min(CONTEXT_MAX, before + CONTEXT_STEP), after, "bottom")));
    data.entries.forEach((item, index) => {
      const current = index === data.index;
      const kind = item.level === "ERROR" || item.level === "CRITICAL" ? " bad" : item.level === "WARNING" ? " warn" : "";
      const row = el(current ? "div" : "button", `jr-ctx-row${current ? " cur" : ""}${kind}`);
      if (!current) {
        row.type = "button";
        row.title = "Открыть эту запись";
        row.addEventListener("click", () => openDrawer(item));
      }
      const text = el("span", "", `${item.module ? `${item.module}: ` : ""}${item.message}`);
      if (item.trace?.length) text.append(el("i", "jr-ctx-tb", " · traceback"));
      row.append(el("time", "", hms(item)), text);
      list.append(row);
    });
    list.append(more("↓ Позже", "Конец файла", hasNewer, () => loadContext(e, list, before, Math.min(CONTEXT_MAX, after + CONTEXT_STEP), "top")));
    // Прокручивается только список окружения, а не вся панель и страница: иначе заголовок
    // панели и сама запись уезжали из виду (особенно в шторке на телефоне).
    const cur = list.querySelector(".cur");
    if (keep === "center" && cur) list.scrollTop = cur.offsetTop - list.clientHeight / 2 + cur.offsetHeight / 2;
    else if (keep === "bottom") list.scrollTop = list.scrollHeight - fromBottom;
  }

  function markSelected() {
    J.dom.feed.querySelectorAll(".jr-card.selected").forEach((node) => node.classList.remove("selected"));
    if (!S.selected) return;
    const group = S.groups.find((g) => g.entries.some((item) => entryKey(item) === S.selected));
    group?.node?.classList.add("selected");
  }

  function closeDrawer() {
    S.selected = null;
    S.drawerMode = null;
    J.dom.drawer.hidden = true;
    J.dom.backdrop.hidden = true;
    $("#problems-open").setAttribute("aria-pressed", "false");
    markSelected();
    J.writeUrl();
  }

  /* ---------- Частые ошибки ---------- */

  // Окно частых ошибок — то же, что у плиток: выбранный диапазон или последние 6 часов.
  // Фильтры модуля и текста не учитываются: клик по группе их меняет, а список должен оставаться.
  function problemsWindow() {
    const from = S.range ? J.lowerBound() : Math.max(J.lowerBound(), Math.floor(J.clock() - WINDOW_MS));
    const to = S.range && S.range.to != null ? Math.ceil(S.range.to) : 0;
    return { from, to };
  }

  const problemsScope = () => `${S.range ? `${S.range.from}-${S.range.to}` : ""}|${S.clearedAt}`;

  async function openProblems() {
    S.problemsKey = problemsScope();
    S.selected = null;
    markSelected();
    showDrawer("problems");
    J.writeUrl();
    const drawer = J.dom.drawer;
    const { from, to } = problemsWindow();
    const t = (ms) => new Date(ms).toTimeString().slice(0, 5);
    const span = S.range ? `${t(from)} — ${to ? t(to) : "сейчас"}` : "последние 6 часов";
    drawer.append(drawerHead("Частые ошибки", false));
    const tabs = el("div", "segmented jr-prob-tabs");
    tabs.setAttribute("role", "group");
    tabs.setAttribute("aria-label", "Уровень");
    for (const [level, label] of [["ERROR", "Ошибки"], ["WARNING", "Предупреждения"]]) {
      const b = el("button", "", label);
      b.type = "button";
      b.setAttribute("aria-pressed", String(S.problemsLevel === level));
      b.addEventListener("click", () => {
        S.problemsLevel = level;
        openProblems();
      });
      tabs.append(b);
    }
    const note = el("div", "jr-prob-note", `Одинаковые записи за ${span}; числа в тексте не в счёт. Клик показывает их в ленте.`);
    const list = el("div", "jr-prob");
    list.append(el("div", "jr-ctx-note", "Загрузка…"));
    drawer.append(tabs, note);
    if (S.rules.length) drawer.append(J.rulesList());
    drawer.append(list);
    const p = new URLSearchParams({ level: S.problemsLevel, from: String(from), limit: "30" });
    if (to) p.set("to", String(to));
    const level = S.problemsLevel;
    let data;
    try {
      data = await ctx.request(`/api/logs/problems?${p}`);
    } catch (error) {
      if (S.drawerMode === "problems") list.replaceChildren(el("div", "jr-ctx-note", `Не удалось посчитать: ${error.message}`));
      return;
    }
    if (S.drawerMode !== "problems" || S.problemsLevel !== level || !list.isConnected) return;
    list.replaceChildren();
    const problems = data.problems || [];
    if (!problems.length) {
      list.append(el("div", "jr-ctx-note", level === "ERROR" ? "За это время ошибок нет." : "За это время предупреждений нет."));
      return;
    }
    for (const problem of problems) list.append(problemRow(problem));
  }

  function problemRow(problem) {
    const e = problem.last;
    const row = el("div", `jr-prob-row${e.level === "WARNING" ? " warn" : ""}`);
    row.tabIndex = 0;
    row.title = "Показать эти записи в ленте";
    const count = el("span", "jr-prob-n", `×${problem.count}`);
    const body = el("div", "jr-prob-body");
    body.append(el("strong", "", e.message || "(пустое сообщение)"));
    const reason = e.trace?.length ? traceSummary(e.trace) : "";
    if (reason) body.append(el("span", "jr-prob-reason", reason));
    const meta = el("span", "jr-prob-meta");
    if (e.module) {
      const module = el("span", "jr-prob-mod", e.module);
      module.style.setProperty("--mh", String(moduleHue(e.module)));
      meta.append(module);
    }
    const first = new Date(problem.first).toTimeString().slice(0, 5);
    meta.append(`последняя ${J.ago(e.ts)}${problem.count > 1 ? ` · первая в ${first}` : ""}`);
    const rules = J.ruleControls(problem);
    body.append(meta, rules);
    const open = el("button", "jr-link jr-prob-open", "Последняя ›");
    open.type = "button";
    open.title = "Открыть последнюю запись";
    open.addEventListener("click", (event) => {
      event.stopPropagation();
      openDrawer(e, { fromProblems: true });
    });
    // «Последняя» стоит в одной строке с действиями правил, перед формой уведомления.
    rules.insertBefore(open, rules.querySelector(".jr-rule-form"));
    row.append(count, body);
    const show = () => {
      // Ровно эти записи: уровень, модуль и устойчивая часть сообщения. Диапазон не трогаем.
      J.setQuery({ level: e.level === "WARNING" ? "WARNING" : "ERROR", module: e.module || "", text: problem.query || "" });
      J.dom.drawer.querySelectorAll(".jr-prob-row.on").forEach((node) => node.classList.remove("on"));
      row.classList.add("on");
      if (window.matchMedia("(max-width: 900px)").matches) closeDrawer();
    };
    row.addEventListener("click", show);
    row.addEventListener("keydown", (event) => {
      if (event.target === row && (event.key === "Enter" || event.key === " ")) {
        event.preventDefault();
        show();
      }
    });
    return row;
  }

  // Перерисовывает открытую запись (после смены уровня или правила).
  function reopenSelected() {
    const entry = S.selectedEntry;
    if (entry && S.drawerMode === "entry") openDrawer(entry, { fromProblems: S.fromProblems });
  }

  return { openDrawer, closeDrawer, reopenSelected, openProblems, problemsScope };
}
