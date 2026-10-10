// Проверка настроек без раскрытия секретов, история версий и резервные копии доступа.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createConfig(ctx) {
  // Рисует метаданные копий и подтверждаемое восстановление базы доступа; неизменившийся список не
  // пересобирается.
  function renderBackups(backups) {
    const signature = JSON.stringify(backups);
    if (signature === ctx.backupsSignature) return;
    ctx.backupsSignature = signature;
    const list = ctx.$("#backup-list");
    list.replaceChildren();
    ctx.$("#backup-empty").hidden = backups.length !== 0;
    for (const backup of backups) {
      const row = document.createElement("div");
      row.className = "backup-row";
      const info = document.createElement("div");
      const title = document.createElement("strong");
      title.textContent = ctx.formatDate(backup.createdAt);
      const detail = document.createElement("small");
      detail.textContent = `${backup.name} · ${(backup.size / 1024).toFixed(1)} КБ`;
      info.append(title, detail);
      const restore = document.createElement("button");
      restore.className = "compact";
      restore.textContent = "Восстановить";
      restore.addEventListener("click", async () => {
        if (
          !(await ctx.confirmAction(
            "Восстановить базу доступа?",
            `Будут восстановлены пользователи и инвайты из копии ${backup.name}. Все пользовательские сессии завершатся.`,
          ))
        )
          return;
        try {
          const result = await ctx.adminRequest(
            `/api/admin/backups/${encodeURIComponent(backup.name)}/restore`,
            { method: "POST" },
          );
          ctx.showNotice(result.message);
          await ctx.refresh();
        } catch (error) {
          ctx.showNotice(error.message, "error");
        }
      });
      row.append(info, restore);
      list.append(row);
    }
  }

  // Рисует версии настроек, сравнение ключей и подтверждаемое восстановление.
  function renderConfigHistory(history) {
    const signature = JSON.stringify(history);
    if (signature === ctx.configHistorySignature) return;
    ctx.configHistorySignature = signature;
    const list = ctx.$("#config-history-list");
    list.replaceChildren();
    ctx.$("#config-history-empty").hidden = history.length !== 0;
    for (const version of history) {
      const row = document.createElement("div");
      row.className = "backup-row";
      const info = document.createElement("div");
      const title = document.createElement("strong");
      title.textContent = ctx.formatDate(version.createdAt);
      const detail = document.createElement("small");
      detail.textContent = `${version.name} · ${(version.size / 1024).toFixed(1)} КБ`;
      info.append(title, detail);
      const actions = document.createElement("span");
      actions.className = "row-actions";
      const inspect = document.createElement("button");
      inspect.className = "compact";
      inspect.textContent = "Сравнить";
      inspect.addEventListener("click", async () => {
        try {
          const diff = await ctx.adminRequest(
            `/api/admin/config/history/${encodeURIComponent(version.name)}/diff`,
          );
          const labels = {
            added: "будет добавлен",
            removed: "будет удалён",
            changed: "будет изменён",
          };
          ctx.showNotice(
            diff.changes?.length
              ? diff.changes
                  .map(
                    (item) =>
                      `${item.key}: ${labels[item.change] || item.change}`,
                  )
                  .join("\n")
              : "Отличий от текущей конфигурации нет",
          );
        } catch (error) {
          ctx.showNotice(error.message, "error");
        }
      });
      const restore = document.createElement("button");
      restore.className = "compact";
      restore.textContent = "Восстановить";
      restore.addEventListener("click", async () => {
        if (
          !(await ctx.confirmAction(
            "Восстановить конфигурацию?",
            `Текущая конфигурация сначала будет сохранена. После восстановления ${version.name} перезапустите бота.`,
          ))
        )
          return;
        try {
          const result = await ctx.adminRequest(
            `/api/admin/config/history/${encodeURIComponent(version.name)}/restore`,
            { method: "POST" },
          );
          ctx.showNotice(result.message);
          await ctx.refresh();
        } catch (error) {
          ctx.showNotice(error.message, "error");
        }
      });
      actions.append(inspect, restore);
      row.append(info, actions);
      list.append(row);
    }
  }
  // Показывает только заполненность параметров и подключает удаление с подтверждением; секретные значения не
  // запрашиваются.
  function renderConfig(data) {
    const signature = JSON.stringify(data);
    if (signature === ctx.configSignature) return;
    ctx.configSignature = signature;
    const labels = {
      api_id: "API ID",
      api_hash: "API hash",
      app_name: "App name",
    };
    const status = ctx.$("#config-status");
    status.replaceChildren();
    for (const [key, label] of Object.entries(labels)) {
      const item = document.createElement("div");
      const text = document.createElement("span");
      text.textContent = label;
      const state = document.createElement("strong");
      state.textContent = data.configured?.[key] ? "настроен" : "не задан";
      item.append(text, state);
      if (data.configured?.[key]) {
        const remove = document.createElement("button");
        remove.type = "button";
        remove.className = "compact danger";
        remove.textContent = "Удалить";
        remove.addEventListener("click", async () => {
          if (
            !(await ctx.confirmAction(
              `Удалить ${label}?`,
              "Перед удалением будет создан снимок. Для применения потребуется перезапуск бота.",
            ))
          )
            return;
          try {
            await ctx.adminRequest(
              `/api/admin/config/${encodeURIComponent(key)}`,
              { method: "DELETE" },
            );
            await ctx.refresh();
          } catch (error) {
            ctx.showNotice(error.message, "error");
          }
        });
        item.append(remove);
      }
      status.append(item);
    }
  }

  // Собирает непустые поля формы для частичного изменения настроек.
  function configPayload() {
    return Object.fromEntries(
      [...new FormData(ctx.$("#config-form")).entries()].filter(
        ([, value]) => String(value).trim() !== "",
      ),
    );
  }

  // Проверяет будущие изменения на сервере без сохранения и показывает список затронутых ключей.
  async function previewConfig() {
    const payload = ctx.configPayload();
    const result = await ctx.adminRequest("/api/admin/config/validate", {
      method: "POST",
      body: JSON.stringify(payload),
    });
    const labels = { added: "будет добавлен", changed: "будет изменён" };
    ctx.$("#config-preview-output").textContent = result.changes.length
      ? result.changes
          .map((item) => `${item.key}: ${labels[item.change] || item.change}`)
          .join(" · ")
      : "Фактических изменений нет.";
    return result;
  }
  // Создаёт резервную копию доступа и обновляет административные данные.
  function bindCreateBackup() {
    ctx.$("#create-backup").addEventListener("click", async (event) => {
      const button = event.currentTarget;
      button.disabled = true;
      try {
        await ctx.adminRequest("/api/admin/backups", { method: "POST" });
        ctx.showNotice("Резервная копия создана");
        await ctx.refresh();
      } catch (error) {
        ctx.showNotice(error.message, "error");
      }
      button.disabled = false;
    });
  }

  // Подключает предварительную проверку и вывод её ошибок.
  function bindConfigPreview() {
    ctx.$("#config-preview").addEventListener("click", async () => {
      try {
        await ctx.previewConfig();
      } catch (error) {
        ctx.$("#config-preview-output").textContent = error.message;
      }
    });
  }

  // Проверяет настройки, запрашивает подтверждение и сохраняет только заполненные поля.
  function bindConfigForm() {
    ctx.$("#config-form").addEventListener("submit", async (event) => {
      event.preventDefault();
      try {
        const preview = await ctx.previewConfig();
        if (!preview.changes.length) return;
        const summary = ctx.$("#config-preview-output").textContent;
        if (
          !(await ctx.confirmAction(
            "Сохранить конфигурацию?",
            `${summary}\n\nТекущая версия будет сохранена автоматически. Для применения потребуется перезапуск бота.`,
          ))
        )
          return;
        const result = await ctx.adminRequest("/api/admin/config", {
          method: "PATCH",
          body: JSON.stringify(ctx.configPayload()),
        });
        ctx.$("#config-form").reset();
        ctx.$("#config-preview-output").textContent =
          "Заполните только параметры, которые нужно изменить.";
        ctx.showNotice(result.message);
        await ctx.refresh();
      } catch (error) {
        ctx.showNotice(error.message, "error");
      }
    });
  }

  return {
    renderBackups,
    renderConfigHistory,
    renderConfig,
    configPayload,
    previewConfig,
    bindCreateBackup,
    bindConfigPreview,
    bindConfigForm,
  };
}
