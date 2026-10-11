// Журнал: лента записей, строка запроса с фильтрами, шкала событий и панель деталей.
// Записи приходят с сервера уже собранными (traceback внутри записи), история читается
// постранично назад, новые записи добавляются потоком SSE. Исходный файл бота не изменяется.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
import { createLogTimeline } from "./log-timeline.js";
import { renderTrace, traceOrigin, traceSummary } from "./log-trace.js";

const PAGE = 200;
// В памяти и в DOM держится ограниченное число записей; это не лимит файла на сервере.
const MAX_ENTRIES = 2500;
const WINDOW_MS = 6 * 3600 * 1000;
const BUCKET_MS = 5 * 60 * 1000;
const BOOKMARKS_KEY = "hkc-log-bookmarks-v2";
// Сколько самых шумных модулей показывать кнопками; остальные — в списке «Ещё модули».
const QUICK_MODULES = 4;
// Окружение записи: сколько соседей грузить сразу и сколько добавлять кнопками «Раньше» и «Позже».
const CONTEXT_STEP = 20;
const CONTEXT_MAX = 200;
// Подписи модулей журнала панели в карточках; фильтр по-прежнему module:process и module:panel.
const SOURCE_LABELS = { process: "вывод процесса", panel: "панель" };
const CLEARED_KEY = "hkc-log-cleared-at";

export function createJournal(ctx) {
  const S = {
    level: "ALL",
    module: "",
    text: "",
    range: null, // { from, to|null } — null у to означает «до сейчас»
    clearedAt: 0, // «Очистить»: записи старше этого момента скрыты, файл не меняется
    live: true,
    bookmarksMode: false,
    entries: [],
    groups: [],
    hasOlder: false,
    seq: 0,
    loadingOlder: false,
    stick: true,
    missed: 0,
    selected: null,
    syncing: false,
    buffer: [],
    online: false,
    opened: false,
    modules: [],
    timeline: null,
    buckets: [], // последняя сводка шкалы: по ней считаются переходы между ошибками
    tiles: { buckets: [] }, // сводка для плиток: без фильтра по уровню, иначе «Ошибки» обнулили бы «Предупреждения»
    bookmarks: [],
    lastDay: "",
    compact: false, // шапка свёрнута в строку: при чтении истории ленте нужно больше места
    compactHold: false, // шапку развернули вручную: не сворачивать, пока лента не вернётся вниз
    lastScroll: 0,
    drawerMode: null, // "entry" — детали записи, "problems" — частые ошибки
    fromProblems: false, // запись открыта из списка частых ошибок: в панели есть «назад»
    problemsLevel: "ERROR",
    pendingEntry: null, // запись из ссылки: откроется после первой загрузки
    selectedEntry: null,
    rules: [], // правила журнала: скрыть группу записей, уведомить о повторах
    showHidden: false, // показать записи, скрытые правилами
    levels: null, // уровни журнала модулей, заданные из панели
  };
  let dom = null;
  let reloadTimer = 0;
  let statsTimer = 0;
  let restartsAt = 0;
  let statsBusy = false;
  let statsAgain = false;

  const $ = (selector) => ctx.$(selector);
  const el = (tag, cls, text) => {
    const node = document.createElement(tag);
    if (cls) node.className = cls;
    if (text != null) node.textContent = text;
    return node;
  };
  const motionOk = () =>
    !(window.prefersReducedMotion?.() ?? window.matchMedia?.("(prefers-reduced-motion: reduce)").matches) &&
    document.visibilityState === "visible";
  const clock = () => (S.timeline ? S.timeline.now() : Date.now());

  /* ---------- Условия отбора ---------- */

  // Параметры запроса к серверу без временного диапазона: его добавляет вызывающий.
  function baseParams() {
    const p = new URLSearchParams();
    if (S.level !== "ALL") p.set("level", S.level);
    if (S.module) p.set("module", S.module);
    if (S.text) p.set("q", S.text);
    if (S.showHidden) p.set("hidden", "1");
    return p;
  }

  // Нижняя граница показа: выбранный диапазон и момент очистки, что позже.
  const lowerBound = () => Math.floor(Math.max(S.range ? S.range.from : 0, S.clearedAt));

  // Добавляет к запросу временные границы.
  function applyRange(p) {
    const from = lowerBound();
    if (from) p.set("from", String(from));
    if (S.range && S.range.to != null) p.set("to", String(Math.ceil(S.range.to)));
  }

  function entryText(e) {
    return `${e.message}\n${e.module}\n${(e.trace || []).join("\n")}`.toLowerCase();
  }

  // Повторяет серверный отбор для записей из потока и закладок.
  function matchesBase(e) {
    if (S.level === "ERROR" && e.level !== "ERROR" && e.level !== "CRITICAL") return false;
    if ((S.level === "WARNING" || S.level === "INFO") && e.level !== S.level) return false;
    if (S.module && e.module !== S.module) return false;
    if (S.text && !entryText(e).includes(S.text.toLowerCase())) return false;
    return true;
  }

  function inRange(e) {
    if (e.ts < lowerBound()) return false;
    return !S.range || S.range.to == null || e.ts <= S.range.to;
  }

  const entryKey = (e) => `${e.ts}|${e.id}`;

  /* ---------- Группировка и карточки ---------- */

  // Подряд идущие одинаковые записи без traceback показываются одной карточкой с счётчиком.
  const groupKey = (e) => (e.trace?.length ? null : `${e.level}\u0000${e.module}\u0000${e.message}`);

  function buildGroups(entries) {
    const groups = [];
    for (const e of entries) {
      const last = groups.at(-1);
      const key = groupKey(e);
      if (last && key && last.key === key) last.entries.push(e);
      else groups.push({ key, entries: [e], node: null });
    }
    return groups;
  }

  const dayOf = (e) => (e.time || "").slice(0, 10);
  const hms = (e) => (e.time || "").slice(11, 19);

  function dayLabel(day) {
    const date = new Date(`${day}T00:00:00`);
    if (Number.isNaN(date.getTime())) return day || "Без даты";
    return date.toLocaleDateString("ru-RU", { day: "numeric", month: "long" });
  }

  // Вставляет текст с подсветкой найденного фрагмента; без innerHTML, поэтому безопасно.
  function highlight(parent, text) {
    const query = S.text;
    const at = query ? text.toLowerCase().indexOf(query.toLowerCase()) : -1;
    if (at < 0) {
      parent.textContent = text;
      return;
    }
    parent.append(text.slice(0, at), el("mark", "", text.slice(at, at + query.length)), text.slice(at + query.length));
  }

  const isMarked = (e) => S.bookmarks.some((item) => entryKey(item) === entryKey(e));

  function toggleBookmark(e) {
    const at = S.bookmarks.findIndex((item) => entryKey(item) === entryKey(e));
    if (at >= 0) S.bookmarks.splice(at, 1);
    else S.bookmarks.push({ ...e });
    S.bookmarks = S.bookmarks.slice(-200);
    try {
      localStorage.setItem(BOOKMARKS_KEY, JSON.stringify(S.bookmarks));
    } catch (_) {
      /* Без хранилища закладки живут до перезагрузки страницы. */
    }
    if (S.bookmarksMode) reload();
    else refreshMarks();
  }

  function refreshMarks() {
    dom.feed.querySelectorAll(".jr-card").forEach((card) => {
      const group = card._group;
      const marked = group.entries.some(isMarked);
      const button = card.querySelector(".jr-mark");
      button.textContent = marked ? "★" : "☆";
      button.setAttribute("aria-pressed", String(marked));
    });
  }

  function updateCard(group) {
    const card = group.node;
    const first = group.entries[0];
    const last = group.entries.at(-1);
    const rep = card.querySelector(".jr-rep");
    if (group.entries.length > 1) {
      const minutes = Math.max(1, Math.round((last.ts - first.ts) / 60000));
      rep.textContent = `×${group.entries.length} за ${minutes} мин`;
      rep.hidden = false;
    } else rep.hidden = true;
  }

  function createCard(group, fresh) {
    const e = group.entries[0];
    const card = el("div", "jr-card");
    card.dataset.level = e.level;
    // Записи журнала панели: вывод процесса бота и действия и события самой панели.
    if (e.module === "process" || e.module === "panel") card.dataset.source = e.module;
    card.tabIndex = 0;
    card._group = group;
    if (fresh && motionOk()) {
      card.classList.add("fresh");
      card.addEventListener("animationend", () => card.classList.remove("fresh"), { once: true });
    }
    card.append(levelIcon(e.level));
    const time = el("time", "jr-time", hms(e));
    time.addEventListener("pointerenter", () => (time.title = `${e.time} · ${ago(e.ts)}`));
    const body = el("div", "jr-body");
    const message = el("span", "jr-message");
    highlight(message, e.message);
    body.append(message);
    if (e.trace?.length) {
      const origin = traceOrigin(e.trace);
      if (origin) body.append(el("div", "jr-where", origin));
      const trace = el("details", "jr-trace");
      const reason = traceSummary(e.trace);
      const summary = el("summary");
      summary.append(el("span", "jr-tr-label", "Traceback"));
      summary.append(reason ? el("span", "jr-tr-reason", reason) : el("span", "jr-tr-count", `${e.trace.length} ${plural(e.trace.length, "строка", "строки", "строк")}`));
      trace.append(summary);
      // Содержимое строится при первом раскрытии: лента с сотнями ошибок не разбирает все traceback сразу.
      const fill = () => {
        if (trace.querySelector(".jr-tv")) return;
        const view = renderTrace(e.trace);
        const copy = el("button", "jr-copy", "Копировать");
        copy.type = "button";
        copy.addEventListener("click", (event) => {
          event.stopPropagation();
          copyText(e.trace.join("\n"), "Traceback скопирован");
        });
        view.prepend(copy);
        trace.append(view);
      };
      trace.addEventListener("toggle", () => trace.open && fill());
      // Если искомое найдено только в traceback, раскрываем его сразу.
      if (S.text && !e.message.toLowerCase().includes(S.text.toLowerCase()) && e.trace.join("\n").toLowerCase().includes(S.text.toLowerCase())) {
        trace.open = true;
        fill();
      }
      trace.addEventListener("click", (event) => event.stopPropagation());
      body.append(trace);
    }
    card.append(body);
    const meta = el("div", "jr-meta");
    meta.append(time, el("span", "jr-rep"));
    if (e.module) {
      const module = el("button", "jr-module", SOURCE_LABELS[e.module] || e.module);
      module.type = "button";
      module.style.setProperty("--mh", String(moduleHue(e.module)));
      module.title = "Показать только этот модуль";
      module.addEventListener("click", (event) => {
        event.stopPropagation();
        setQuery({ module: e.module });
      });
      meta.append(module);
    }
    const mark = el("button", "jr-mark", "☆");
    mark.type = "button";
    mark.title = "В закладки";
    mark.setAttribute("aria-label", "В закладки");
    mark.addEventListener("click", (event) => {
      event.stopPropagation();
      toggleBookmark(e);
    });
    meta.append(mark);
    card.append(meta);
    card.addEventListener("click", () => openDrawer(group.entries[0]));
    card.addEventListener("keydown", (event) => {
      if (event.target === card && (event.key === "Enter" || event.key === " ")) {
        event.preventDefault();
        openDrawer(group.entries[0]);
      }
    });
    group.node = card;
    updateCard(group);
    if (S.selected && group.entries.some((item) => entryKey(item) === S.selected)) card.classList.add("selected");
    if (group.entries.some(isMarked)) {
      mark.textContent = "★";
      mark.setAttribute("aria-pressed", "true");
    } else mark.setAttribute("aria-pressed", "false");
    return card;
  }

  const SVG = "http://www.w3.org/2000/svg";
  const ICON_PATHS = {
    err: ["M12 3.5 2.8 19.5h18.4z", "M12 10v4.2", "M12 17.2v.01"],
    warn: ["M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z", "M12 7.5V12l3 2"],
    info: ["M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z", "M12 11v5", "M12 8v.01"],
  };

  // Плитка слева в карточке: форма и цвет сразу говорят об уровне, не читая текста.
  function levelIcon(level) {
    const kind = level === "ERROR" || level === "CRITICAL" ? "err" : level === "WARNING" ? "warn" : "info";
    const tile = el("span", `jr-ico ${kind}`);
    tile.title = level;
    tile.setAttribute("aria-hidden", "true");
    const svg = document.createElementNS(SVG, "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    for (const d of ICON_PATHS[kind]) {
      const path = document.createElementNS(SVG, "path");
      path.setAttribute("d", d);
      svg.append(path);
    }
    tile.append(svg);
    return tile;
  }

  // Цвет точки у имени модуля: один модуль всегда одного оттенка, соседние различимы с первого взгляда.
  function moduleHue(name) {
    let h = 0;
    for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360;
    return h;
  }

  // «3 мин назад» для подсказки у времени записи.
  function ago(ts) {
    const s = Math.max(0, Math.round((clock() - ts) / 1000));
    if (s < 60) return `${s} с назад`;
    if (s < 3600) return `${Math.floor(s / 60)} мин назад`;
    if (s < 86400) return `${Math.floor(s / 3600)} ч назад`;
    return `${Math.floor(s / 86400)} дн назад`;
  }

  async function copyText(text, notice) {
    try {
      await navigator.clipboard.writeText(text);
      ctx.showNotice(notice);
    } catch (_) {
      ctx.showNotice("Не удалось скопировать", "error");
    }
  }

  function plural(n, one, few, many) {
    const m10 = n % 10;
    const m100 = n % 100;
    if (m10 === 1 && m100 !== 11) return one;
    if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few;
    return many;
  }

  function daySeparator(day) {
    const sep = el("div", "jr-day", dayLabel(day));
    sep.dataset.day = day;
    return sep;
  }

  /* ---------- Отрисовка ленты ---------- */

  function emptyState() {
    const box = el("div", "log-empty");
    const icon = el("span", "", S.bookmarksMode ? "☆" : S.entries.length || hasFilters() ? "⌕" : "⌁");
    const title = el("strong");
    const detail = el("small");
    if (S.bookmarksMode) {
      title.textContent = "Закладок пока нет";
      detail.textContent = "Отметьте звёздочкой запись в ленте — она появится здесь.";
    } else if (S.clearedAt && !S.text && !S.module && S.level === "ALL" && !S.range) {
      title.textContent = "Экран очищен";
      detail.textContent = "Новые записи появятся здесь автоматически. Файл журнала не изменён.";
    } else if (hasFilters()) {
      title.textContent = "Ничего не найдено";
      detail.textContent = "Измените запрос или выберите другой диапазон на шкале.";
    } else {
      title.textContent = "Событий пока нет";
      detail.textContent = "Поток подключён — новые записи появятся автоматически.";
    }
    box.append(icon, title, detail);
    return box;
  }

  const hasFilters = () => S.level !== "ALL" || S.module || S.text || S.range || S.clearedAt;

  // Полная перерисовка: только при смене запроса, когда лента всё равно заменяется целиком.
  function renderAll() {
    S.groups = buildGroups(S.entries);
    const fragment = document.createDocumentFragment();
    let day = "";
    for (const group of S.groups) {
      const d = dayOf(group.entries[0]);
      if (d !== day) {
        day = d;
        fragment.append(daySeparator(d));
      }
      fragment.append(createCard(group, false));
    }
    S.lastDay = day;
    if (!S.groups.length) fragment.append(emptyState());
    dom.feed.replaceChildren(fragment);
    updateCount();
    if (S.stick) dom.feed.scrollTop = dom.feed.scrollHeight;
  }

  function updateCount() {
    $("#line-count").textContent = `${S.entries.length}${S.hasOlder ? "+" : ""}`;
    $("#export-logs").disabled = S.entries.length === 0;
  }

  // Добавляет одну живую запись без перерисовки ленты.
  function appendEntry(e) {
    S.entries.push(e);
    if (S.entries.length > MAX_ENTRIES) {
      const dropped = S.entries.splice(0, S.entries.length - MAX_ENTRIES);
      S.hasOlder = true;
      dropFromTop(dropped.length);
    }
    dom.feed.querySelector(".log-empty")?.remove();
    const last = S.groups.at(-1);
    const key = groupKey(e);
    if (last && key && last.key === key) {
      last.entries.push(e);
      updateCard(last);
    } else {
      const group = { key, entries: [e], node: null };
      S.groups.push(group);
      const d = dayOf(e);
      if (d !== S.lastDay) {
        S.lastDay = d;
        dom.feed.append(daySeparator(d));
      }
      dom.feed.append(createCard(group, true));
    }
    updateCount();
    if (S.stick && ctx.autoscroll?.checked !== false) dom.feed.scrollTop = dom.feed.scrollHeight;
    else {
      S.missed++;
      showPill();
    }
  }

  // Убирает из начала ленты карточки, чьи записи вытеснены из памяти.
  function dropFromTop(count) {
    let left = count;
    while (left > 0 && S.groups.length) {
      const group = S.groups[0];
      if (group.entries.length > left) {
        group.entries.splice(0, left);
        updateCard(group);
        return;
      }
      left -= group.entries.length;
      S.groups.shift();
      const prev = group.node.previousElementSibling;
      const next = group.node.nextElementSibling;
      group.node.remove();
      // Разделитель дня без карточек под ним не нужен.
      if (prev?.classList.contains("jr-day") && !next?.classList.contains("jr-card")) prev.remove();
    }
  }

  function showPill() {
    const text = S.live ? `${S.missed} новых ↓` : `${S.missed} новых — вернуться к потоку`;
    dom.pill.textContent = text;
    dom.pill.hidden = S.missed === 0;
  }

  /* ---------- Загрузка ---------- */

  async function reload() {
    const seq = ++S.seq;
    S.syncing = true;
    S.buffer = [];
    S.missed = 0;
    dom.pill.hidden = true;
    S.stick = true;
    try {
      if (S.bookmarksMode) {
        S.entries = S.bookmarks.filter((e) => matchesBase(e) && inRange(e)).sort((a, b) => a.ts - b.ts);
        S.hasOlder = false;
      } else {
        const p = baseParams();
        p.set("limit", String(PAGE));
        applyRange(p);
        const data = await ctx.request(`/api/logs/entries?${p}`);
        if (seq !== S.seq) return;
        S.entries = data.entries || [];
        S.hasOlder = !!data.hasMore;
      }
      renderAll();
      refreshStats();
    } catch (error) {
      if (seq === S.seq) {
        dom.feed.replaceChildren(el("div", "log-empty", `Не удалось загрузить журнал: ${error.message}`));
      }
    } finally {
      if (seq === S.seq) {
        S.syncing = false;
        drainBuffer();
      }
    }
  }

  // Подгружает более старые записи при прокрутке к началу и не сбивает положение.
  async function loadOlder() {
    if (S.loadingOlder || !S.hasOlder || S.bookmarksMode || !S.entries.length || S.entries.length >= MAX_ENTRIES) return;
    S.loadingOlder = true;
    const seq = S.seq;
    try {
      const oldest = S.entries[0].ts;
      const skip = S.entries.filter((e) => e.ts === oldest).length;
      const p = baseParams();
      p.set("limit", String(PAGE));
      p.set("before", String(oldest));
      p.set("skip", String(skip));
      applyRange(p);
      const data = await ctx.request(`/api/logs/entries?${p}`);
      if (seq !== S.seq) return;
      const older = data.entries || [];
      S.hasOlder = !!data.hasMore;
      if (!older.length) {
        updateCount();
        return;
      }
      prependEntries(older);
    } catch (_) {
      /* Следующая прокрутка повторит попытку. */
    } finally {
      S.loadingOlder = false;
    }
  }

  function prependEntries(older) {
    const feed = dom.feed;
    const before = feed.scrollHeight - feed.scrollTop;
    S.entries = older.concat(S.entries);
    const fresh = buildGroups(older);
    const first = S.groups[0];
    const tail = fresh.at(-1);
    // Одинаковые записи на стыке страниц остаются одной карточкой.
    if (first && tail && tail.key && tail.key === first.key) {
      first.entries = tail.entries.concat(first.entries);
      fresh.pop();
      updateCard(first);
    }
    const fragment = document.createDocumentFragment();
    let day = "";
    for (const group of fresh) {
      const d = dayOf(group.entries[0]);
      if (d !== day) {
        day = d;
        fragment.append(daySeparator(d));
      }
      fragment.append(createCard(group, false));
    }
    // Разделитель дня на стыке дублировался бы.
    const firstNode = feed.querySelector(".jr-day, .jr-card");
    if (firstNode?.classList.contains("jr-day") && firstNode.dataset.day === day) firstNode.remove();
    feed.prepend(fragment);
    S.groups = fresh.concat(S.groups);
    updateCount();
    feed.scrollTop = feed.scrollHeight - before;
  }

  /* ---------- Поток ---------- */

  function drainBuffer() {
    const queued = S.buffer.splice(0);
    for (const e of queued) ingest(e);
  }

  // Принимает запись из потока; во время синхронизации записи ждут в буфере.
  function ingest(e) {
    if (S.syncing) {
      S.buffer.push(e);
      return;
    }
    if (S.bookmarksMode) return;
    // Запись, скрытая правилом журнала, не попадает ни в ленту, ни на шкалу, пока скрытые не показаны.
    if (e.hidden && !S.showHidden) return;
    if (matchesBase(e)) S.timeline?.bump(e.ts, e.level);
    if (!matchesBase(e) || !inRange(e)) return;
    if (!S.live) {
      S.missed++;
      showPill();
      return;
    }
    appendEntry(e);
  }

  // Догоняет записи, пришедшие между загрузкой истории и подключением потока (или за время обрыва).
  async function catchUp() {
    if (S.bookmarksMode || !S.live || (S.range && S.range.to != null)) return;
    const seq = ++S.seq;
    S.syncing = true;
    try {
      const last = S.entries.at(-1);
      const p = baseParams();
      p.set("limit", "500");
      if (last) p.set("from", String(Math.max(last.ts, S.clearedAt)));
      else applyRange(p);
      const data = await ctx.request(`/api/logs/entries?${p}`);
      if (seq !== S.seq) return;
      const fetched = data.entries || [];
      if (!last) {
        S.entries = fetched;
        S.hasOlder = !!data.hasMore;
        renderAll();
      } else {
        const known = S.entries.filter((e) => e.ts === last.ts).length;
        let skipped = 0;
        for (const e of fetched) {
          if (e.ts < last.ts) continue;
          if (e.ts === last.ts && skipped < known) {
            skipped++;
            continue;
          }
          appendEntry(e);
        }
      }
      // Поток подключился раньше запроса: его начало уже вошло в ответ. Ищем наибольшее
      // совпадение хвоста ответа с началом буфера и отбрасываем его, чтобы не показать дважды.
      const queued = S.buffer.filter(matchesBase);
      let overlap = Math.min(queued.length, fetched.length);
      for (; overlap > 0; overlap--) {
        let same = true;
        for (let i = 0; i < overlap && same; i++) same = entryKey(fetched[fetched.length - overlap + i]) === entryKey(queued[i]);
        if (same) break;
      }
      S.buffer = S.buffer.filter(matchesBase).slice(overlap);
    } catch (_) {
      /* Следующее переподключение повторит догонку. */
    } finally {
      if (seq === S.seq) {
        S.syncing = false;
        drainBuffer();
      }
    }
  }

  // Состояние потока показывается, только когда что-то не так или включён архив: о работающем
  // потоке говорит значок Live в заголовке, и дублировать его строкой не нужно.
  function setStreamState(text, online) {
    S.online = online;
    const node = $("#stream-state");
    node.replaceChildren(el("i"), document.createTextNode(` ${text}`));
    node.classList.toggle("online", online);
    node.hidden = online && S.live && !S.bookmarksMode;
    const badge = document.querySelector(".live-badge");
    if (badge) {
      badge.classList.toggle("off", !online);
      badge.lastChild.textContent = online ? "Live" : "Нет связи";
    }
  }

  // Закрывает прежний EventSource и подключает новый; переподключение выполняет браузер.
  function connectStream() {
    if (ctx.stream) ctx.stream.close();
    const stream = new EventSource("/api/logs/stream");
    ctx.stream = stream;
    let errored = false;
    stream.onopen = () => {
      setStreamState(S.live ? "поток подключён" : "архив · поток идёт в фоне", true);
      catchUp();
      errored = false;
    };
    stream.onmessage = (event) => {
      try {
        ingest(JSON.parse(event.data));
      } catch (_) {
        /* Повреждённое событие пропускается, поток остаётся. */
      }
    };
    stream.addEventListener("state", (event) => {
      try {
        const state = JSON.parse(event.data);
        if (!state.alive) setStreamState(`файл журнала недоступен${state.reason ? `: ${state.reason}` : ""}`, false);
        else if (!S.online || errored) setStreamState(S.live ? "поток подключён" : "архив · поток идёт в фоне", true);
      } catch (_) {
        /* Служебное событие не обязательно. */
      }
    });
    stream.onerror = () => {
      errored = true;
      setStreamState("переподключение…", false);
    };
  }

  // Первая загрузка после входа.
  async function loadHistory() {
    S.opened = true;
    loadRules();
    loadLevels();
    loadArchive();
    await reload();
    startTimeline();
    await openPendingEntry();
  }

  /* ---------- Шкала ---------- */

  function startTimeline() {
    if (S.timeline) return;
    S.timeline = createLogTimeline($("#timeline"), {
      windowMs: WINDOW_MS,
      bucketMs: BUCKET_MS,
      onSelect: (range) => selectRange(range),
    });
    S.timeline.setSelection(S.range ? [S.range.from, S.range.to ?? clock()] : null);
    refreshStats();
    refreshRestarts();
  }

  // Обновляет шкалу. Если запрос изменился, пока ответ был в пути, устаревший ответ отбрасывается
  // и запрос повторяется: иначе шкала держала бы данные прежнего фильтра до следующего опроса.
  async function refreshStats() {
    if (!S.timeline) return;
    if (statsBusy) {
      statsAgain = true;
      return;
    }
    statsBusy = true;
    const p = baseParams();
    p.set("bucket", String(BUCKET_MS));
    const key = p.toString();
    try {
      const stats = await ctx.request(`/api/logs/stats?${key}`);
      const fresh = baseParams();
      fresh.set("bucket", String(BUCKET_MS));
      if (fresh.toString() !== key) {
        statsAgain = true;
        return;
      }
      S.timeline.setStats(stats);
      S.buckets = stats.buckets || [];
      S.modules = Object.entries(stats.modules || {}).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
      // Плиткам нужны все уровни: при фильтре «Ошибки» счётчик предупреждений не должен падать в ноль.
      S.tiles = { buckets: S.buckets, modules: S.modules };
      const levelAtStart = S.level;
      if (S.level !== "ALL") {
        const all = baseParams();
        all.delete("level");
        all.set("bucket", String(BUCKET_MS));
        try {
          const wide = await ctx.request(`/api/logs/stats?${all}`);
          if (S.level === levelAtStart) {
            S.tiles = { buckets: wide.buckets || [], modules: Object.entries(wide.modules || {}).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0])) };
          }
        } catch (_) {
          /* Без второго запроса плитки покажут числа отфильтрованной выборки. */
        }
      }
      renderTimelineInfo();
      renderStats();
      renderQuick();
    } catch (_) {
      /* Шкала необязательна: лента работает без неё. */
    } finally {
      statsBusy = false;
      if (statsAgain) {
        statsAgain = false;
        refreshStats();
      }
    }
  }

  async function refreshRestarts() {
    if (!S.timeline) return;
    restartsAt = Date.now();
    try {
      const history = await ctx.request("/api/system/history?range=24h");
      const cutoff = clock() - WINDOW_MS;
      S.timeline.setMarkers((history.restarts || []).map((value) => Date.parse(value)).filter((t) => t > cutoff));
    } catch (_) {
      /* Отметки перезапусков необязательны. */
    }
  }

  function selectRange([from, to]) {
    const open = to >= clock() - BUCKET_MS / 2;
    S.range = { from: Math.floor(from), to: open ? null : Math.ceil(to) };
    if (!open) S.live = false;
    syncModeButtons();
    queryChanged();
  }

  function setPresetRange(minutes) {
    if (!minutes) S.range = null;
    else S.range = { from: clock() - minutes * 60000, to: null };
    queryChanged();
  }

  /* ---------- Запрос и чипы ---------- */

  // Применяет изменения запроса и перезагружает ленту; быстрые правки (ввод) откладываются.
  function queryChanged(delay = 0) {
    renderChips();
    // Открытый список частых ошибок следует за выбранным окном времени.
    if (S.drawerMode === "problems" && S.problemsKey !== problemsScope()) openProblems();
    S.timeline?.setSelection(S.range ? [S.range.from, S.range.to ?? clock()] : null);
    clearTimeout(reloadTimer);
    reloadTimer = setTimeout(reload, delay);
  }

  // Меняет часть запроса и синхронизирует поле ввода, чипы и ленту.
  function setQuery(patch, { reloadNow = true } = {}) {
    if ("level" in patch) S.level = patch.level;
    if ("module" in patch) S.module = patch.module;
    if ("text" in patch) {
      S.text = patch.text;
      dom.input.value = patch.text;
    }
    if ("range" in patch) S.range = patch.range;
    if (reloadNow) queryChanged();
    else renderChips();
  }

  // Разбирает введённое: level:error и module:name уходят в чипы, остальное — текст поиска.
  function parseInput(raw, final) {
    const rest = [];
    let level = S.level;
    let module = S.module;
    const tokens = raw.match(/"[^"]*"|\S+/g) || [];
    tokens.forEach((token, index) => {
      const m = token.match(/^(level|module):(.+)$/i);
      const complete = final || index < tokens.length - 1 || /\s$/.test(raw);
      if (m && complete) {
        if (m[1].toLowerCase() === "level") {
          const v = m[2].toLowerCase();
          level = v.startsWith("err") ? "ERROR" : v.startsWith("warn") ? "WARNING" : v.startsWith("info") ? "INFO" : "ALL";
        } else module = m[2];
      } else rest.push(token.replace(/^"|"$/g, ""));
    });
    return { level, module, text: rest.join(" ") };
  }

  function onInput(final) {
    const parsed = parseInput(dom.input.value, final);
    const structural = parsed.level !== S.level || parsed.module !== S.module;
    const textChanged = parsed.text !== S.text;
    if (!structural && !textChanged) return;
    S.level = parsed.level;
    S.module = parsed.module;
    S.text = parsed.text;
    if (structural) dom.input.value = parsed.text;
    queryChanged(final || structural ? 0 : 260);
  }

  function renderChips() {
    const box = $("#query-chips");
    box.replaceChildren();
    const chip = (name, value, clear) => {
      const node = el("span", "jr-chip");
      node.append(el("b", "", `${name}:`), document.createTextNode(value));
      const x = el("button", "", "×");
      x.type = "button";
      x.setAttribute("aria-label", `Убрать фильтр ${name}`);
      x.addEventListener("click", (event) => {
        event.preventDefault();
        clear();
        queryChanged();
      });
      node.append(x);
      box.append(node);
    };
    if (S.level !== "ALL") chip("level", S.level.toLowerCase(), () => (S.level = "ALL"));
    if (S.module) chip("module", S.module, () => (S.module = ""));
    if (S.range) {
      const t = (ms) => new Date(ms).toTimeString().slice(0, 5);
      chip("время", `${t(S.range.from)}–${S.range.to == null ? "сейчас" : t(S.range.to)}`, () => (S.range = null));
    }
    if (S.clearedAt) {
      chip("очищено", `до ${new Date(S.clearedAt).toTimeString().slice(0, 5)}`, () => setCleared(0));
    }
    $("#tl-reset").hidden = !S.range;
    renderTimelineInfo();
    renderStats();
    renderQuick();
    $("#preset-delete").disabled = !$("#preset-select").value;
    writeUrl();
  }

  // Подпись шкалы: окно или диапазон и итоги по ошибкам и предупреждениям в нём.
  function renderTimelineInfo() {
    const info = $("#tl-info");
    if (!info) return;
    info.replaceChildren();
    let from = -Infinity;
    let to = Infinity;
    if (S.range) {
      const t = (ms) => new Date(ms).toTimeString().slice(0, 5);
      info.append(`Диапазон ${t(S.range.from)} — ${S.range.to == null ? "сейчас" : t(S.range.to)}`);
      from = S.range.from - BUCKET_MS;
      to = S.range.to ?? Infinity;
    } else info.append("Последние 6 часов");
    S.tileRange = { from, to };
  }

  // Итоги окна или выбранного диапазона для плиток сверху.
  function renderStats() {
    const box = $("#jr-stats");
    if (!box) return;
    const { from = -Infinity, to = Infinity } = S.tileRange || {};
    let errors = 0;
    let warnings = 0;
    let total = 0;
    for (const b of S.tiles.buckets) {
      if (b.t < from || b.t > to) continue;
      errors += b.err;
      warnings += b.warn;
      total += b.err + b.warn + b.info;
    }
    const set = (name, value, sub = "") => {
      const tile = box.querySelector(`[data-tile="${name}"]`);
      tile.querySelector("b").textContent = value;
      const small = tile.querySelector("small");
      if (small) small.textContent = sub;
      return tile;
    };
    set("err", errors).classList.toggle("on", S.level === "ERROR");
    set("warn", warnings).classList.toggle("on", S.level === "WARNING");
    set("total", total);
    // Те же итоги в свёрнутой шапке.
    for (const [name, value, level] of [["err", errors, "ERROR"], ["warn", warnings, "WARNING"]]) {
      const mini = dom.mini.querySelector(`[data-tile="${name}"]`);
      mini.querySelector("b").textContent = value;
      mini.setAttribute("aria-pressed", String(S.level === level));
    }
    const top = (S.tiles.modules || S.modules)[0];
    const mod = set("mod", top ? top[0] : "—", top ? `${top[1]} ${plural(top[1], "запись", "записи", "записей")}` : "");
    mod.dataset.module = top ? top[0] : "";
    mod.classList.toggle("on", !!top && S.module === top[0]);
    mod.disabled = !top;
  }

  /* ---------- Переходы между ошибками ---------- */

  const errorCards = () => [...dom.feed.querySelectorAll('.jr-card[data-level="ERROR"], .jr-card[data-level="CRITICAL"]')];

  function focusCard(card) {
    dom.feed.querySelectorAll(".jr-card.nav").forEach((node) => node.classList.remove("nav"));
    card.classList.add("nav");
    S.stick = false;
    card.scrollIntoView({ block: "center", behavior: motionOk() ? "smooth" : "auto" });
  }

  // Ищет ближайшую к якорю ошибку среди загруженных карточек. Якорь — последняя выбранная
  // стрелками карточка, а без неё середина видимой ленты.
  function neighbourError(dir) {
    const feedTop = dom.feed.getBoundingClientRect().top;
    const at = (card) => card.getBoundingClientRect().top - feedTop;
    const anchor = dom.feed.querySelector(".jr-card.nav");
    const y = anchor ? at(anchor) : dom.feed.clientHeight / 2;
    const cards = errorCards().filter((card) => card !== anchor);
    if (dir > 0) return cards.find((card) => at(card) > y + 1);
    return [...cards].reverse().find((card) => at(card) < y - 1);
  }

  // Переход к предыдущей (dir < 0) или следующей ошибке: сначала по загруженной ленте,
  // затем подгрузкой старых записей, затем перескоком на ближайший интервал шкалы с ошибками.
  async function jumpError(dir) {
    let target = neighbourError(dir);
    for (let i = 0; !target && dir < 0 && S.hasOlder && i < 6; i++) {
      await loadOlder();
      target = neighbourError(dir);
    }
    if (target) return focusCard(target);
    const edge = dir < 0 ? S.entries[0]?.ts : S.entries.at(-1)?.ts;
    if (edge == null) return ctx.showNotice("Записей нет");
    const edgeBucket = Math.floor(edge / BUCKET_MS) * BUCKET_MS;
    const candidates = S.buckets.filter((b) => b.err > 0 && (dir < 0 ? b.t < edgeBucket : b.t > edgeBucket));
    const bucket = dir < 0 ? candidates.at(-1) : candidates[0];
    if (!bucket) return ctx.showNotice(dir < 0 ? "Более ранних ошибок в этом окне нет" : "Более поздних ошибок нет");
    const to = bucket.t + BUCKET_MS - 1;
    S.range = { from: bucket.t, to: to >= clock() - BUCKET_MS / 2 ? null : to };
    if (S.range.to != null) S.live = false;
    syncModeButtons();
    renderChips();
    S.timeline?.setSelection([S.range.from, S.range.to ?? clock()]);
    clearTimeout(reloadTimer);
    await reload();
    const cards = errorCards();
    const found = dir < 0 ? cards.at(-1) : cards[0];
    if (found) focusCard(found);
  }

  // Возврат к живому хвосту: диапазон снимается, лента прокручивается вниз.
  function jumpNow() {
    S.range = null;
    S.stick = true;
    dom.feed.querySelectorAll(".jr-card.nav").forEach((node) => node.classList.remove("nav"));
    setLive(true);
  }

  // Быстрые фильтры: самые шумные модули кнопками, остальные в списке «Ещё модули». Модуль не
  // повторяется в двух местах; выбранный, но редкий модуль показывается выбранным в списке.
  function renderQuick() {
    const box = $("#module-chips");
    const quick = S.modules.slice(0, QUICK_MODULES).map(([name]) => name);
    const rest = S.modules.slice(QUICK_MODULES).map(([name]) => name);
    if (S.module && !quick.includes(S.module) && !rest.includes(S.module)) rest.unshift(S.module);
    const signature = JSON.stringify([quick, rest]);
    if (box.dataset.sig !== signature) {
      box.dataset.sig = signature;
      box.replaceChildren();
      for (const name of quick) {
        const button = el("button", "jr-pill-btn", name);
        button.type = "button";
        button.dataset.module = name;
        button.style.setProperty("--mh", String(moduleHue(name)));
        button.addEventListener("click", () => setQuery({ module: S.module === name ? "" : name }));
        box.append(button);
      }
      const select = dom.moduleSelect;
      select.replaceChildren(new Option(`Ещё модули · ${rest.length}`, ""));
      for (const name of rest) select.add(new Option(name, name));
      select.hidden = rest.length === 0;
    }
    box.querySelectorAll("button").forEach((button) => button.setAttribute("aria-pressed", String(S.module === button.dataset.module)));
    const inRest = S.module && !quick.includes(S.module);
    dom.moduleSelect.value = inRest ? S.module : "";
    dom.moduleSelect.classList.toggle("on", !!inRest);
  }

  /* ---------- Панель деталей ---------- */

  // Готовит панель под новое содержимое: на телефоне это шторка с затемнением позади.
  function showDrawer(mode) {
    S.drawerMode = mode;
    dom.drawer.hidden = false;
    dom.backdrop.hidden = false;
    dom.drawer.replaceChildren();
    dom.drawer.scrollTop = 0;
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

  function openDrawer(e, { fromProblems = false } = {}) {
    S.selected = entryKey(e);
    S.selectedEntry = e;
    S.fromProblems = fromProblems;
    markSelected();
    showDrawer("entry");
    const drawer = dom.drawer;
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
    act("Копировать", () => copyText(rawText(e), "Запись скопирована"));
    act("Ссылка", () => copyText(entryLink(e), "Ссылка на запись скопирована"));
    if (e.module) act(`Только ${e.module}`, () => setQuery({ module: e.module }));
    act(isMarked(e) ? "Убрать закладку" : "В закладки", () => {
      toggleBookmark(e);
      openDrawer(e, { fromProblems });
    });
    if (canControl() && e.ts) {
      act("Скрыть такие", async () => {
        if (await saveRule({ ts: e.ts, id: e.id, hide: true }, "Такие записи скрыты. Показать их — кнопка «Скрытые» под поиском")) closeDrawer();
      });
    }
    drawer.append(drawerHead(`${e.module || "Запись"} · ${hms(e)}`, fromProblems), full);
    if (e.trace?.length) drawer.append(renderTrace(e.trace));
    drawer.append(kv, actions);
    if (e.module && e.module !== "panel" && e.module !== "process") drawer.append(levelSection(e.module));
    const list = el("div", "jr-ctx");
    drawer.append(el("div", "jr-ctx-title", "Окружение"), list);
    writeUrl();
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

  function rawText(e) {
    const head = e.time ? `${e.time} [${e.level}] ${e.module}: ${e.message}` : e.message;
    return [head, ...(e.trace || [])].join("\n");
  }

  function markSelected() {
    dom.feed.querySelectorAll(".jr-card.selected").forEach((node) => node.classList.remove("selected"));
    if (!S.selected) return;
    const group = S.groups.find((g) => g.entries.some((item) => entryKey(item) === S.selected));
    group?.node?.classList.add("selected");
  }

  function closeDrawer() {
    S.selected = null;
    S.drawerMode = null;
    dom.drawer.hidden = true;
    dom.backdrop.hidden = true;
    $("#problems-open").setAttribute("aria-pressed", "false");
    markSelected();
    writeUrl();
  }

  /* ---------- Частые ошибки ---------- */

  // Окно частых ошибок — то же, что у плиток: выбранный диапазон или последние 6 часов.
  // Фильтры модуля и текста не учитываются: клик по группе их меняет, а список должен оставаться.
  function problemsWindow() {
    const from = S.range ? lowerBound() : Math.max(lowerBound(), Math.floor(clock() - WINDOW_MS));
    const to = S.range && S.range.to != null ? Math.ceil(S.range.to) : 0;
    return { from, to };
  }

  const problemsScope = () => `${S.range ? `${S.range.from}-${S.range.to}` : ""}|${S.clearedAt}`;

  async function openProblems() {
    S.problemsKey = problemsScope();
    S.selected = null;
    markSelected();
    showDrawer("problems");
    writeUrl();
    const drawer = dom.drawer;
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
    if (S.rules.length) drawer.append(rulesList());
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
    meta.append(`последняя ${ago(e.ts)}${problem.count > 1 ? ` · первая в ${first}` : ""}`);
    const rules = ruleControls(problem);
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
      setQuery({ level: e.level === "WARNING" ? "WARNING" : "ERROR", module: e.module || "", text: problem.query || "" });
      dom.drawer.querySelectorAll(".jr-prob-row.on").forEach((node) => node.classList.remove("on"));
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

  /* ---------- Правила, уровни модулей, архив ---------- */

  const canControl = () => ctx.role !== "viewer";
  const ruleFor = (key) => S.rules.find((rule) => rule.key === key);
  const hiddenRules = () => S.rules.filter((rule) => rule.hide);
  const LEVEL_LABELS = { DEBUG: "подробно", INFO: "обычно", WARNING: "только предупреждения", ERROR: "только ошибки" };

  async function postJSON(path, body) {
    return ctx.request(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  }

  async function loadRules() {
    try {
      const data = await ctx.request("/api/logs/rules");
      S.rules = data.rules || [];
    } catch (_) {
      /* Без правил лента показывается целиком. */
    }
    renderControls();
  }

  async function loadLevels() {
    try {
      S.levels = await ctx.request("/api/logs/levels");
    } catch (_) {
      S.levels = { available: false, reason: "Уровни недоступны", levels: [] };
    }
    renderControls();
  }

  async function loadArchive() {
    try {
      const data = await ctx.request("/api/logs/archive");
      const span = $("#archive-span");
      if (data.enabled && data.firstTs) {
        const since = new Date(data.firstTs).toLocaleDateString("ru-RU", { day: "numeric", month: "long" });
        span.textContent = `· история с ${since}`;
        span.title = `Сжатый архив журнала: ${data.files} ${plural(data.files, "копия", "копии", "копий")}, ${(data.bytes / 1048576).toFixed(1)} МБ из ${Math.round(data.limitBytes / 1048576)} МБ, не старше ${data.limitDays} дн.`;
        span.hidden = false;
      } else span.hidden = true;
    } catch (_) {
      /* Подпись об архиве необязательна. */
    }
  }

  // Сохраняет правило группы и перечитывает ленту: скрытое пропадает из записей и счётчиков сразу.
  async function saveRule(body, notice) {
    try {
      const data = await postJSON("/api/logs/rules", body);
      S.rules = data.rules || [];
      renderControls();
      if (notice) ctx.showNotice(notice);
      queryChanged();
      return data.rule;
    } catch (error) {
      ctx.showNotice(error.message, "error");
      return null;
    }
  }

  async function removeRule(rule) {
    try {
      const data = await ctx.request(`/api/logs/rules/${encodeURIComponent(rule.id)}`, { method: "DELETE" });
      S.rules = data.rules || [];
      renderControls();
      queryChanged();
    } catch (error) {
      ctx.showNotice(error.message, "error");
    }
  }

  async function setLevel(logger, level, minutes) {
    try {
      const result = await postJSON("/api/logs/levels", { logger, level, minutes });
      ctx.showNotice(result.message);
    } catch (error) {
      ctx.showNotice(error.message, "error");
    }
    await loadLevels();
    // Мост в процессе бота применяет уровень раз в секунду: через пару секунд видно «применён».
    setTimeout(loadLevels, 1600);
  }

  const untilText = (until) => (until ? `до ${new Date(until * 1000).toTimeString().slice(0, 5)}` : "до отмены");

  // Под фильтрами: переключатель «Скрытые» и строка переопределённых уровней модулей.
  function renderControls() {
    const toggle = $("#hidden-toggle");
    const hidden = hiddenRules();
    toggle.hidden = hidden.length === 0 && !S.showHidden;
    toggle.textContent = `${S.showHidden ? "Скрытые показаны" : "Скрытые"} · ${hidden.length}`;
    toggle.setAttribute("aria-pressed", String(S.showHidden));
    toggle.title = hidden.length
      ? `Правила скрывают: ${hidden.map((rule) => `${rule.module || "—"}: ${rule.label}`).join("; ")}`
      : "Скрывающих правил нет";
    const row = $("#jr-levels");
    row.replaceChildren();
    const levels = S.levels?.levels || [];
    if (levels.length) {
      row.append(el("span", "jr-levels-title", "Уровни модулей"));
      for (const item of levels) {
        const chip = el("span", `jr-level-chip${item.applied ? "" : " pending"}`);
        chip.title = item.applied ? "Применён в процессе бота" : S.levels.reason || "Ждёт применения в процессе бота";
        chip.append(el("b", "", item.logger), document.createTextNode(` ${LEVEL_LABELS[item.level] || item.level} · ${untilText(item.until)}`));
        if (canControl()) {
          const x = el("button", "", "×");
          x.type = "button";
          x.setAttribute("aria-label", `Вернуть уровень ${item.logger} по умолчанию`);
          x.addEventListener("click", () => setLevel(item.logger, "", 0));
          chip.append(x);
        }
        row.append(chip);
      }
      if (!S.levels.available && S.levels.reason) row.append(el("span", "jr-levels-note", S.levels.reason));
    }
    row.hidden = !levels.length;
  }

  // Блок «Уровень журнала модуля» в деталях записи: подробнее на время или тише до отмены.
  function levelSection(module) {
    const box = el("div", "jr-lvl");
    box.append(el("div", "jr-ctx-title", "Уровень журнала модуля"));
    const current = (S.levels?.levels || []).find((item) => item.logger === module);
    box.append(el("p", "jr-lvl-note", current
      ? `${module}: ${LEVEL_LABELS[current.level] || current.level} · ${untilText(current.until)}${current.applied ? "" : " · ждёт применения"}`
      : `${module} пишет в журнал как настроено в боте. Можно включить подробный журнал на время или приглушить шум.`));
    if (!canControl()) return box;
    const level = el("select");
    level.setAttribute("aria-label", "Уровень");
    for (const [value, label] of [["DEBUG", "Подробно (DEBUG)"], ["INFO", "Обычно (INFO)"], ["WARNING", "Только предупреждения"], ["ERROR", "Только ошибки"]]) level.add(new Option(label, value));
    level.value = current?.level || "DEBUG";
    const span = el("select");
    span.setAttribute("aria-label", "Срок");
    for (const [value, label] of [["15", "на 15 минут"], ["60", "на час"], ["1440", "на сутки"], ["0", "до отмены"]]) span.add(new Option(label, value));
    const apply = el("button", "compact", "Применить");
    apply.type = "button";
    apply.addEventListener("click", () => setLevel(module, level.value, Number(span.value)).then(() => S.selected && reopenSelected()));
    const row = el("div", "jr-lvl-row");
    row.append(level, span, apply);
    if (current) {
      const reset = el("button", "compact", "По умолчанию");
      reset.type = "button";
      reset.addEventListener("click", () => setLevel(module, "", 0).then(() => S.selected && reopenSelected()));
      row.append(reset);
    }
    box.append(row);
    if (S.levels && !S.levels.available && S.levels.reason) box.append(el("p", "jr-lvl-note warn", S.levels.reason));
    return box;
  }

  // Перерисовывает открытую запись (после смены уровня или правила).
  function reopenSelected() {
    const entry = S.selectedEntry;
    if (entry && S.drawerMode === "entry") openDrawer(entry, { fromProblems: S.fromProblems });
  }

  // Строка правил в «Частых ошибках»: скрыть или показать группу и уведомлять о повторах.
  function ruleControls(problem) {
    const box = el("div", "jr-rule");
    const rule = ruleFor(problem.key);
    if (rule?.hide) box.append(el("span", "jr-rule-badge", "скрыто"));
    if (rule?.notify) box.append(el("span", "jr-rule-badge bell", `уведомлять: ${rule.notify.count} за ${rule.notify.minutes} мин`));
    if (!canControl()) return box;
    const hide = el("button", "jr-link", rule?.hide ? "Показать" : "Скрыть");
    hide.type = "button";
    hide.title = rule?.hide ? "Вернуть такие записи в ленту и счётчики" : "Убрать такие записи из ленты, счётчиков и Telegram";
    hide.addEventListener("click", (event) => {
      event.stopPropagation();
      saveRule({ key: problem.key, hide: !rule?.hide, notify: rule?.notify || null }, rule?.hide ? "Записи снова видны" : "Такие записи скрыты. Показать их — кнопка «Скрытые» под поиском").then(() => S.drawerMode === "problems" && openProblems());
    });
    const bell = el("button", "jr-link", rule?.notify ? "Уведомления…" : "Уведомлять…");
    bell.type = "button";
    bell.addEventListener("click", (event) => {
      event.stopPropagation();
      const open = box.querySelector(".jr-rule-form");
      if (open) open.remove();
      else box.append(notifyForm(problem, rule));
    });
    box.append(hide, bell);
    return box;
  }

  // Все правила журнала — в том числе для групп, которых нет в текущем окне.
  function rulesList() {
    const box = el("details", "jr-rules");
    box.append(el("summary", "", `Правила журнала · ${S.rules.length}`));
    for (const rule of S.rules) {
      const row = el("div", "jr-rules-row");
      const text = el("span", "jr-rules-text");
      text.append(el("b", "", rule.module || "—"), document.createTextNode(` ${rule.label}`));
      const badges = [rule.hide ? "скрыто" : "", rule.notify ? `уведомлять: ${rule.notify.count} за ${rule.notify.minutes} мин` : ""].filter(Boolean).join(" · ");
      row.append(text, el("small", "", badges));
      if (canControl()) {
        const x = el("button", "jr-x", "×");
        x.type = "button";
        x.title = "Удалить правило";
        x.setAttribute("aria-label", "Удалить правило");
        x.addEventListener("click", () => removeRule(rule).then(() => openProblems()));
        row.append(x);
      }
      box.append(row);
    }
    return box;
  }

  function notifyForm(problem, rule) {
    const form = el("form", "jr-rule-form");
    form.addEventListener("click", (event) => event.stopPropagation());
    const count = el("input");
    count.type = "number";
    count.min = "1";
    count.max = "1000";
    count.value = String(rule?.notify?.count || 5);
    count.setAttribute("aria-label", "Сколько раз");
    const minutes = el("input");
    minutes.type = "number";
    minutes.min = "1";
    minutes.max = "1440";
    minutes.value = String(rule?.notify?.minutes || 10);
    minutes.setAttribute("aria-label", "За сколько минут");
    const save = el("button", "compact primary", "Сохранить");
    save.type = "submit";
    form.append("Если повторится", count, "раз за", minutes, "мин", save);
    if (rule?.notify) {
      const off = el("button", "compact", "Выключить");
      off.type = "button";
      off.addEventListener("click", () => saveRule({ key: problem.key, hide: !!rule.hide, notify: null }, "Уведомление выключено").then(() => openProblems()));
      form.append(off);
    }
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      saveRule({ key: problem.key, hide: !!rule?.hide, notify: { count: Number(count.value), minutes: Number(minutes.value) } }, "Уведомление включено: придёт туда же, куда остальные уведомления панели").then(() => openProblems());
    });
    return form;
  }

  /* ---------- Адрес страницы ---------- */

  // Запрос журнала и открытая запись живут в адресе (#logs?level=ERROR&module=…&entry=…): ссылкой
  // можно поделиться, а перезагрузка страницы не сбрасывает фильтры. Адрес заменяется, а не
  // добавляется в историю, чтобы «Назад» не листал каждую букву поиска.
  function urlParams() {
    const p = baseParams();
    if (S.range) {
      p.set("from", String(Math.floor(S.range.from)));
      if (S.range.to != null) p.set("to", String(Math.ceil(S.range.to)));
    }
    if (S.drawerMode === "entry" && S.selected) {
      const at = S.selected.indexOf("|");
      p.set("entry", `${S.selected.slice(0, at)}~${S.selected.slice(at + 1)}`);
    }
    return p;
  }

  function writeUrl() {
    if (ctx.currentView !== "logs" || !dom) return;
    const p = urlParams().toString();
    const hash = p ? `#logs?${p}` : "#logs";
    if (location.hash !== hash) history.replaceState(history.state, "", `${location.pathname}${location.search}${hash}`);
  }

  function entryLink(e) {
    return `${location.origin}${location.pathname}#logs?entry=${e.ts}~${encodeURIComponent(e.id)}`;
  }

  // Разбирает адрес; true, если он относится к журналу.
  function readUrl() {
    const m = location.hash.match(/^#logs(?:\?(.*))?$/);
    if (!m) return false;
    const p = new URLSearchParams(m[1] || "");
    const level = (p.get("level") || "").toUpperCase();
    S.level = ["ERROR", "WARNING", "INFO"].includes(level) ? level : "ALL";
    S.module = p.get("module") || "";
    S.text = p.get("q") || "";
    if (dom) dom.input.value = S.text;
    const from = Number(p.get("from")) || 0;
    const to = Number(p.get("to")) || 0;
    S.range = from ? { from, to: to > from ? to : null } : null;
    S.bookmarksMode = false;
    S.showHidden = p.get("hidden") === "1";
    S.live = !S.range || S.range.to == null;
    S.pendingEntry = null;
    const entry = (p.get("entry") || "").match(/^(\d+)~(.+)$/);
    if (entry) S.pendingEntry = { ts: Number(entry[1]), id: entry[2] };
    return true;
  }

  // Открывает запись из ссылки: показывает её место в ленте (диапазон ±15 минут, если её нет среди
  // загруженных) и панель деталей. Если запись не подходит под фильтры, панель всё равно открывается.
  async function openPendingEntry() {
    const target = S.pendingEntry;
    S.pendingEntry = null;
    if (!target) return;
    const key = `${target.ts}|${target.id}`;
    const loaded = () => S.entries.find((e) => entryKey(e) === key);
    if (!loaded() && !S.bookmarksMode) {
      const pad = 15 * 60000;
      S.range = { from: target.ts - pad, to: Math.min(target.ts + pad, Math.floor(clock())) };
      S.live = false;
      syncModeButtons();
      renderChips();
      S.timeline?.setSelection([S.range.from, S.range.to]);
      clearTimeout(reloadTimer);
      await reload();
    }
    let entry = loaded();
    if (!entry) {
      try {
        const data = await ctx.request(`/api/logs/context?ts=${target.ts}&id=${encodeURIComponent(target.id)}&before=0&after=0`);
        entry = data.entries?.[data.index];
      } catch (_) {
        /* Ниже — сообщение о ротации. */
      }
    }
    if (!entry) {
      ctx.showNotice("Запись из ссылки не найдена: журнал мог ротироваться", "error");
      return;
    }
    const group = S.groups.find((g) => g.entries.some((item) => entryKey(item) === key));
    if (group?.node) {
      S.stick = false;
      group.node.scrollIntoView({ block: "center" });
      group.node.classList.add("focused");
      setTimeout(() => group.node.classList.remove("focused"), 2400);
    }
    openDrawer(entry);
  }

  // Вызывается точкой входа: раздел из адреса важнее запомненного в браузере.
  function initialView() {
    return /^#logs(\?|$)/.test(location.hash) ? "logs" : "";
  }

  /* ---------- Свёрнутая шапка ---------- */

  // Пока читаешь историю, шапка (плитки, модули, подписи шкалы) сворачивается в строку поиска
  // с итогами и узкой шкалой; у живого хвоста разворачивается обратно.
  function setCompact(on) {
    if (S.compact === on) return;
    S.compact = on;
    dom.view.classList.toggle("compact", on);
    if (S.stick) dom.feed.scrollTop = dom.feed.scrollHeight;
    S.lastScroll = dom.feed.scrollTop;
  }

  /* ---------- Очистка экрана ---------- */

  // Скрывает записи старше момента at (0 — показать всё). Файл журнала на сервере не меняется.
  function setCleared(at) {
    S.clearedAt = at;
    try {
      if (at) localStorage.setItem(CLEARED_KEY, String(at));
      else localStorage.removeItem(CLEARED_KEY);
    } catch (_) {
      /* Без хранилища очистка действует до перезагрузки страницы. */
    }
    queryChanged();
  }

  /* ---------- Режимы, закладки, экспорт ---------- */

  function syncModeButtons() {
    $("#mode-live").setAttribute("aria-pressed", String(S.live && !S.bookmarksMode));
    $("#mode-archive").setAttribute("aria-pressed", String(!S.live && !S.bookmarksMode));
    $("#bookmarks-only").setAttribute("aria-pressed", String(S.bookmarksMode));
    if (S.online) setStreamState(S.bookmarksMode ? "закладки · поток идёт в фоне" : S.live ? "поток подключён" : "архив · поток идёт в фоне", true);
  }

  function setLive(live) {
    S.live = live;
    S.bookmarksMode = false;
    // Возврат в поток: диапазон, не доходящий до «сейчас», сбрасывается, иначе новых записей не будет.
    if (live && S.range && S.range.to != null) S.range = null;
    syncModeButtons();
    queryChanged();
  }

  async function exportLogs() {
    const button = $("#export-logs");
    button.disabled = true;
    try {
      const lines = [];
      let before = 0;
      let skip = 0;
      for (let page = 0; page < 40; page++) {
        const p = baseParams();
        p.set("limit", "500");
        applyRange(p);
        if (before) {
          p.set("before", String(before));
          p.set("skip", String(skip));
        }
        const data = S.bookmarksMode ? { entries: S.entries, hasMore: false } : await ctx.request(`/api/logs/entries?${p}`);
        const batch = data.entries || [];
        for (let i = batch.length - 1; i >= 0; i--) lines.push(rawText(batch[i]));
        if (!data.hasMore || !batch.length) break;
        before = batch[0].ts;
        skip = batch.filter((e) => e.ts === before).length;
      }
      const text = `${lines.reverse().join("\n")}\n`;
      const link = document.createElement("a");
      link.href = URL.createObjectURL(new Blob([text], { type: "text/plain;charset=utf-8" }));
      link.download = `heroku-log-${new Date().toISOString().slice(0, 10)}.txt`;
      link.click();
      setTimeout(() => URL.revokeObjectURL(link.href), 1000);
    } catch (error) {
      ctx.showNotice(`Не удалось выгрузить журнал: ${error.message}`, "error");
    } finally {
      button.disabled = S.entries.length === 0;
    }
  }

  /* ---------- Сохранённые фильтры ---------- */

  function refreshPresetOptions() {
    const select = $("#preset-select");
    const selected = select.value;
    select.replaceChildren(new Option("Сохранённые фильтры", ""));
    for (const name of Object.keys(ctx.savedFilters).sort()) select.add(new Option(name, name));
    if (ctx.savedFilters[selected]) select.value = selected;
    $("#preset-delete").disabled = !select.value;
  }

  function savePresets() {
    try {
      localStorage.setItem("hkc-log-presets", JSON.stringify(ctx.savedFilters));
    } catch (_) {
      /* Без хранилища набор живёт до перезагрузки. */
    }
  }

  function bindPresets() {
    $("#preset-save").addEventListener("click", () => {
      const name = $("#preset-name").value.trim();
      if (!name) {
        ctx.showNotice("Введите имя фильтра", "error");
        return;
      }
      ctx.savedFilters[name] = { query: S.text, level: S.level, module: S.module };
      savePresets();
      refreshPresetOptions();
      $("#preset-select").value = name;
      $("#preset-delete").disabled = false;
      $("#preset-name").value = "";
      ctx.showNotice(`Фильтр «${name}» сохранён`);
    });
    $("#preset-select").addEventListener("change", () => {
      $("#preset-delete").disabled = !$("#preset-select").value;
      const preset = ctx.savedFilters[$("#preset-select").value];
      if (!preset) return;
      setQuery({ text: preset.query || "", level: ["WARNING", "ERROR", "INFO"].includes(preset.level) ? preset.level : "ALL", module: preset.module || "", range: null });
    });
    $("#preset-delete").addEventListener("click", () => {
      const name = $("#preset-select").value;
      if (!name) return;
      delete ctx.savedFilters[name];
      savePresets();
      refreshPresetOptions();
      ctx.showNotice(`Фильтр «${name}» удалён`);
    });
  }

  /* ---------- Переходы из других разделов ---------- */

  // Открывает журнал с заданным запросом. moduleHint — имя модуля бота: подбирается подходящий
  // логгер, а если такого нет, имя ищется как текст. focusError выделяет последнюю ошибку.
  async function openJournal({ module = "", moduleHint = "", text = "", level = "ALL", from = 0, to = 0, focusError = false } = {}) {
    if (moduleHint) {
      const wanted = moduleHint.toLowerCase();
      const found = S.modules.map(([name]) => name).find((name) => {
        const lower = name.toLowerCase();
        return lower === wanted || lower.endsWith(`.${wanted}`);
      });
      if (found) module = found;
      else text = moduleHint;
    }
    S.bookmarksMode = false;
    S.level = level;
    S.module = module;
    S.text = text;
    dom.input.value = text;
    S.range = from ? { from, to: to || null } : null;
    S.live = !S.range || S.range.to == null;
    if (focusError) S.live = false;
    syncModeButtons();
    ctx.setView("logs");
    renderChips();
    S.timeline?.setSelection(S.range ? [S.range.from, S.range.to ?? clock()] : null);
    clearTimeout(reloadTimer);
    await reload();
    if (!focusError) return;
    const cards = [...dom.feed.querySelectorAll('.jr-card[data-level="ERROR"], .jr-card[data-level="CRITICAL"]')];
    const target = cards.at(-1);
    if (!target) return;
    S.stick = false;
    target.scrollIntoView({ block: "center" });
    target.classList.add("focused");
    setTimeout(() => target.classList.remove("focused"), 2400);
  }

  function clearSearch() {
    if (!dom.input.value && !S.text) return;
    setQuery({ text: "" });
  }

  // Вызывается при показе раздела: шкала не рисуется, пока он скрыт.
  function onJournalShown() {
    S.timeline?.invalidate();
    if (S.opened) refreshStats();
    writeUrl();
  }

  /* ---------- Подписки ---------- */

  function bindJournal() {
    dom = {
      feed: $("#log"),
      input: $("#filter"),
      pill: $("#log-pill"),
      drawer: $("#log-drawer"),
      backdrop: $("#log-backdrop"),
      moduleSelect: $("#module-filter"),
      mini: $(".jr-mini"),
      view: $("#logs-view"),
    };
    readUrl();
    try {
      const stored = JSON.parse(localStorage.getItem(BOOKMARKS_KEY) || "[]");
      if (Array.isArray(stored)) S.bookmarks = stored.filter((e) => e && typeof e.ts === "number");
    } catch (_) {
      /* Повреждённые закладки игнорируются. */
    }

    try {
      S.clearedAt = Number(localStorage.getItem(CLEARED_KEY)) || 0;
    } catch (_) {
      /* Без хранилища журнал показывается целиком. */
    }
    $("#clear-log").addEventListener("click", () => {
      setCleared(Math.floor(clock()));
      ctx.showNotice("Экран очищен — файл журнала не изменён");
    });
    dom.input.addEventListener("input", () => onInput(false));
    dom.input.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        onInput(true);
      } else if (event.key === "Backspace" && !dom.input.value) {
        // Пустое поле: Backspace снимает последний чип, как в привычных строках поиска.
        if (S.range) S.range = null;
        else if (S.clearedAt) setCleared(0);
        else if (S.module) S.module = "";
        else if (S.level !== "ALL") S.level = "ALL";
        else return;
        queryChanged();
      }
    });
    // Подсказка в поиске короче на узком экране, иначе она обрезается посреди слова.
    const narrow = window.matchMedia("(max-width: 760px)");
    const placeholder = () => (dom.input.placeholder = narrow.matches ? "Поиск по журналу" : "Поиск · level:error  module:updates  «текст»");
    narrow.addEventListener?.("change", placeholder);
    placeholder();
    dom.moduleSelect.addEventListener("change", () => setQuery({ module: dom.moduleSelect.value }));
    $("#mode-live").addEventListener("click", () => setLive(true));
    $("#mode-archive").addEventListener("click", () => {
      S.live = false;
      S.bookmarksMode = false;
      syncModeButtons();
      queryChanged();
    });
    $("#bookmarks-only").addEventListener("click", () => {
      S.bookmarksMode = !S.bookmarksMode;
      if (!S.bookmarksMode) S.live = true;
      syncModeButtons();
      queryChanged();
    });
    $("#export-logs").addEventListener("click", exportLogs);
    // Плитки сводки — быстрые фильтры уровня; повторное нажатие снимает фильтр, «Записей» — все уровни.
    // Те же счётчики в свёрнутой шапке ведут себя так же.
    document.querySelectorAll('#jr-stats [data-tile="err"], .jr-mini [data-tile="err"]').forEach((button) =>
      button.addEventListener("click", () => setQuery({ level: S.level === "ERROR" ? "ALL" : "ERROR" })),
    );
    document.querySelectorAll('#jr-stats [data-tile="warn"], .jr-mini [data-tile="warn"]').forEach((button) =>
      button.addEventListener("click", () => setQuery({ level: S.level === "WARNING" ? "ALL" : "WARNING" })),
    );
    $('#jr-stats [data-tile="total"]').addEventListener("click", () => setQuery({ level: "ALL" }));
    $('#jr-stats [data-tile="mod"]').addEventListener("click", (event) => {
      const name = event.currentTarget.dataset.module;
      if (name) setQuery({ module: S.module === name ? "" : name });
    });
    $("#problems-open").addEventListener("click", () => (S.drawerMode === "problems" ? closeDrawer() : openProblems()));
    $("#hidden-toggle").addEventListener("click", () => {
      S.showHidden = !S.showHidden;
      renderControls();
      queryChanged();
    });
    dom.backdrop.addEventListener("click", closeDrawer);
    $("#jr-expand").addEventListener("click", () => {
      S.compactHold = true;
      setCompact(false);
    });
    $("#err-prev").addEventListener("click", () => jumpError(-1));
    $("#err-next").addEventListener("click", () => jumpError(1));
    $("#mini-prev").addEventListener("click", () => jumpError(-1));
    $("#mini-next").addEventListener("click", () => jumpError(1));
    // Ссылка, вставленная в адресную строку уже открытой панели, применяется без перезагрузки.
    window.addEventListener("hashchange", () => {
      if (!readUrl()) return;
      closeDrawer();
      ctx.setView("logs");
      queryChanged();
      if (S.pendingEntry) setTimeout(openPendingEntry, 0);
    });
    $("#tl-now").addEventListener("click", jumpNow);
    $("#tl-reset").addEventListener("click", () => {
      S.range = null;
      queryChanged();
    });
    document.querySelectorAll("[data-tl-range]").forEach((button) =>
      button.addEventListener("click", () => setPresetRange(Number(button.dataset.tlRange))),
    );
    dom.pill.addEventListener("click", () => {
      if (!S.live) setLive(true);
      else {
        S.stick = true;
        S.missed = 0;
        dom.pill.hidden = true;
        dom.feed.scrollTop = dom.feed.scrollHeight;
      }
    });
    dom.feed.addEventListener("scroll", () => {
      const f = dom.feed;
      S.stick = f.scrollHeight - f.scrollTop - f.clientHeight < 48;
      if (S.stick && S.live) {
        S.missed = 0;
        dom.pill.hidden = true;
      }
      // Прокрутка вверх по истории сворачивает шапку, возврат к живому хвосту разворачивает.
      // Развёрнутую вручную шапку не трогаем, пока лента снова не дойдёт до низа.
      const up = f.scrollTop < S.lastScroll - 4;
      S.lastScroll = f.scrollTop;
      if (S.stick) {
        S.compactHold = false;
        if (S.live) setCompact(false);
      } else if (up && !S.compactHold) setCompact(true);
      if (f.scrollTop < 240) loadOlder();
    });
    document.addEventListener("keydown", (event) => {
      // [ и ] — к предыдущей и следующей ошибке, когда фокус не в поле ввода.
      if ((event.key === "[" || event.key === "]") && ctx.currentView === "logs" && !event.ctrlKey && !event.metaKey && !event.altKey &&
        !/^(INPUT|TEXTAREA|SELECT)$/.test(document.activeElement?.tagName || "")) {
        event.preventDefault();
        jumpError(event.key === "[" ? -1 : 1);
        return;
      }
      if (event.key === "Escape" && S.drawerMode && ctx.currentView === "logs" && document.activeElement !== dom.input) closeDrawer();
    });
    ctx.timestamps?.addEventListener("change", () => dom.feed.classList.toggle("no-time", !ctx.timestamps.checked));
    dom.feed.classList.toggle("no-time", ctx.timestamps ? !ctx.timestamps.checked : false);
    bindPresets();
    refreshPresetOptions();
    renderChips();
    syncModeButtons();

    // Шкала обновляется раз в 10 секунд, пока раздел открыт; записи потока добавляют столбики сразу.
    statsTimer = setInterval(() => {
      if (!ctx.authenticated || ctx.currentView !== "logs" || document.hidden) return;
      refreshStats();
      if (S.levels?.levels?.length) loadLevels();
      if (Date.now() - restartsAt > 60000) {
        refreshRestarts();
        loadArchive();
      }
    }, 10000);
  }

  return {
    loadHistory,
    connectStream,
    openJournal,
    bindJournal,
    clearSearch,
    onJournalShown,
    refreshPresetOptions,
    initialView,
    stopJournalTimers() {
      clearInterval(statsTimer);
      S.timeline?.destroy();
      S.timeline = null;
    },
  };
}
