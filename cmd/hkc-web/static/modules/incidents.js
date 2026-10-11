// Список происшествий и переход из группы ошибок к отфильтрованному журналу.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createIncidents(ctx) {
  // Загружает происшествия, не пересобирая неизменившийся список; переход к событию задаёт соответствующие
  // фильтры журнала.
  async function refreshIncidents() {
    if (ctx.incidentsBusy) return;
    ctx.incidentsBusy = true;
    const container = ctx.$("#incident-list");
    try {
      const data = await ctx.request("/api/incidents");
      const signature = JSON.stringify(data.incidents || []);
      if (signature === ctx.incidentsSignature) return;
      ctx.incidentsSignature = signature;
      container.replaceChildren();
      if (!data.incidents?.length) {
        const empty = document.createElement("p");
        empty.className = "empty-state";
        empty.textContent = "В доступной части журнала происшествий нет.";
        container.append(empty);
        return;
      }
      for (const incident of data.incidents) {
        const card = document.createElement("button");
        card.className = "incident-card";
        card.type = "button";
        const meta = document.createElement("span");
        meta.className = "incident-meta";
        meta.textContent = `${incident.level} · ${incident.module} · ${incident.count} событий · ${incident.start}${incident.restarts ? ` · перезапусков рядом: ${incident.restarts}` : ""}`;
        const title = document.createElement("strong");
        title.textContent = incident.title || "Ошибка без описания";
        const context = document.createElement("small");
        context.textContent = incident.context
          ? `Перед ошибкой: ${incident.context}`
          : "Предшествующей строки нет";
        const action = document.createElement("em");
        action.textContent = "Открыть этот интервал в журнале →";
        card.append(meta, title, context, action);
        card.addEventListener("click", () => {
          // Время происшествия задано в часовом поясе сервера, как и записи журнала.
          const start = new Date(incident.start.replace(" ", "T")).getTime();
          const end = new Date(incident.end.replace(" ", "T")).getTime();
          ctx.openJournal({
            module: incident.module,
            from: start - 120000,
            to: end + 120000,
            focusError: true,
          });
        });
        container.append(card);
      }
    } catch (error) {
      container.textContent = `Не удалось загрузить происшествия: ${error.message}`;
    } finally {
      ctx.incidentsBusy = false;
    }
  }
  // Подключает ручную проверку происшествий.
  function bindIncidentsRefresh() {
    ctx.$("#incidents-refresh").addEventListener("click", ctx.refreshIncidents);
  }

  return { refreshIncidents, bindIncidentsRefresh };
}
