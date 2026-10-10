// Вкладка «Модули»: одна фраза о главном, полоса долей, блок упавших модулей с ошибками,
// плитки по группам и карточка модуля. Узлы живут между опросами и меняются только по
// разнице; данные бота пишутся исключительно через textContent.
export function createModules(ctx) {
  const labels = {
    ready: "Работает",
    loading: "Загружается",
    error: "Ошибка",
    suspended: "Приостановлен",
    unloaded: "Выгружен",
  };
  const explanations = {
    ready: "Модуль загрузился, прошёл инициализацию и доступен боту.",
    loading: "Импорт или инициализация ещё идут.",
    error: "Модуль не удалось загрузить или инициализировать — ниже текст ошибки.",
    suspended: "Модуль сам приостановил свою инициализацию.",
    unloaded: "Модуль выгружен в этом запуске.",
  };
  const categories = ["ready", "loading", "problems", "unloaded", "unknown"];
  const tiles = new Map();
  const issues = new Map();
  let snapshot = null;
  let busy = false;
  let session = "";
  let kind = "all";
  let stateFilter = "all";
  let selected = "";
  let previousStates = null;
  let feed = [];
  let showAllIssues = false;
  const issueLimit = 4;
  const reducedMotion = () => window.prefersReducedMotion?.() ?? matchMedia("(prefers-reduced-motion: reduce)").matches;

  const isProblem = (item) => item.state === "error" || item.state === "suspended";
  // Категории исчерпывают весь снимок, включая будущие состояния моста.
  const category = (item) => (isProblem(item) ? "problems" : ["ready", "loading", "unloaded"].includes(item.state) ? item.state : "unknown");
  const label = (item) => labels[item.state] || "Неизвестно";
  const setText = (element, text) => {
    if (element && element.textContent !== String(text)) element.textContent = text;
  };
  const plural = (count, one, few, many) => {
    const mod10 = count % 10;
    const mod100 = count % 100;
    if (mod10 === 1 && mod100 !== 11) return one;
    if (mod10 >= 2 && mod10 <= 4 && (mod100 < 12 || mod100 > 14)) return few;
    return many;
  };
  // Оттенок аватара постоянен для имени: модуль узнаётся по цвету между запусками.
  const hue = (name) => {
    let hash = 0;
    for (const char of name) hash = (hash * 31 + char.codePointAt(0)) >>> 0;
    return hash % 360;
  };
  const initials = (name) => {
    const words = (name || "?").replace(/([a-zа-я])([A-ZА-Я])/g, "$1 $2").split(/[\s_.-]+/).filter(Boolean);
    return (words.length > 1 ? words[0][0] + words[1][0] : (name || "?").slice(0, 2)).toLocaleUpperCase();
  };
  const describe = (item) => `${item.kind === "core" ? "Встроенный" : "Установленный"}${item.version ? ` · v${item.version}` : ""}`;
  const firstLine = (text) =>
    (text || "")
      .split("\n")
      .map((line) => line.trim())
      .filter(Boolean)
      .at(-1) || "";

  /* ---------- Переход в журнал ---------- */

  // Фильтрует журнал по логгеру модуля (или по имени в тексте) и подсвечивает последнюю ошибку.
  function openInJournal(name, isError) {
    const wanted = name.toLowerCase();
    const logger = [...ctx.$("#module-filter").options]
      .map((option) => option.value)
      .find((value) => {
        const lower = value.toLowerCase();
        return value && (lower === wanted || lower.endsWith(`.${wanted}`));
      });
    ctx.filterInput.value = logger ? "" : name;
    ctx.$("#module-filter").value = logger || "";
    ctx.$("#time-from").value = "";
    ctx.$("#time-to").value = "";
    ctx.activeLevel = "ALL";
    ctx.bookmarksOnly = false;
    ctx.$("#bookmarks-only").setAttribute("aria-pressed", "false");
    document.querySelectorAll(".filter-chip").forEach((chip) => chip.classList.toggle("active", chip.dataset.level === "ALL"));
    ctx.setView("logs");
    ctx.renderLines();
    // Уровень не фильтруем: строки traceback без уровня должны остаться рядом с ошибкой.
    if (!isError) return;
    const errors = ctx.logEl.querySelectorAll('.line[data-level="ERROR"], .line[data-level="CRITICAL"]');
    const last = errors[errors.length - 1];
    if (!last) return;
    ctx.autoscroll.checked = false;
    last.scrollIntoView({ block: "center" });
    last.classList.add("focused");
    setTimeout(() => last.classList.remove("focused"), 2400);
  }

  async function copyError(text, button) {
    try {
      await navigator.clipboard.writeText(text);
      const original = button.textContent;
      button.textContent = "Скопировано";
      setTimeout(() => (button.textContent = original), 1500);
    } catch {
      ctx.showNotice("Не удалось скопировать — выделите текст вручную", "error");
    }
  }

  /* ---------- Плитки ---------- */

  function makeTile(item) {
    const tile = document.createElement("button");
    tile.type = "button";
    tile.className = "mods-tile";
    tile.dataset.id = item.id;
    tile.innerHTML =
      '<span class="mods-avatar" aria-hidden="true"></span><span class="mods-tile-text"><strong></strong><small></small></span><span class="mods-state"><i></i><span></span></span>';
    tile.addEventListener("click", () => openInspector(tile.dataset.id));
    return tile;
  }

  function updateTile(tile, item, entering) {
    const signature = JSON.stringify(item);
    if (tile.dataset.signature === signature) return;
    const previous = tile.dataset.state;
    tile.dataset.signature = signature;
    tile.dataset.state = item.state;
    tile.dataset.category = category(item);
    const avatar = tile.querySelector(".mods-avatar");
    avatar.style.setProperty("--hue", hue(item.name || ""));
    setText(avatar, initials(item.name));
    setText(tile.querySelector("strong"), item.name);
    // Тип модуля уже назван заголовком группы — на плитке только версия.
    setText(tile.querySelector("small"), item.version ? `v${item.version}` : "версия не указана");
    setText(tile.querySelector(".mods-state span"), label(item));
    tile.title = `${item.name} · ${label(item)}`;
    if (entering) tile.classList.add("entering");
    // Смена состояния подсвечивается вспышкой, чтобы глаз её заметил.
    if (previous && previous !== item.state && !reducedMotion())
      tile.animate([{ boxShadow: "0 0 0 3px var(--tile-tone)" }, { boxShadow: "0 0 0 0 transparent" }], { duration: 900, easing: "ease-out" });
  }

  // Порядок внутри группы: проблемы, загрузка, остальные — по имени.
  const rank = (item) => (isProblem(item) ? 0 : item.state === "loading" ? 1 : item.state === "ready" ? 2 : 3);
  const order = (a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name, "ru", { sensitivity: "base" });

  function renderCatalog(items, query, animateEntries) {
    const ids = new Set(items.map((item) => item.id));
    for (const [id, tile] of tiles)
      if (!ids.has(id)) {
        tile.remove();
        tiles.delete(id);
      }
    const matches = (item) =>
      (kind === "all" || kind === item.kind) &&
      (stateFilter === "all" || category(item) === stateFilter) &&
      `${item.name} ${item.version || ""}`.toLocaleLowerCase().includes(query);
    let shown = 0;
    for (const group of ["external", "core"]) {
      const grid = ctx.$(`[data-grid="${group}"]`);
      const members = items.filter((item) => (group === "core" ? item.kind === "core" : item.kind !== "core")).sort(order);
      let visible = 0;
      members.forEach((item, index) => {
        let tile = tiles.get(item.id);
        const entering = !tile;
        if (!tile) {
          tile = makeTile(item);
          tile.style.setProperty("--entry-delay", `${Math.min(index, 12) * 22}ms`);
          tiles.set(item.id, tile);
        }
        updateTile(tile, item, entering && animateEntries);
        // Узел переносится только если стоит не на своём месте — раскрытие и анимации живут.
        if (grid.children[index] !== tile) grid.insertBefore(tile, grid.children[index] || null);
        tile.hidden = !matches(item);
        if (!tile.hidden) visible++;
      });
      setText(ctx.$(`[data-group-count="${group}"]`), visible === members.length ? members.length : `${visible} из ${members.length}`);
      ctx.$(`[data-group="${group}"]`).hidden = visible === 0;
      shown += visible;
    }
    return shown;
  }

  /* ---------- Требуют внимания ---------- */

  function renderIssues(items) {
    const broken = items.filter(isProblem).sort(order);
    const list = ctx.$("#mods-issues");
    const ids = new Set(broken.map((item) => item.id));
    for (const [id, card] of issues)
      if (!ids.has(id)) {
        card.remove();
        issues.delete(id);
      }
    broken.forEach((item, index) => {
      let card = issues.get(item.id);
      if (!card) {
        card = document.createElement("article");
        card.className = "mods-issue";
        card.innerHTML =
          '<header><span class="mods-avatar" aria-hidden="true"></span><div><strong></strong><small></small></div><span class="mods-state"><i></i><span></span></span></header><code class="mods-issue-error"></code><footer><details class="mods-issue-trace"><summary>Полный текст ошибки</summary><pre></pre></details><div class="mods-issue-actions"><button class="compact" type="button" data-issue="copy">Скопировать</button><button class="compact primary" type="button" data-issue="log">В журнал ↗</button></div></footer>';
        card.querySelector('[data-issue="log"]').addEventListener("click", () => openInJournal(card.dataset.name, card.dataset.state === "error"));
        card.querySelector('[data-issue="copy"]').addEventListener("click", (event) => copyError(card.dataset.error, event.currentTarget));
        issues.set(item.id, card);
      }
      const signature = JSON.stringify(item);
      if (card.dataset.signature !== signature) {
        card.dataset.signature = signature;
        card.dataset.state = item.state;
        card.dataset.name = item.name;
        card.dataset.error = item.error || "";
        const avatar = card.querySelector(".mods-avatar");
        avatar.style.setProperty("--hue", hue(item.name || ""));
        setText(avatar, initials(item.name));
        setText(card.querySelector("strong"), item.name);
        setText(card.querySelector(".mods-state span"), label(item));
        setText(card.querySelector("small"), describe(item));
        const summary = firstLine(item.error) || explanations[item.state] || "";
        setText(card.querySelector(".mods-issue-error"), summary);
        const trace = card.querySelector(".mods-issue-trace");
        setText(trace.querySelector("pre"), item.error || "");
        // Полный текст нужен, только если он длиннее одной строки.
        trace.hidden = !item.error || item.error.trim() === summary;
        card.querySelector('[data-issue="copy"]').hidden = !item.error;
      }
      if (list.children[index] !== card) list.insertBefore(card, list.children[index] || null);
      // Длинный список упавших не вытесняет каталог: остальные — по кнопке.
      card.hidden = !showAllIssues && index >= issueLimit;
    });
    ctx.$("#mods-attention").hidden = broken.length === 0;
    setText(ctx.$("#mods-attention-title").lastChild, `Требуют внимания · ${broken.length}`);
    const more = ctx.$("#mods-issues-more");
    more.hidden = broken.length <= issueLimit;
    setText(more, showAllIssues ? "Свернуть" : `Показать ещё ${broken.length - issueLimit}`);
  }

  /* ---------- Изменения в запуске ---------- */

  function trackChanges(items) {
    const current = new Map(items.map((item) => [item.id, item]));
    if (previousStates) {
      const now = new Date();
      for (const item of items) {
        const before = previousStates.get(item.id);
        if (before && before !== item.state) feed.unshift({ at: now, name: item.name, from: before, to: item.state });
        // Новые модули при обычной загрузке не шумят; в ленту попадает только упавший сразу.
        if (!before && isProblem(item)) feed.unshift({ at: now, name: item.name, from: "", to: item.state });
      }
      feed = feed.slice(0, 8);
    }
    previousStates = new Map([...current].map(([id, item]) => [id, item.state]));
    const list = ctx.$("#mods-feed-list");
    const signature = JSON.stringify(feed.map((entry) => [entry.at.getTime(), entry.name, entry.to]));
    ctx.$("#mods-feed").hidden = feed.length === 0;
    if (list.dataset.signature === signature) return;
    list.dataset.signature = signature;
    list.replaceChildren(
      ...feed.map((entry) => {
        const row = document.createElement("li");
        row.dataset.to = ["error", "suspended"].includes(entry.to) ? "problems" : entry.to;
        const time = document.createElement("time");
        time.textContent = entry.at.toLocaleTimeString("ru-RU");
        const name = document.createElement("strong");
        name.textContent = entry.name;
        const change = document.createElement("span");
        change.textContent = entry.from ? `${labels[entry.from] || "Неизвестно"} → ${labels[entry.to] || "Неизвестно"}` : `появился · ${labels[entry.to] || "Неизвестно"}`;
        row.append(time, name, change);
        return row;
      }),
    );
  }

  /* ---------- Карточка модуля ---------- */

  function openInspector(id) {
    selected = id;
    renderInspector();
    const dialog = ctx.$("#mods-inspector");
    if (!dialog.open) dialog.showModal();
  }

  function renderInspector() {
    const dialog = ctx.$("#mods-inspector");
    const item = snapshot?.modules?.find((entry) => entry.id === selected);
    if (!item) {
      if (dialog.open) dialog.close();
      return;
    }
    dialog.dataset.state = item.state;
    const avatar = ctx.$("#mods-inspector-avatar");
    avatar.style.setProperty("--hue", hue(item.name || ""));
    setText(avatar, initials(item.name));
    setText(ctx.$("#mods-inspector-name"), item.name);
    setText(ctx.$("#mods-inspector-state span"), label(item));
    setText(ctx.$("#mods-inspector-explanation"), explanations[item.state] || "Загрузчик передал неизвестное состояние.");
    setText(ctx.$("#mods-inspector-kind"), item.kind === "core" ? "Встроенный" : "Установленный");
    setText(ctx.$("#mods-inspector-version"), item.version ? `v${item.version}` : "не указана");
    setText(ctx.$("#mods-inspector-id"), item.id);
    const error = ctx.$("#mods-inspector-error");
    setText(error, item.error || "");
    error.hidden = !item.error;
    ctx.$("#mods-inspector-copy").hidden = !item.error;
    setText(ctx.$("#mods-inspector-log"), item.state === "error" ? "Найти ошибку в журнале ↗" : "Открыть журнал ↗");
  }

  /* ---------- Сводка ---------- */

  function headline(items, counts) {
    const total = items.length;
    const modules = (count) => `${count} ${plural(count, "модуль", "модуля", "модулей")}`;
    if (!total) {
      if (snapshot.status === "live") return ["Загрузчик ищет модули…", "Heroku запущен, первые модули вот-вот появятся."];
      return [{ stopped: "Бот остановлен", waiting: "Новый запуск", unavailable: "Нет данных о модулях" }[snapshot.status] || "Нет связи", snapshot.message];
    }
    const stale = snapshot.status === "live" ? "" : "Последний снимок · ";
    if (counts.problems)
      return [
        `${counts.problems === total ? "Ни один модуль не загрузился" : `${modules(counts.problems)} ${plural(counts.problems, "не загрузился", "не загрузились", "не загрузились")}`}`,
        `${stale}${counts.ready} из ${total} работают${counts.loading ? `, ещё ${counts.loading} ${plural(counts.loading, "загружается", "загружаются", "загружаются")}` : ""}.`,
      ];
    if (counts.loading) return [`Загружается ${counts.loading} из ${total}`, `${stale}${counts.ready} уже работают.`];
    if (counts.ready === total) return [`${total === 1 ? "Модуль работает" : `Все ${modules(total)} работают`}`, `${stale}${snapshot.message || ""}`.trim()];
    return [`${counts.ready} из ${modules(total)} работают`, `${stale}${snapshot.message || ""}`.trim()];
  }

  // animateEntries — плитки новых модулей въезжают; первый снимок запуска рисуется сразу.
  function renderModules(animateEntries = false) {
    if (!snapshot) return;
    const items = snapshot.modules || [];
    const query = ctx.$("#modules-search").value.trim().toLocaleLowerCase();
    const counts = Object.fromEntries(categories.map((name) => [name, 0]));
    items.forEach((item) => counts[category(item)]++);

    const [title, subline] = headline(items, counts);
    const view = ctx.$("#modules-view");
    view.dataset.connection = snapshot.status;
    view.dataset.health = !items.length ? "empty" : counts.problems ? "problems" : counts.loading ? "loading" : "ok";
    setText(ctx.$("#mods-headline"), title);
    setText(ctx.$("#mods-subline"), subline);

    for (const name of categories) {
      const segment = ctx.$(`[data-segment="${name}"]`);
      segment.style.flexGrow = String(counts[name]);
      const button = ctx.$(`[data-modules-state="${name}"]`);
      const count = button.querySelector("strong");
      if (count.textContent !== String(counts[name])) ctx.animateValue(count, String(counts[name]));
      // Пустые редкие категории не занимают место, но выбранную не прячем.
      button.hidden = counts[name] === 0 && !["ready", "problems"].includes(name) && stateFilter !== name;
      button.setAttribute("aria-pressed", String(stateFilter === name));
    }
    ctx.$("#mods-bar").dataset.empty = String(items.length === 0);
    ctx.$("#mods-bar").setAttribute(
      "aria-label",
      `Работают: ${counts.ready}; загружаются: ${counts.loading}; с ошибкой: ${counts.problems}; выгружены: ${counts.unloaded}; неизвестно: ${counts.unknown}`,
    );

    renderIssues(items);
    const shown = renderCatalog(items, query, animateEntries === true);
    ctx.$("#mods-catalog").classList.toggle("modules-stale", snapshot.status !== "live");
    ctx.$("#modules-reset").hidden = stateFilter === "all" && kind === "all" && !query;

    const empty = ctx.$("#modules-empty");
    empty.hidden = shown > 0;
    setText(empty, items.length ? "По этим фильтрам модулей нет." : snapshot.status === "live" ? "Heroku запущен. Ожидаем обнаружение первых модулей…" : snapshot.message);

    const connection = ctx.$("#modules-connection");
    connection.dataset.status = snapshot.status;
    setText(connection.querySelector("span"), { live: "Live", stale: "Нет связи", stopped: "Остановлен", waiting: "Ожидаем", unavailable: "Нет данных" }[snapshot.status] || snapshot.status);
    connection.title = snapshot.message || "";
    if (ctx.$("#mods-inspector").open) renderInspector();
  }

  async function refreshModules() {
    if (busy) return;
    busy = true;
    try {
      const data = await ctx.request("/api/modules", { signal: AbortSignal.timeout(5000), cache: "no-store" });
      if (!ctx.authenticated) return;
      // Новый запуск бота: прежние плитки и история относятся к старому процессу.
      if (data.session && session !== data.session) {
        session = data.session;
        tiles.clear();
        issues.clear();
        feed = [];
        previousStates = null;
        document.querySelectorAll("[data-grid]").forEach((grid) => grid.replaceChildren());
        ctx.$("#mods-issues").replaceChildren();
      }
      snapshot = data;
      const animateEntries = previousStates !== null;
      if (data.status === "live") trackChanges(data.modules || []);
      renderModules(animateEntries);
    } catch (error) {
      if (snapshot) {
        snapshot.status = "stale";
        snapshot.message = "Не удалось обновить данные. Показан последний снимок.";
        renderModules();
      } else {
        ctx.$("#modules-connection").dataset.status = "stale";
        setText(ctx.$("#modules-connection span"), "Нет связи");
        setText(ctx.$("#mods-subline"), `Не удалось подключиться: ${error.message}`);
        setText(ctx.$("#modules-empty"), "Повторим подключение автоматически.");
      }
    } finally {
      busy = false;
    }
  }

  function setKind(next) {
    kind = next;
    document.querySelectorAll("[data-modules-kind]").forEach((button) => {
      const active = button.dataset.modulesKind === kind;
      button.classList.toggle("active", active);
      button.setAttribute("aria-pressed", String(active));
    });
  }

  function bindModules() {
    ctx.$("#modules-search").addEventListener("input", renderModules);
    ctx.$("#mods-issues-more").addEventListener("click", () => {
      showAllIssues = !showAllIssues;
      renderModules();
    });
    document.querySelectorAll("[data-modules-kind]").forEach((button) =>
      button.addEventListener("click", () => {
        setKind(button.dataset.modulesKind);
        renderModules();
      }),
    );
    document.querySelectorAll("[data-modules-state]").forEach((button) =>
      button.addEventListener("click", () => {
        stateFilter = stateFilter === button.dataset.modulesState ? "all" : button.dataset.modulesState;
        renderModules();
      }),
    );
    ctx.$("#modules-reset").addEventListener("click", () => {
      stateFilter = "all";
      setKind("all");
      ctx.$("#modules-search").value = "";
      renderModules();
    });
    const dialog = ctx.$("#mods-inspector");
    ctx.$("#mods-inspector-close").addEventListener("click", () => dialog.close());
    // Клик по затемнению вокруг карточки закрывает её.
    dialog.addEventListener("click", (event) => {
      if (event.target === dialog) dialog.close();
    });
    ctx.$("#mods-inspector-log").addEventListener("click", () => {
      const item = snapshot?.modules?.find((entry) => entry.id === selected);
      dialog.close();
      if (item) openInJournal(item.name, item.state === "error");
    });
    ctx.$("#mods-inspector-copy").addEventListener("click", (event) => {
      const item = snapshot?.modules?.find((entry) => entry.id === selected);
      if (item?.error) copyError(item.error, event.currentTarget);
    });
    document.addEventListener("visibilitychange", () => {
      if (!document.hidden && ctx.authenticated && ctx.currentView === "modules") refreshModules();
    });
  }

  return { refreshModules, bindModules };
}
