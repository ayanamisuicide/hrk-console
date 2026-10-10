// Компактный аудит с группировкой повторов и сохранением раскрытых подробностей.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createAudit(ctx) {
  // Объединяет повторяющиеся события аудита в группы для компактного списка.
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

  // Выбирает русскую форму подписи количества повторений.
  function repetitionLabel(count) {
    const suffix =
      count % 100 >= 11 && count % 100 <= 14
        ? "раз"
        : count % 10 >= 2 && count % 10 <= 4
          ? "раза"
          : "раз";
    return `${count} ${suffix}`;
  }

  // Рисует ограниченную часть сгруппированного аудита, сохраняя раскрытые группы.
  function drawAudit() {
    const groups = ctx.auditGroups(ctx.auditEvents);
    const list = ctx.$("#audit-groups");
    list.replaceChildren();
    ctx.$("#audit-empty").hidden = groups.length !== 0;
    groups.slice(0, ctx.auditVisible).forEach((group, index) => {
      const event = group.events[0];
      const key = group.identity;
      const card = document.createElement("article");
      card.className = "audit-group";
      const head = document.createElement("div");
      head.className = "audit-group-head";
      const action = document.createElement("strong");
      action.textContent = event.action || "Действие";
      const detail = document.createElement("p");
      detail.textContent = event.detail || "Без описания";
      const meta = document.createElement("span");
      meta.textContent = `${event.actor || "—"} · ${ctx.formatDate(event.time)} · ${event.ip || "—"}`;
      const main = document.createElement("div");
      main.append(action, detail, meta);
      head.append(main);
      if (group.events.length > 1) {
        const button = document.createElement("button");
        button.className = "compact audit-toggle";
        const panel = document.createElement("div");
        panel.className = "audit-expander";
        panel.id = `audit-detail-${index}`;
        const inner = document.createElement("div");
        inner.className = "audit-expander-inner";
        for (const occurrence of group.events) {
          const row = document.createElement("div");
          row.className = "audit-occurrence";
          const time = document.createElement("time");
          time.textContent = ctx.formatDate(occurrence.time);
          const copy = document.createElement("span");
          copy.textContent = `${occurrence.actor || "—"} · ${occurrence.ip || "—"}`;
          row.append(time, copy);
          inner.append(row);
        }
        panel.append(inner);
        const toggle = (open) => {
          card.classList.toggle("open", open);
          button.setAttribute("aria-expanded", String(open));
          button.textContent = `${ctx.repetitionLabel(group.events.length)} ${open ? "▴" : "▾"}`;
          panel.inert = !open;
          panel.setAttribute("aria-hidden", String(!open));
          if (open) ctx.auditOpen.add(key);
          else ctx.auditOpen.delete(key);
        };
        button.setAttribute("aria-controls", panel.id);
        button.addEventListener("click", () =>
          toggle(!card.classList.contains("open")),
        );
        head.append(button);
        card.append(head, panel);
        toggle(ctx.auditOpen.has(key));
      } else card.append(head);
      list.append(card);
    });
    ctx.$("#audit-more").hidden = ctx.auditVisible >= groups.length;
    ctx.$("#audit-more").textContent =
      `Показать ещё · ${Math.min(12, groups.length - ctx.auditVisible)}`;
  }

  // Сохраняет новые события и перерисовывает аудит только при изменении сигнатуры.
  function renderAudit(events) {
    const signature = JSON.stringify(events);
    if (signature === ctx.auditSignature) return;
    ctx.auditSignature = signature;
    ctx.auditEvents = events;
    ctx.drawAudit();
  }
  // Увеличивает число видимых групп аудита.
  function bindAuditMore() {
    ctx.$("#audit-more").addEventListener("click", () => {
      ctx.auditVisible += 12;
      ctx.drawAudit();
    });
  }

  return {
    auditGroups,
    repetitionLabel,
    drawAudit,
    renderAudit,
    bindAuditMore,
  };
}
