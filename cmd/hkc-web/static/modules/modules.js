// Строки сохраняют DOM и раскрытие при опросе. Меняются только изменившиеся поля.
export function createModules(ctx) {
  const rows = new Map();
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
  let problemsOnly = false;
  let session = "";
  let page = 0;
  const compact = matchMedia("(max-width: 700px)");
  const isProblem = (item) => ["error", "suspended"].includes(item.state);
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
      '<summary><span class="module-symbol" aria-hidden="true">◈</span><span class="module-identity"><strong></strong><small></small></span><span class="module-state"><i class="module-dot"></i><span></span></span><span class="module-chevron" aria-hidden="true">⌄</span></summary><div class="module-detail"><p class="module-explanation"></p><pre class="module-error" hidden></pre><button class="compact module-log" type="button">Открыть журнал →</button></div>';
    row.querySelector(".module-log").addEventListener("click", () => {
      // Поиск по имени не предполагает совпадения имени модуля и logger.name.
      ctx.filterInput.value = row.querySelector(
        ".module-identity strong",
      ).textContent;
      ctx.$("#module-filter").value = "";
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
        (!problemsOnly || isProblem(item)) &&
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
        setText(row.querySelector(".module-identity strong"), item.name);
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
        if (
          previous &&
          previous !== item.state &&
          !matchMedia("(prefers-reduced-motion: reduce)").matches
        )
          row.animate(
            [
              { backgroundColor: "var(--panel-2)" },
              { backgroundColor: "transparent" },
            ],
            { duration: 650, easing: "ease-out" },
          );
      }
      row.hidden = !visibleIds.has(item.id);
    }
    const counts = [
      items.length,
      items.filter((i) => i.state === "ready").length,
      items.filter((i) => i.state === "loading").length,
      items.filter(isProblem).length,
    ];
    ["total", "ready", "loading", "problems"].forEach((name, index) => {
      const element = ctx.$(`#modules-${name}`);
      if (element.textContent !== String(counts[index]))
        ctx.animateValue(element, String(counts[index]));
    });
    ctx.$("#modules-progress").style.width =
      `${counts[0] ? (counts[1] / counts[0]) * 100 : 0}%`;
    const empty = ctx.$("#modules-empty");
    empty.hidden = filtered.length > 0;
    ctx.$("#modules-page").textContent =
      `${page + 1} / ${pages} · ${filtered.length} модулей`;
    ctx.$("#modules-prev").disabled = page === 0;
    ctx.$("#modules-next").disabled = page >= pages - 1;
    ctx.$(".modules-overview").dataset.loading = String(
      counts[2] > 0 || !items.length,
    );
    ctx.$(".modules-progress").dataset.loading = String(counts[2] > 0);
    ctx
      .$("#modules-progress")
      .setAttribute(
        "aria-valuenow",
        String(counts[0] ? Math.round((counts[1] / counts[0]) * 100) : 0),
      );
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
    ctx.$("#modules-only-problems").addEventListener("click", (event) => {
      page = 0;
      problemsOnly = !problemsOnly;
      event.currentTarget.setAttribute("aria-pressed", String(problemsOnly));
      event.currentTarget.classList.toggle("active", problemsOnly);
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
