// Двухпанельный журнал: выбор события не растягивает список.
export function createAudit(ctx) {
  const narrow = matchMedia("(max-width: 1050px)");
  let inspectorDialog;
  let selected = "";
  let detailSignature = "";
  const labels = {
    "auth.login": "Вход в панель",
    "auth.logout": "Выход из панели",
    "auth.register": "Регистрация",
    "bot.recover": "Автовосстановление",
    "bot.start": "Запуск бота",
    "bot.stop": "Остановка бота",
    "bot.restart": "Перезапуск бота",
    "config.update": "Изменение настроек",
    "config.restore": "Восстановление настроек",
    "config.delete": "Удаление настройки",
    "watchdog.update": "Настройка восстановления",
    "backup.create": "Создание копии",
    "backup.restore": "Восстановление доступа",
    "diagnostic.startup-log": "Проверка лога запуска",
    "diagnostics.bundle": "Диагностический архив",
  };
  const title = (event) =>
    labels[event.action] ||
    event.detail?.split("\n")[0] ||
    "Действие администратора";
  const category = (event) => event.action?.split(".")[0] || "admin";
  function auditGroups(events) {
    const groups = [],
      identities = new Map();
    for (const event of events) {
      const identity = JSON.stringify([
        event.actor,
        event.action,
        event.detail,
        event.ip,
      ]);
      if (identities.has(identity)) identities.get(identity).events.push(event);
      else {
        const group = { identity, events: [event] };
        groups.push(group);
        identities.set(identity, group);
      }
    }
    return groups;
  }
  function showDetail(group, animate = true) {
    const panel = ctx.$("#audit-inspector");
    const signature = JSON.stringify(group);
    if (signature === detailSignature) return;
    detailSignature = signature;
    panel.replaceChildren();
    if (!group) {
      panel.textContent = "Выберите событие, чтобы посмотреть подробности.";
      return;
    }
    const event = group.events[0];
    const caption = document.createElement("span");
    caption.className = "eyebrow";
    caption.textContent = "ПОДРОБНОСТИ СОБЫТИЯ";
    const heading = document.createElement("h3");
    heading.id = "audit-detail-title";
    heading.textContent = title(event);
    const description = document.createElement("p");
    description.className = "audit-description";
    description.textContent = event.detail || "Без описания";
    const meta = document.createElement("dl");
    for (const [key, value] of [
      ["Автор", event.actor || "—"],
      ["Адрес", event.ip || "—"],
      ["Действие", event.action || "—"],
      ["Последний раз", ctx.formatDate(event.time)],
    ]) {
      const dt = document.createElement("dt");
      dt.textContent = key;
      const dd = document.createElement("dd");
      dd.textContent = value;
      meta.append(dt, dd);
    }
    panel.append(caption, heading, description, meta);
    if (group.events.length > 1) {
      const repeat = document.createElement("p");
      repeat.className = "audit-repeat-label";
      repeat.textContent = `Повторено · ${group.events.length}`;
      const occurrences = document.createElement("div");
      occurrences.className = "audit-occurrences";
      // Без вложенного длинного журнала: последние четыре времени и общий счётчик.
      group.events.slice(0, 4).forEach((event) => {
        const time = document.createElement("time");
        time.textContent = ctx.formatDate(event.time);
        occurrences.append(time);
      });
      if (group.events.length > 4) {
        const remaining = document.createElement("small");
        remaining.textContent = `И ещё ${group.events.length - 4} повторов`;
        occurrences.append(remaining);
      }
      panel.append(repeat, occurrences);
    }
    if (animate && !window.prefersReducedMotion?.())
      panel.animate(
        [
          { opacity: 0, transform: "translateX(10px)" },
          { opacity: 1, transform: "translateX(0)" },
        ],
        { duration: 420, easing: "cubic-bezier(.16,1,.3,1)" },
      );
  }
  function drawAudit(animate = false) {
    const size = matchMedia("(max-width: 560px)").matches ? 4 : 6;
    const query = ctx.$("#audit-search").value.trim().toLocaleLowerCase();
    const groups = auditGroups(ctx.auditEvents).filter(
      (group) =>
        !query ||
        `${JSON.stringify(group.events[0])} ${title(group.events[0])}`
          .toLocaleLowerCase()
          .includes(query),
    );
    const pages = Math.max(1, Math.ceil(groups.length / size));
    ctx.auditPage = Math.min(ctx.auditPage, pages - 1);
    const pageGroups = groups.slice(
      ctx.auditPage * size,
      (ctx.auditPage + 1) * size,
    );
    if (!pageGroups.some((g) => g.identity === selected))
      selected = pageGroups[0]?.identity || "";
    const list = ctx.$("#audit-groups");
    const focused =
      document.activeElement?.closest(".audit-group")?.dataset.key;
    list.replaceChildren();
    ctx.$("#audit-count").textContent =
      `${groups.length} групп · ${ctx.auditEvents.length} событий`;
    ctx.$("#audit-empty").hidden = groups.length !== 0;
    ctx.$("#audit-empty").textContent = query
      ? "По этому запросу действий нет."
      : "Действий пока нет.";
    for (const [index, group] of pageGroups.entries()) {
      const event = group.events[0];
      const button = document.createElement("button");
      button.type = "button";
      button.className = "audit-group";
      button.dataset.key = group.identity;
      button.dataset.category = category(event);
      button.setAttribute("aria-pressed", String(selected === group.identity));
      button.style.setProperty("--entry-delay", `${index * 35}ms`);
      const icon = document.createElement("span");
      icon.className = "audit-icon";
      icon.setAttribute("aria-hidden", "true");
      icon.textContent =
        { bot: "↻", auth: "↗", config: "⚙", watchdog: "◉", backup: "◇" }[
          category(event)
        ] || "•";
      const main = document.createElement("span");
      main.className = "audit-main";
      const heading = document.createElement("strong");
      heading.textContent = title(event);
      const time = document.createElement("small");
      time.textContent = `${event.actor || "—"} · ${ctx.formatDate(event.time)}`;
      main.append(heading, time);
      const count = document.createElement("span");
      count.className = "audit-repeat";
      count.textContent =
        group.events.length > 1 ? `×${group.events.length}` : "→";
      button.append(icon, main, count);
      button.addEventListener("click", () => {
        selected = group.identity;
        list
          .querySelectorAll("button")
          .forEach((row) =>
            row.setAttribute("aria-pressed", String(row === button)),
          );
        showDetail(group);
        if (narrow.matches && !inspectorDialog.open)
          inspectorDialog.showModal();
      });
      list.append(button);
      if (focused === group.identity) button.focus();
    }
    showDetail(
      pageGroups.find((g) => g.identity === selected),
      animate,
    );
    ctx.$("#audit-prev").disabled = ctx.auditPage === 0;
    ctx.$("#audit-more").disabled = ctx.auditPage >= pages - 1;
    ctx.$("#audit-page").textContent = `${ctx.auditPage + 1} / ${pages}`;
    if (animate && !window.prefersReducedMotion?.())
      list.animate(
        [
          { opacity: 0.25, transform: "translateY(6px)" },
          { opacity: 1, transform: "none" },
        ],
        { duration: 380, easing: "ease-out" },
      );
  }
  function renderAudit(events) {
    const signature = JSON.stringify(events);
    if (signature === ctx.auditSignature) return;
    const initial = !ctx.auditSignature;
    ctx.auditSignature = signature;
    ctx.auditEvents = events;
    drawAudit(initial);
  }
  function bindAuditMore() {
    // На узком экране подробности — отдельная доступная карточка, не продолжение длинной страницы.
    inspectorDialog = document.createElement("dialog");
    inspectorDialog.className = "audit-dialog";
    inspectorDialog.setAttribute("aria-labelledby", "audit-detail-title");
    const close = document.createElement("button");
    close.type = "button";
    close.className = "compact audit-close";
    close.textContent = "Закрыть ×";
    const closeDialog = () => {
      if (window.prefersReducedMotion?.()) {
        inspectorDialog.close();
        return;
      }
      close.disabled = true;
      const animation = inspectorDialog.animate(
        [
          { opacity: 1, transform: "translateY(0)" },
          { opacity: 0, transform: "translateY(16px)" },
        ],
        { duration: 240, easing: "ease-in" },
      );
      animation.onfinish = () => {
        inspectorDialog.close();
        close.disabled = false;
      };
    };
    close.addEventListener("click", closeDialog);
    inspectorDialog.addEventListener("cancel", (event) => {
      event.preventDefault();
      closeDialog();
    });
    inspectorDialog.addEventListener("click", (event) => {
      if (event.target === inspectorDialog) closeDialog();
    });
    inspectorDialog.append(close);
    document.body.append(inspectorDialog);
    const moveInspector = () => {
      const inspector = ctx.$("#audit-inspector");
      if (narrow.matches) inspectorDialog.append(inspector);
      else {
        if (inspectorDialog.open) inspectorDialog.close();
        ctx.$(".audit-workspace").append(inspector);
      }
    };
    moveInspector();
    narrow.addEventListener("change", () => {
      moveInspector();
      drawAudit();
    });
    ctx.$("#audit-more").addEventListener("click", () => {
      ctx.auditPage++;
      drawAudit(true);
    });
    ctx.$("#audit-prev").addEventListener("click", () => {
      ctx.auditPage--;
      drawAudit(true);
    });
    ctx.$("#audit-search").addEventListener("input", () => {
      ctx.auditPage = 0;
      drawAudit(true);
    });
  }
  return { auditGroups, drawAudit, renderAudit, bindAuditMore };
}
