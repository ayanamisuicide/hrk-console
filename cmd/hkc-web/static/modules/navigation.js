// Разделы панели, тема, поиск, быстрые команды и уведомления.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createNavigation(ctx) {
  // Выбирает допустимый раздел, обновляет навигацию и сохраняет выбор; данные загружаются после авторизации.
  function setView(view) {
    if (!["logs", "system", "incidents", "modules"].includes(view)) view = "logs";
    ctx.currentView = view;
    document.querySelectorAll(".workspace-view").forEach((panel) => {
      panel.hidden = panel.id !== `${view}-view`;
    });
    document
      .querySelectorAll("[data-view]")
      .forEach((item) =>
        item.classList.toggle("active", item.dataset.view === view),
      );
    ctx
      .$("#journal-nav")
      .classList.toggle(
        "section-active",
        view === "logs" || view === "incidents",
      );
    if (view === "incidents" && typeof ctx.setJournalNavOpen === "function")
      ctx.setJournalNavOpen(true);
    ctx.animateValue(
      ctx.$("#view-title"),
      {
        logs: "Журнал событий",
        system: "Состояние системы",
        incidents: "Происшествия",
        modules: "Модули бота",
      }[view],
    );
    if (view === "incidents" && ctx.authenticated) ctx.refreshIncidents();
    if (view === "system" && ctx.authenticated) {
      ctx.refreshHistory();
      ctx.refreshDetails();
    }
    if (view === "modules" && ctx.authenticated) ctx.refreshModules();
    localStorage.setItem("hkc-view", view);
  }

  // Сохраняет раскрытие группы журнала и синхронизирует визуальное и доступное состояние.
  function setJournalNavOpen(open) {
    ctx.$("#journal-nav").classList.toggle("open", open);
    ctx.$("#journal-nav-toggle").setAttribute("aria-expanded", String(open));
    ctx
      .$("#journal-nav-toggle")
      .setAttribute(
        "aria-label",
        open ? "Свернуть раздел журнала" : "Раскрыть раздел журнала",
      );
    localStorage.setItem("hkc-journal-nav-open", String(open));
  }

  // Показывает сообщение через общий помощник анимации и автоматического скрытия.
  function showNotice(message, kind = "ok") {
    window.motionNotice(ctx.notice, message, kind);
  }

  // Фильтрует команды по запросу и создаёт доступные кнопки результатов.
  function renderCommands() {
    const query = ctx.$("#command-query").value.trim().toLowerCase();
    const matches = ctx.commands.filter((command) =>
      command.name.toLowerCase().includes(query),
    );
    ctx.commandIndex = Math.min(
      ctx.commandIndex,
      Math.max(matches.length - 1, 0),
    );
    const results = ctx.$("#command-results");
    results.replaceChildren();
    for (const [index, command] of matches.entries()) {
      const button = document.createElement("button");
      button.className = `command-item ${index === ctx.commandIndex ? "selected" : ""}`;
      button.textContent = command.name;
      button.addEventListener("click", () => {
        ctx.$("#command-dialog").close();
        command.run();
      });
      results.append(button);
    }
    if (!matches.length) results.textContent = "Ничего не найдено";
  }

  // Открывает палитру команд и переводит фокус в строку поиска.
  function openCommands() {
    ctx.$("#command-query").value = "";
    ctx.commandIndex = 0;
    ctx.renderCommands();
    ctx.$("#command-dialog").showModal();
    ctx.$("#command-query").focus();
  }
  // Перехватывает внутреннюю навигацию без перезагрузки страницы.
  function bindViewLinks() {
    document
      .querySelectorAll("[data-view], [data-jump]")
      .forEach((item) =>
        item.addEventListener("click", () =>
          ctx.setView(item.dataset.view || item.dataset.jump),
        ),
      );
  }

  // Подключает раскрытие группы журнала в боковой панели.
  function bindJournalNavToggle() {
    ctx
      .$("#journal-nav-toggle")
      .addEventListener("click", () =>
        ctx.setJournalNavOpen(
          !ctx.$("#journal-nav").classList.contains("open"),
        ),
      );
  }

  // Подключает поиск, палитру команд и клавиатурные сокращения страницы.
  function bindSearch() {
    document.addEventListener("keydown", (event) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        ctx.openCommands();
        return;
      }
      if (
        event.key === "/" &&
        !document.activeElement?.matches("input, textarea, [contenteditable='true']") &&
        !ctx.authDialog.open
      ) {
        event.preventDefault();
        ctx.setView("logs");
        ctx.filterInput.focus();
      }
      if (
        event.key === "Escape" &&
        document.activeElement === ctx.filterInput
      ) {
        ctx.filterInput.value = "";
        ctx.filterInput.blur();
        ctx.renderLines();
      }
    });
  }

  // Открывает палитру по кнопке.
  function bindCommandOpen() {
    ctx.$("#command-open").addEventListener("click", ctx.openCommands);
  }

  // Перерисовывает результаты палитры при вводе запроса.
  function bindCommandQuery() {
    ctx.$("#command-query").addEventListener("input", () => {
      ctx.commandIndex = 0;
      ctx.renderCommands();
    });
  }

  // Обрабатывает выбор команды стрелками и запуск через Enter.
  function bindCommandQueryKeyboard() {
    ctx.$("#command-query").addEventListener("keydown", (event) => {
      const items = [...document.querySelectorAll(".command-item")];
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        ctx.commandIndex =
          (ctx.commandIndex +
            (event.key === "ArrowDown" ? 1 : -1) +
            items.length) %
          (items.length || 1);
        ctx.renderCommands();
      }
      if (event.key === "Enter" && items[ctx.commandIndex]) {
        event.preventDefault();
        items[ctx.commandIndex].click();
      }
    });
  }

  // Копирует ссылку страницы и сообщает об успехе или недоступности буфера обмена.
  function bindCopyConsoleLink() {
    ctx.$("#copy-console-link").addEventListener("click", async (event) => {
      const button = event.currentTarget;
      try {
        await navigator.clipboard.writeText(location.origin + "/");
        button.textContent = "Ссылка скопирована";
        setTimeout(() => {
          button.textContent = "Скопировать ссылку";
        }, 2000);
      } catch (_) {
        ctx.showNotice("Не удалось скопировать ссылку", "error");
      }
    });
  }

  return {
    setView,
    setJournalNavOpen,
    showNotice,
    renderCommands,
    openCommands,
    bindViewLinks,
    bindJournalNavToggle,
    bindSearch,
    bindCommandOpen,
    bindCommandQuery,
    bindCommandQueryKeyboard,
    bindCopyConsoleLink,
  };
}
