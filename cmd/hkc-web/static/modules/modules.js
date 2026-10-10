// Строки сохраняют DOM и раскрытие при опросе. Меняются только изменившиеся поля.
export function createModules(ctx) {
  const rows = new Map();
  const matrixCells = new Map();
  const labels = {
    ready: "Готов",
    loading: "Загружается",
    error: "Ошибка",
    suspended: "Приостановлен",
    unloaded: "Выгружен",
  };
  let snapshot = null;
  let busy = false;
  let kind = "all";
  let stateFilter = "all";
  let matrixSignature = "";
  let session = "";
  let page = 0;
  const compact = matchMedia("(max-width: 700px)");
  const isProblem = (item) => ["error", "suspended"].includes(item.state);
  // Категории исчерпывают весь снимок, включая выгрузку и будущие состояния моста.
  const category = (item) =>
    isProblem(item)
      ? "problems"
      : ["ready", "loading", "unloaded"].includes(item.state)
        ? item.state
        : "unknown";
  const setText = (element, text) => {
    if (element.textContent !== String(text)) element.textContent = text;
  };

  function makeRow(item, index) {
    const row = document.createElement("details");
    row.className = "module-row";
    row.dataset.id = item.id;
    row.style.setProperty("--entry-delay", `${Math.min(index, 8) * 35}ms`);
    // Разметка постоянна; данные бота записываются исключительно через textContent.
    row.innerHTML =
      '<summary><span class="module-symbol" aria-hidden="true"></span><span class="module-identity"><strong></strong><small></small></span><span class="module-state"><i class="module-dot"></i><span></span></span><span class="module-chevron" aria-hidden="true">+</span></summary><div class="module-detail"><p class="module-explanation"></p><pre class="module-error" hidden></pre><button class="compact module-log" type="button">Открыть журнал ↗</button></div>';
    row.querySelector(".module-log").addEventListener("click", () => {
      const name = row.querySelector(".module-identity strong").textContent;
      // Если в журнале есть логгер с именем модуля, фильтруем по нему точно;
      // иначе ищем имя в тексте: имя модуля не обязано совпадать с logger.name.
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
      document
        .querySelectorAll(".filter-chip")
        .forEach((chip) =>
          chip.classList.toggle("active", chip.dataset.level === "ALL"),
        );
      ctx.setView("logs");
      ctx.renderLines();
      // У модуля с ошибкой сразу показываем последнюю ошибку; уровень не фильтруем,
      // чтобы строки traceback без уровня остались рядом с ней.
      if (row.dataset.state !== "error") return;
      const errors = ctx.logEl.querySelectorAll(
        '.line[data-level="ERROR"], .line[data-level="CRITICAL"]',
      );
      const last = errors[errors.length - 1];
      if (!last) return;
      ctx.autoscroll.checked = false;
      last.scrollIntoView({ block: "center" });
      last.classList.add("focused");
      setTimeout(() => last.classList.remove("focused"), 2400);
    });
    window.motionDisclosure(row, row.querySelector(".module-detail"));
    return row;
  }

  function renderModules() {
    if (!snapshot) return;
    const list = ctx.$("#modules-list");
    const items = snapshot.modules || [];
    const query = ctx.$("#modules-search").value.trim().toLocaleLowerCase();
    const ids = new Set(items.map((item) => item.id));
    for (const [id, row] of rows)
      if (!ids.has(id)) {
        row.remove();
        rows.delete(id);
      }
    const filtered = items.filter(
      (item) =>
        (kind === "all" || kind === item.kind) &&
        (stateFilter === "all" || category(item) === stateFilter) &&
        `${item.name} ${item.version || ""}`
          .toLocaleLowerCase()
          .includes(query),
    );
    const pageSize = compact.matches ? 6 : 12;
    const pages = Math.max(1, Math.ceil(filtered.length / pageSize));
    page = Math.min(page, pages - 1);
    const visibleIds = new Set(
      filtered
        .slice(page * pageSize, (page + 1) * pageSize)
        .map((item) => item.id),
    );
    for (const item of items) {
      let row = rows.get(item.id);
      if (!row) {
        row = makeRow(item, rows.size);
        rows.set(item.id, row);
        list.append(row);
      }
      const signature = JSON.stringify(item);
      if (row.dataset.signature !== signature) {
        const previous = row.dataset.state;
        row.dataset.signature = signature;
        row.dataset.state = item.state;
        row.dataset.category = category(item);
        setText(row.querySelector(".module-identity strong"), item.name);
        setText(
          row.querySelector(".module-symbol"),
          (item.name || "?").slice(0, 2).toLocaleUpperCase(),
        );
        setText(
          row.querySelector(".module-identity small"),
          `${item.kind === "core" ? "Встроенный" : "Установленный"}${item.version ? ` · v${item.version}` : ""}`,
        );
        setText(
          row.querySelector(".module-state span"),
          labels[item.state] || "Неизвестно",
        );
        row.querySelector(".module-dot").className =
          `module-dot ${labels[item.state] ? item.state : "unknown"}`;
        setText(
          row.querySelector(".module-explanation"),
          {
            ready: "Модуль завершил инициализацию и доступен боту.",
            loading: "Импорт или инициализация ещё выполняются.",
            error: "Не удалось загрузить или инициализировать модуль.",
            suspended: "Модуль приостановил свою инициализацию.",
            unloaded: "Модуль выгружен в этом запуске.",
          }[item.state] || "Загрузчик передал неизвестное состояние.",
        );
        const error = row.querySelector(".module-error");
        setText(error, item.error || "");
        error.hidden = !item.error;
        setText(
          row.querySelector(".module-log"),
          item.state === "error"
            ? "Найти ошибку в журнале ↗"
            : "Открыть журнал ↗",
        );
        if (
          previous &&
          previous !== item.state &&
          !matchMedia("(prefers-reduced-motion: reduce)").matches
        )
          row.animate(
            [
              { backgroundColor: "var(--surface-hover)" },
              { backgroundColor: "transparent" },
            ],
            { duration: 650, easing: "ease-out" },
          );
      }
      row.hidden = !visibleIds.has(item.id);
    }
    const counts = {
      total: items.length,
      ready: 0,
      loading: 0,
      problems: 0,
      unloaded: 0,
      unknown: 0,
    };
    items.forEach((item) => counts[category(item)]++);
    Object.entries(counts).forEach(([name, count]) => {
      const element = ctx.$(`#modules-${name}`);
      if (element.textContent !== String(count))
        ctx.animateValue(element, String(count));
    });
    for (const name of [
      "ready",
      "loading",
      "problems",
      "unloaded",
      "unknown",
    ]) {
      const segment =
        name === "ready"
          ? ctx.$("#modules-progress")
          : ctx.$(`[data-module-segment="${name}"]`);
      segment.style.width = `${counts.total ? (counts[name] / counts.total) * 100 : 0}%`;
    }
    // Кольцо делится на доли по состояниям; цвета берутся из CSS-переменных HUD.
    const ringColors = {
      ready: "var(--ok)",
      loading: "var(--warn)",
      problems: "var(--bad)",
      unloaded: "var(--text-3)",
      unknown: "var(--violet)",
    };
    let from = 0;
    const stops = [];
    for (const name of Object.keys(ringColors)) {
      if (!counts[name]) continue;
      const to = from + (counts[name] / counts.total) * 100;
      stops.push(`${ringColors[name]} ${from.toFixed(2)}% ${to.toFixed(2)}%`);
      from = to;
    }
    ctx
      .$("#modules-ring")
      .style.setProperty(
        "--ring",
        stops.length
          ? `conic-gradient(${stops.join(", ")})`
          : "conic-gradient(var(--border) 0 100%)",
      );
    const currentMatrix = JSON.stringify(
      items.slice(0, 60).map((item) => [item.id, item.name, item.state]),
    );
    if (currentMatrix !== matrixSignature) {
      matrixSignature = currentMatrix;
      const visibleCells = new Set(items.slice(0, 60).map((item) => item.id));
      for (const [id, cell] of matrixCells) {
        if (!visibleCells.has(id)) {
          cell.remove();
          matrixCells.delete(id);
        }
      }
      const cells = items.slice(0, 60).map((item, index) => {
        let cell = matrixCells.get(item.id);
        if (!cell) {
          cell = document.createElement("i");
          matrixCells.set(item.id, cell);
          cell.style.setProperty("--cell-delay", `${index * 12}ms`);
        }
        cell.dataset.state = category(item);
        cell.title = `${item.name} · ${labels[item.state] || "Неизвестно"}`;
        return cell;
      });
      const matrix = ctx.$("#modules-matrix");
      cells.forEach((cell, index) => {
        if (matrix.children[index] !== cell)
          matrix.insertBefore(cell, matrix.children[index] || null);
      });
    }
    setText(
      ctx.$("#modules-map-caption"),
      !items.length
        ? "Ожидаем снимок"
        : items.length > 60
          ? `Первые 60 из ${items.length}`
          : "Одна ячейка — один модуль",
    );
    setText(
      ctx.$("#modules-summary"),
      items.length
        ? `${snapshot.status === "live" ? "" : "Последний снимок · "}${counts.ready} из ${counts.total} готовы к работе`
        : snapshot.status === "live"
          ? "Загрузчик ещё обнаруживает модули"
          : "Ожидаем связь с Heroku",
    );
    ctx
      .$(".modules-progress")
      .setAttribute(
        "aria-label",
        `Готовы: ${counts.ready}; загружаются: ${counts.loading}; с проблемами: ${counts.problems}; выгружены: ${counts.unloaded}; неизвестно: ${counts.unknown}`,
      );
    const empty = ctx.$("#modules-empty");
    empty.hidden = filtered.length > 0;
    ctx.$("#modules-page").textContent =
      `${page + 1} / ${pages} · ${filtered.length} модулей`;
    ctx.$("#modules-prev").disabled = page === 0;
    ctx.$("#modules-next").disabled = page >= pages - 1;
    ctx.$(".modules-overview").dataset.loading = String(
      counts.loading > 0 || !items.length,
    );
    ctx.$("#modules-view").dataset.connection = snapshot.status;
    document
      .querySelectorAll("[data-modules-state]")
      .forEach((button) =>
        button.setAttribute(
          "aria-pressed",
          String(button.dataset.modulesState === stateFilter),
        ),
      );
    ctx.$("#modules-reset").hidden =
      stateFilter === "all" && kind === "all" && !query;
    ctx
      .$("#modules-only-problems")
      .setAttribute("aria-pressed", String(stateFilter === "problems"));
    ctx
      .$("#modules-only-problems")
      .classList.toggle("active", stateFilter === "problems");
    setText(
      empty,
      items.length
        ? "По этим фильтрам модулей нет."
        : snapshot.status === "live"
          ? "Heroku запущен. Ожидаем обнаружение первых модулей…"
          : snapshot.message,
    );
    const connection = ctx.$("#modules-connection");
    connection.dataset.status = snapshot.status;
    setText(connection.querySelector("span"), snapshot.message);
    list.classList.toggle("modules-stale", snapshot.status !== "live");
  }

  async function refreshModules() {
    if (busy) return;
    busy = true;
    try {
      const data = await ctx.request("/api/modules", {
        signal: AbortSignal.timeout(5000),
        cache: "no-store",
      });
      if (!ctx.authenticated) return;
      if (data.session && session !== data.session) {
        session = data.session;
        page = 0;
        rows.clear();
        matrixSignature = "";
        matrixCells.clear();
        ctx.$("#modules-matrix").replaceChildren();
        ctx.$("#modules-list").replaceChildren();
      }
      snapshot = data;
      renderModules();
    } catch (error) {
      if (snapshot) {
        snapshot.status = "stale";
        snapshot.message =
          "Не удалось обновить данные. Показан последний снимок.";
        renderModules();
      } else {
        ctx.$("#modules-connection").dataset.status = "stale";
        setText(
          ctx.$("#modules-connection span"),
          `Не удалось подключиться: ${error.message}`,
        );
        setText(ctx.$("#modules-empty"), "Повторим подключение автоматически.");
      }
    } finally {
      busy = false;
    }
  }

  function bindModules() {
    const paginate = (direction) => {
      page += direction;
      renderModules();
      if (!window.prefersReducedMotion?.())
        ctx.$("#modules-list").animate(
          [
            { opacity: 0.2, transform: "translateY(8px)" },
            { opacity: 1, transform: "none" },
          ],
          { duration: 420, easing: "ease-out" },
        );
    };
    ctx.$("#modules-prev").addEventListener("click", () => paginate(-1));
    ctx.$("#modules-next").addEventListener("click", () => paginate(1));
    compact.addEventListener("change", () => {
      page = 0;
      renderModules();
    });
    ctx.$("#modules-search").addEventListener("input", () => {
      page = 0;
      renderModules();
    });
    document.querySelectorAll("[data-modules-kind]").forEach((button) =>
      button.addEventListener("click", () => {
        page = 0;
        kind = button.dataset.modulesKind;
        document.querySelectorAll("[data-modules-kind]").forEach((item) => {
          item.classList.toggle("active", item === button);
          item.setAttribute("aria-pressed", String(item === button));
        });
        renderModules();
      }),
    );
    ctx.$("#modules-only-problems").addEventListener("click", () => {
      page = 0;
      stateFilter = stateFilter === "problems" ? "all" : "problems";
      renderModules();
    });
    document.querySelectorAll("[data-modules-state]").forEach((button) =>
      button.addEventListener("click", () => {
        page = 0;
        stateFilter =
          stateFilter === button.dataset.modulesState
            ? "all"
            : button.dataset.modulesState;
        renderModules();
      }),
    );
    ctx.$("#modules-reset").addEventListener("click", () => {
      page = 0;
      kind = stateFilter = "all";
      ctx.$("#modules-search").value = "";
      document.querySelectorAll("[data-modules-kind]").forEach((button) => {
        button.classList.toggle("active", button.dataset.modulesKind === "all");
        button.setAttribute(
          "aria-pressed",
          String(button.dataset.modulesKind === "all"),
        );
      });
      renderModules();
    });
    document.addEventListener("visibilitychange", () => {
      if (
        !document.hidden &&
        ctx.authenticated &&
        ctx.currentView === "modules"
      )
        refreshModules();
    });
  }
  return { refreshModules, bindModules };
}
