// Навигация по рабочим областям и вкладкам истории; формы остаются в DOM.
export function createNavigation(ctx) {
  const pages = {
    operations: [
      "01 / НАБЛЮДЕНИЕ",
      "Автовосстановление",
      "Бот вернётся в работу после остановки или зависания.",
    ],
    telegram: [
      "02 / TELEGRAM",
      "Telegram",
      "Уведомления, команды бота и мини-приложение панели.",
    ],
    access: [
      "03 / ДОСТУП",
      "Пользователи и инвайты",
      "Аккаунты, роли и приглашения в панель.",
    ],
    settings: [
      "04 / НАСТРОЙКИ",
      "Конфигурация и защита",
      "Параметры Heroku и состояние безопасности.",
    ],
    history: [
      "05 / ИСТОРИЯ",
      "История и восстановление",
      "События, резервные копии доступа и версии настроек.",
    ],
    updates: [
      "06 / ОБНОВЛЕНИЯ",
      "Версии и установка",
      "Что нового, установка новой версии и возврат к прошлой.",
    ],
  };

  function setAdminView(view) {
    if (!pages[view]) view = "operations";
    document.querySelectorAll("[data-admin-page]").forEach((page) => {
      page.hidden = page.dataset.adminPage !== view;
    });
    document.querySelectorAll("[data-admin-view]").forEach((button) => {
      const active = button.dataset.adminView === view;
      button.classList.toggle("active", active);
      if (active) button.setAttribute("aria-current", "page");
      else button.removeAttribute("aria-current");
    });
    ["index", "title", "description"].forEach((name, index) => {
      ctx.$(`#admin-page-${name}`).textContent = pages[view][index];
    });
    localStorage.setItem("hkc-admin-view", view);
  }

  function setHistoryTab(tab, focus = false) {
    if (!["audit", "backups", "config"].includes(tab)) tab = "audit";
    document.querySelectorAll("[data-history-tab]").forEach((button) => {
      const active = button.dataset.historyTab === tab;
      button.setAttribute("aria-selected", String(active));
      button.tabIndex = active ? 0 : -1;
      ctx.$(`#${button.getAttribute("aria-controls")}`).hidden = !active;
      if (active && focus) button.focus();
    });
    localStorage.setItem("hkc-admin-history-tab", tab);
  }

  function bindAdminNavigation() {
    let view = localStorage.getItem("hkc-admin-view");
    if (!view) {
      try {
        view = JSON.parse(localStorage.getItem("hkc-admin-tree") || "[]").at(
          -1,
        );
      } catch (_) {}
    }
    setAdminView(view);
    setHistoryTab(localStorage.getItem("hkc-admin-history-tab"));
    document
      .querySelectorAll("[data-admin-view]")
      .forEach((button) =>
        button.addEventListener("click", () =>
          setAdminView(button.dataset.adminView),
        ),
      );
    const tabs = [...document.querySelectorAll("[data-history-tab]")];
    tabs.forEach((button, index) => {
      button.addEventListener("click", () =>
        setHistoryTab(button.dataset.historyTab),
      );
      button.addEventListener("keydown", (event) => {
        let next;
        if (event.key === "ArrowRight") next = (index + 1) % tabs.length;
        if (event.key === "ArrowLeft")
          next = (index + tabs.length - 1) % tabs.length;
        if (event.key === "Home") next = 0;
        if (event.key === "End") next = tabs.length - 1;
        if (next === undefined) return;
        event.preventDefault();
        setHistoryTab(tabs[next].dataset.historyTab, true);
      });
    });
  }

  // По умолчанию кнопка подтверждения «опасная»; для мирных действий передают tone: "primary".
  function confirmAction(title, message, { accept = "Подтвердить", tone = "danger" } = {}) {
    const dialog = ctx.$("#confirm-dialog");
    ctx.$("#confirm-title").textContent = title;
    ctx.$("#confirm-message").textContent = message;
    const button = ctx.$("#confirm-accept");
    button.textContent = accept;
    button.className = tone;
    dialog.showModal();
    return new Promise((resolve) =>
      dialog.addEventListener(
        "close",
        () => resolve(dialog.returnValue === "confirm"),
        { once: true },
      ),
    );
  }

  function formatDate(value) {
    if (!value) return "никогда";
    return new Intl.DateTimeFormat("ru-RU", {
      dateStyle: "medium",
      timeStyle: "short",
    }).format(new Date(value));
  }

  function showNotice(message, kind = "ok") {
    window.motionNotice(ctx.$("#admin-notice"), message, kind);
  }

  return {
    setAdminView,
    setHistoryTab,
    bindAdminNavigation,
    confirmAction,
    formatDate,
    showNotice,
  };
}
