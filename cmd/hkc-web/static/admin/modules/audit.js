// Компактные строки аудита; подробности и повторы раскрываются по запросу.
export function createAudit(ctx) {
  const actionLabels = {
    "bot.recover": "Автовосстановление бота",
    "bot.start": "Запуск бота",
    "bot.stop": "Остановка бота",
    "bot.restart": "Перезапуск бота",
    "config.update": "Изменение настроек",
    "config.restore": "Восстановление настроек",
    "watchdog.update": "Настройка автовосстановления",
  };
  function auditGroups(events) {
    const groups = [];
    const byIdentity = new Map();
    for (const event of events) {
      const identity = JSON.stringify([
        event.actor,
        event.action,
        event.detail,
        event.ip,
      ]);
      if (byIdentity.has(identity)) byIdentity.get(identity).events.push(event);
      else {
        const group = { identity, events: [event] };
        groups.push(group);
        byIdentity.set(identity, group);
      }
    }
    return groups;
  }

  function drawAudit() {
    const query = ctx.$("#audit-search").value.trim().toLocaleLowerCase();
    const groups = auditGroups(ctx.auditEvents).filter(
      (group) =>
        !query ||
        JSON.stringify(group.events[0]).toLocaleLowerCase().includes(query),
    );
    const list = ctx.$("#audit-groups");
    const focusedKey =
      document.activeElement?.closest(".audit-group")?.dataset.key;
    list.replaceChildren();
    ctx.$("#audit-count").textContent =
      `${groups.length} групп · ${ctx.auditEvents.length} событий`;
    ctx.$("#audit-empty").hidden = groups.length !== 0;
    ctx.$("#audit-empty").textContent = query
      ? "По этому запросу действий нет."
      : "Действий пока нет.";
    groups.slice(0, ctx.auditVisible).forEach((group) => {
      const event = group.events[0];
      const card = document.createElement("details");
      card.className = "audit-group";
      card.dataset.key = group.identity;
      card.open = ctx.auditOpen.has(group.identity);
      const head = document.createElement("summary");
      const main = document.createElement("span");
      main.className = "audit-main";
      const action = document.createElement("strong");
      action.textContent =
        actionLabels[event.action] || event.action || "Действие";
      const preview = document.createElement("small");
      preview.textContent = event.detail || "Без описания";
      main.append(action, preview);
      const actor = document.createElement("span");
      actor.className = "audit-actor";
      actor.textContent = event.actor || "—";
      const time = document.createElement("time");
      time.textContent = ctx.formatDate(event.time);
      const repeat = document.createElement("span");
      repeat.className = "audit-repeat";
      repeat.textContent =
        group.events.length > 1 ? `×${group.events.length}` : "";
      head.append(main, actor, time, repeat);
      const detail = document.createElement("div");
      detail.className = "audit-detail";
      const description = document.createElement("p");
      description.textContent = event.detail || "Без описания";
      const origin = document.createElement("small");
      origin.textContent = `${event.action || "Действие"} · Автор: ${event.actor || "—"} · IP: ${event.ip || "—"}`;
      detail.append(description, origin);
      if (group.events.length > 1) {
        const occurrences = document.createElement("div");
        occurrences.className = "audit-occurrences";
        for (const occurrence of group.events) {
          const row = document.createElement("div");
          row.textContent = ctx.formatDate(occurrence.time);
          occurrences.append(row);
        }
        detail.append(occurrences);
      }
      card.append(head, detail);
      card.addEventListener("toggle", () => {
        if (card.open) ctx.auditOpen.add(group.identity);
        else ctx.auditOpen.delete(group.identity);
      });
      list.append(card);
      if (focusedKey === group.identity) head.focus();
    });
    ctx.$("#audit-more").hidden = ctx.auditVisible >= groups.length;
    ctx.$("#audit-more").textContent =
      `Показать ещё · ${Math.min(8, groups.length - ctx.auditVisible)}`;
    // Удалённые сервером группы не накапливаются в состоянии раскрытия.
    const current = new Set(
      auditGroups(ctx.auditEvents).map((group) => group.identity),
    );
    for (const key of ctx.auditOpen)
      if (!current.has(key)) ctx.auditOpen.delete(key);
  }

  function renderAudit(events) {
    const signature = JSON.stringify(events);
    if (signature === ctx.auditSignature) return;
    ctx.auditSignature = signature;
    ctx.auditEvents = events;
    drawAudit();
  }

  function bindAuditMore() {
    ctx.$("#audit-more").addEventListener("click", () => {
      ctx.auditVisible += 8;
      drawAudit();
    });
    ctx.$("#audit-search").addEventListener("input", () => {
      ctx.auditVisible = 8;
      drawAudit();
    });
  }
  return { auditGroups, drawAudit, renderAudit, bindAuditMore };
}
