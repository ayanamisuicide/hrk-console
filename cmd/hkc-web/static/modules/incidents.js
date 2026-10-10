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
          const start = new Date(incident.start.replace(" ", "T"));
          const end = new Date(incident.end.replace(" ", "T"));
          const localValue = (date) =>
            `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}T${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
          ctx.$("#time-from").value = localValue(
            new Date(start.getTime() - 120000),
          );
          ctx.$("#time-to").value = localValue(
            new Date(end.getTime() + 120000),
          );
          ctx.$("#module-filter").value = incident.module;
          ctx.activeLevel = "ALL";
          ctx.bookmarksOnly = false;
          ctx.$("#bookmarks-only").setAttribute("aria-pressed", "false");
          document
            .querySelectorAll(".filter-chip")
            .forEach((item) =>
              item.classList.toggle("active", item.dataset.level === "ALL"),
            );
          ctx.setView("logs");
          ctx.renderLines();
          document.querySelector(".log-advanced").open = true;
          ctx.logEl
            .querySelector(
              '.line[data-level="ERROR"], .line[data-level="CRITICAL"]',
            )
            ?.scrollIntoView({ block: "center", behavior: "auto" });
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
