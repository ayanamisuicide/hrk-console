// Журнал: лента записей, строка запроса с фильтрами, шкала событий и панель деталей.
// Записи приходят с сервера уже собранными (traceback внутри записи), история читается
// постранично назад, новые записи добавляются потоком SSE. Исходный файл бота не изменяется.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
import { createLogTimeline } from "./log-timeline.js";

const PAGE = 200;
// В памяти и в DOM держится ограниченное число записей; это не лимит файла на сервере.
const MAX_ENTRIES = 2500;
const WINDOW_MS = 6 * 3600 * 1000;
const BUCKET_MS = 5 * 60 * 1000;
const LEVEL_LABEL = { ALL: "все", INFO: "инфо", WARNING: "предупреждения", ERROR: "ошибки" };
const BOOKMARKS_KEY = "hkc-log-bookmarks-v2";
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
    bookmarks: [],
    lastDay: "",
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
    card.tabIndex = 0;
    card._group = group;
    if (fresh && motionOk()) {
      card.classList.add("fresh");
      card.addEventListener("animationend", () => card.classList.remove("fresh"), { once: true });
    }
    card.append(el("time", "jr-time", hms(e)));
    const body = el("div", "jr-body");
    const level = el("span", `jr-level ${e.level}`, e.level === "INFO" || e.level === "DEBUG" ? "" : e.level === "WARNING" ? "WARN" : e.level);
    const message = el("span", "jr-message");
    highlight(message, e.message);
    body.append(level, message);
    if (e.trace?.length) {
      const trace = el("details", "jr-trace");
      trace.append(el("summary", "", `Traceback · ${e.trace.length} ${plural(e.trace.length, "строка", "строки", "строк")}`));
      trace.append(el("pre", "", e.trace.join("\n")));
      // Если искомое найдено только в traceback, раскрываем его сразу.
      if (S.text && !e.message.toLowerCase().includes(S.text.toLowerCase()) && e.trace.join("\n").toLowerCase().includes(S.text.toLowerCase())) trace.open = true;
      trace.addEventListener("click", (event) => event.stopPropagation());
      body.append(trace);
    }
    card.append(body);
    const meta = el("div", "jr-meta");
    meta.append(el("span", "jr-rep"));
    if (e.module) {
      const module = el("button", "jr-module", e.module);
      module.type = "button";
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

  function setStreamState(text, online) {
    S.online = online;
    const node = $("#stream-state");
    node.replaceChildren(el("i"), document.createTextNode(` ${text}`));
    node.classList.toggle("online", online);
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
    await reload();
    startTimeline();
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
      S.modules = Object.entries(stats.modules || {}).sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]));
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
    document.querySelectorAll(".filter-chip").forEach((button) => {
      const active = button.dataset.level === S.level;
      button.classList.toggle("active", active);
      button.setAttribute("aria-pressed", String(active));
    });
    $("#tl-reset").hidden = !S.range;
    const info = $("#tl-info");
    if (S.range) {
      const t = (ms) => new Date(ms).toTimeString().slice(0, 5);
      info.textContent = `Диапазон ${t(S.range.from)} — ${S.range.to == null ? "сейчас" : t(S.range.to)}`;
    } else info.textContent = "Последние 6 часов";
    dom.moduleSelect.value = S.module;
    $("#preset-delete").disabled = !$("#preset-select").value;
  }

  // Быстрые фильтры: самые шумные модули кнопками, остальные в списке.
  function renderQuick() {
    const box = $("#module-chips");
    box.replaceChildren();
    for (const [name] of S.modules.slice(0, 5)) {
      const button = el("button", "jr-pill-btn", name);
      button.type = "button";
      button.setAttribute("aria-pressed", String(S.module === name));
      button.addEventListener("click", () => setQuery({ module: S.module === name ? "" : name }));
      box.append(button);
    }
    const select = dom.moduleSelect;
    select.replaceChildren(new Option("Все модули", ""));
    for (const [name] of S.modules) select.add(new Option(name, name));
    if (S.module && !S.modules.some(([name]) => name === S.module)) select.add(new Option(S.module, S.module));
    select.value = S.module;
  }

  /* ---------- Панель деталей ---------- */

  async function openDrawer(e) {
    S.selected = entryKey(e);
    markSelected();
    const drawer = dom.drawer;
    drawer.hidden = false;
    drawer.replaceChildren();
    const head = el("div", "jr-dh");
    head.append(el("strong", "", `${e.module || "Запись"} · ${hms(e)}`));
    const close = el("button", "jr-x", "×");
    close.type = "button";
    close.setAttribute("aria-label", "Закрыть панель");
    close.addEventListener("click", closeDrawer);
    head.append(close);
    const full = el("pre", "jr-full", rawText(e));
    const kv = el("dl", "jr-kv");
    for (const [k, v] of [["Время", e.time], ["Уровень", e.level], ["Модуль", e.module || "—"]]) kv.append(el("dt", "", k), el("dd", "", v));
    const actions = el("div", "jr-acts");
    const act = (label, fn) => {
      const b = el("button", "compact", label);
      b.type = "button";
      b.addEventListener("click", fn);
      actions.append(b);
    };
    act("Копировать", async () => {
      try {
        await navigator.clipboard.writeText(rawText(e));
        ctx.showNotice("Запись скопирована");
      } catch (_) {
        ctx.showNotice("Не удалось скопировать", "error");
      }
    });
    if (e.module) act(`Только ${e.module}`, () => setQuery({ module: e.module }));
    act(isMarked(e) ? "Убрать закладку" : "В закладки", () => {
      toggleBookmark(e);
      openDrawer(e);
    });
    drawer.append(head, full, kv, actions);
    const ctxTitle = el("div", "jr-ctx-title", "Окружение");
    const list = el("div", "jr-ctx");
    list.append(el("div", "jr-ctx-note", "Загрузка…"));
    drawer.append(ctxTitle, list);
    try {
      const data = await ctx.request(`/api/logs/context?ts=${e.ts}&id=${encodeURIComponent(e.id)}&n=20`);
      if (S.selected !== entryKey(e)) return;
      list.replaceChildren();
      data.entries.forEach((item, index) => {
        const row = el("div", `jr-ctx-row${index === data.index ? " cur" : ""}${item.level === "ERROR" || item.level === "CRITICAL" ? " bad" : item.level === "WARNING" ? " warn" : ""}`);
        row.append(el("time", "", hms(item)), el("span", "", `${item.module ? `${item.module}: ` : ""}${item.message}`));
        list.append(row);
      });
      list.querySelector(".cur")?.scrollIntoView({ block: "center" });
    } catch (_) {
      list.replaceChildren(el("div", "jr-ctx-note", "Окружение недоступно: файл мог ротироваться."));
    }
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
    dom.drawer.hidden = true;
    markSelected();
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
    if (S.online) setStreamState(S.live ? "поток подключён" : "архив · поток идёт в фоне", true);
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
  }

  /* ---------- Подписки ---------- */

  function bindJournal() {
    dom = {
      feed: $("#log"),
      input: $("#filter"),
      pill: $("#log-pill"),
      drawer: $("#log-drawer"),
      moduleSelect: $("#module-filter"),
    };
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
    document.querySelectorAll(".filter-chip").forEach((button) =>
      button.addEventListener("click", () => setQuery({ level: button.dataset.level })),
    );
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
      if (f.scrollTop < 240) loadOlder();
    });
    document.addEventListener("keydown", (event) => {
      if (event.key === "Escape" && S.selected && ctx.currentView === "logs" && document.activeElement !== dom.input) closeDrawer();
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
      if (Date.now() - restartsAt > 60000) refreshRestarts();
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
    stopJournalTimers() {
      clearInterval(statsTimer);
      S.timeline?.destroy();
      S.timeline = null;
    },
  };
}
