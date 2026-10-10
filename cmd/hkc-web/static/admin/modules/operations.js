// Обслуживание, расписания, сводка безопасности и диагностическая выгрузка.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createOperations(ctx) {
  // Обновляет состояние бота и доступность управляющих кнопок админки.
  function renderBot(bot) {
    ctx.animateValue(
      ctx.$("#admin-bot-status"),
      bot.running ? "Heroku работает" : "Heroku остановлен",
    );
    ctx.$("#admin-bot-meta").textContent = bot.running
      ? `PID ${bot.pid} · ${bot.uptime} · версия ${bot.version || "—"}`
      : bot.herokuDir;
    ctx.$("#bot-orbit-dot").classList.toggle("online", bot.running);
    document.querySelector('[data-bot-action="start"]').disabled = bot.running;
    document.querySelector('[data-bot-action="restart"]').disabled =
      !bot.running;
    document.querySelector('[data-bot-action="stop"]').disabled = !bot.running;
  }

  // Показывает численную сводку безопасности и включённые возможности без пересборки неизменившихся данных.
  function renderSecurity(data) {
    const signature = JSON.stringify(data);
    if (signature === ctx.securitySignature) return;
    ctx.securitySignature = signature;
    const labels = [
      ["Пользователи", data.users],
      ["Активные сессии", data.sessions],
      ["API-токены", data.apiTokens],
      ["Заблокированные клиенты", data.rateLimiter?.blockedClients || 0],
      ["Терминал", data.features?.terminal ? "включён" : "выключен"],
      [
        "Доверенный прокси",
        data.features?.trustedProxy ? "включён" : "выключен",
      ],
    ];
    const grid = ctx.$("#security-overview");
    grid.replaceChildren();
    for (const [label, value] of labels) {
      const card = document.createElement("article");
      const caption = document.createElement("span");
      caption.textContent = label;
      const strong = document.createElement("strong");
      strong.textContent = value;
      card.append(caption, strong);
      grid.append(card);
    }
  }

  // Синхронизирует обслуживание с формой; поле сообщения не меняется, пока в нём находится фокус.
  function renderMaintenance(data) {
    ctx.maintenanceEnabled = Boolean(data.enabled);
    ctx.$("#maintenance-enabled").checked = ctx.maintenanceEnabled;
    if (document.activeElement !== ctx.$("#maintenance-message"))
      ctx.$("#maintenance-message").value = data.message || "";
    ctx.$("#maintenance-state").textContent = ctx.maintenanceEnabled
      ? "Включён"
      : "Выключен";
    ctx
      .$("#maintenance-state")
      .classList.toggle("active", ctx.maintenanceEnabled);
    if (ctx.maintenanceEnabled) {
      document.querySelector('[data-bot-action="start"]').disabled = true;
      document.querySelector('[data-bot-action="restart"]').disabled = true;
    }
  }

  // Рисует очередь и результаты отложенных действий с возможностью отмены допустимых задач.
  function renderSchedules(items) {
    const signature = JSON.stringify(items);
    if (signature === ctx.schedulesSignature) return;
    ctx.schedulesSignature = signature;
    const list = ctx.$("#schedule-list");
    list.replaceChildren();
    ctx.$("#schedule-empty").hidden = items.length !== 0;
    const actionLabels = {
      start: "Запуск",
      stop: "Остановка",
      restart: "Перезапуск",
    };
    const statusLabels = {
      pending: "ожидает",
      running: "выполняется",
      completed: "выполнено",
      failed: "ошибка",
    };
    for (const item of items) {
      const row = document.createElement("div");
      row.className = `backup-row schedule-${item.status}`;
      const info = document.createElement("div");
      const title = document.createElement("strong");
      title.textContent = `${actionLabels[item.action] || item.action} · ${ctx.formatDate(item.runAt)}`;
      const detail = document.createElement("small");
      detail.textContent = `${statusLabels[item.status] || item.status}${item.result ? ` · ${item.result}` : ""}`;
      info.append(title, detail);
      row.append(info);
      if (item.status === "pending") {
        const remove = document.createElement("button");
        remove.className = "compact danger";
        remove.textContent = "Отменить";
        remove.addEventListener("click", async () => {
          try {
            await ctx.adminRequest(
              `/api/admin/schedules/${encodeURIComponent(item.id)}`,
              { method: "DELETE" },
            );
            await ctx.refresh();
          } catch (error) {
            ctx.showNotice(error.message, "error");
          }
        });
        row.append(remove);
      }
      list.append(row);
    }
  }
  // Отправляет выбранное действие бота, блокируя кнопку на время запроса и обновляя статус после ответа.
  function bindBotActions() {
    document.querySelectorAll("[data-bot-action]").forEach((button) => {
      button.addEventListener("click", async () => {
        const action = button.dataset.botAction;
        button.disabled = true;
        try {
          const result = await ctx.adminRequest(`/api/admin/bot/${action}`, {
            method: "POST",
          });
          ctx.showNotice(result.message);
          setTimeout(ctx.refresh, 500);
        } catch (error) {
          ctx.showNotice(error.message, "error");
          await ctx.refresh();
        }
      });
    });
  }

  // Скачивает диагностический ZIP с административной авторизацией и освобождает временный URL.
  function bindDownloadDiagnostics() {
    ctx.$("#download-diagnostics").addEventListener("click", async (event) => {
      const button = event.currentTarget;
      button.disabled = true;
      try {
        const response = await fetch("/api/admin/diagnostic-bundle", {
          headers: { Authorization: `Bearer ${ctx.adminToken}` },
        });
        if (!response.ok) {
          const body = await response.json().catch(() => ({}));
          throw new Error(body.message || `HTTP ${response.status}`);
        }
        const blob = await response.blob();
        const disposition = response.headers.get("Content-Disposition") || "";
        const match = disposition.match(/filename="([^"]+)"/);
        const link = document.createElement("a");
        link.href = URL.createObjectURL(blob);
        link.download = match?.[1] || "hkc-diagnostics.zip";
        document.body.append(link);
        link.click();
        link.remove();
        setTimeout(() => URL.revokeObjectURL(link.href), 1000);
        ctx.showNotice("Диагностический архив подготовлен");
      } catch (error) {
        ctx.showNotice(error.message, "error");
      }
      button.disabled = false;
    });
  }

  // Отправляет изменение режима обслуживания после подтверждения.
  function bindMaintenanceForm() {
    ctx.$("#maintenance-form").addEventListener("submit", async (event) => {
      event.preventDefault();
      const enabled = ctx.$("#maintenance-enabled").checked;
      const stopBot = ctx.$("#maintenance-stop").checked;
      if (
        enabled &&
        stopBot &&
        !(await ctx.confirmAction(
          "Включить обслуживание и остановить бота?",
          "Новые запуски и перезапуски будут заблокированы до выключения режима.",
        ))
      )
        return;
      try {
        await ctx.adminRequest("/api/admin/maintenance", {
          method: "PUT",
          body: JSON.stringify({
            enabled,
            stopBot,
            message: ctx.$("#maintenance-message").value,
          }),
        });
        ctx.$("#maintenance-stop").checked = false;
        ctx.showNotice(
          enabled
            ? "Режим обслуживания включён"
            : "Режим обслуживания выключен",
        );
        await ctx.refresh();
      } catch (error) {
        ctx.showNotice(error.message, "error");
      }
    });
  }

  // Переводит выбранное локальное время в ISO и создаёт отложенное действие после подтверждения.
  function bindScheduleForm() {
    ctx.$("#schedule-form").addEventListener("submit", async (event) => {
      event.preventDefault();
      const localTime = ctx.$("#schedule-run-at").value;
      const runAt = new Date(localTime);
      if (!localTime || Number.isNaN(runAt.getTime())) {
        ctx.showNotice("Укажите корректную дату и время", "error");
        return;
      }
      try {
        await ctx.adminRequest("/api/admin/schedules", {
          method: "POST",
          body: JSON.stringify({
            action: ctx.$("#schedule-action").value,
            runAt: runAt.toISOString(),
          }),
        });
        ctx.showNotice("Действие добавлено в расписание");
        await ctx.refresh();
      } catch (error) {
        ctx.showNotice(error.message, "error");
      }
    });
  }

  return {
    renderBot,
    renderSecurity,
    renderMaintenance,
    renderSchedules,
    bindBotActions,
    bindDownloadDiagnostics,
    bindMaintenanceForm,
    bindScheduleForm,
  };
}
