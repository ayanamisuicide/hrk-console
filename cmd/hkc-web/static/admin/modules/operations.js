// Управление ботом, автоматическое восстановление и сводка безопасности.
export function createOperations(ctx) {
  let formDirty = false;

  function renderBot(bot) {
    ctx.animateValue(ctx.$("#admin-bot-status"), bot.running ? "Heroku работает" : "Heroku остановлен");
    ctx.$("#admin-bot-meta").textContent = bot.running
      ? `PID ${bot.pid} · ${bot.uptime} · версия ${bot.version || "—"}` : bot.herokuDir;
    ctx.$("#bot-orbit-dot").classList.toggle("online", bot.running);
    document.querySelector('[data-bot-action="start"]').disabled = bot.running;
    document.querySelector('[data-bot-action="restart"]').disabled = !bot.running;
    document.querySelector('[data-bot-action="stop"]').disabled = !bot.running;
  }

  function renderSecurity(data) {
    const signature = JSON.stringify(data);
    if (signature === ctx.securitySignature) return;
    ctx.securitySignature = signature;
    const labels = [
      ["Пользователи", data.users], ["Активные сессии", data.sessions],
      ["API-токены", data.apiTokens], ["Заблокированные клиенты", data.rateLimiter?.blockedClients || 0],
      ["Терминал", data.features?.terminal ? "включён" : "выключен"],
      ["Доверенный прокси", data.features?.trustedProxy ? "включён" : "выключен"],
    ];
    const grid = ctx.$("#security-overview");
    grid.replaceChildren();
    for (const [label, value] of labels) {
      const card = document.createElement("article");
      const caption = document.createElement("span"); caption.textContent = label;
      const strong = document.createElement("strong"); strong.textContent = value;
      card.append(caption, strong); grid.append(card);
    }
  }

  function renderWatchdog(data) {
    const settings = data.settings || {};
    const status = data.status || {};
    if (!formDirty) {
      ctx.$("#watchdog-enabled").checked = Boolean(settings.enabled);
      ctx.$("#watchdog-timeout").value = settings.timeoutSeconds || 180;
    }
    const labels = { healthy: "Под защитой", waiting: "Ожидание ответа", recovering: "Перезапуск", failed: "Повторная попытка", disabled: "Выключено", unsupported: "Нужен Linux / WSL", starting: "Подключаемся" };
    const badge = ctx.$("#watchdog-state");
    ctx.animateValue(badge, labels[status.state] || "Подключаемся");
    badge.dataset.state = status.state || "starting";
    ctx.$("#watchdog-message").textContent = status.message || "Получаем состояние наблюдения…";
    ctx.animateValue(ctx.$("#watchdog-countdown"), status.remainingSeconds > 0 ? `${status.remainingSeconds} с` : "—");
    ctx.animateValue(ctx.$("#watchdog-attempts"), status.attempts || 0);
    ctx.$("#watchdog-last-result").textContent = status.lastAttempt
      ? `${ctx.formatDate(status.lastAttempt)} · ${status.lastResult}` : "Восстановление пока не требовалось.";
  }

  function bindWatchdogForm() {
    const form = ctx.$("#watchdog-form");
    form.addEventListener("input", () => { formDirty = true; });
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      const button = form.querySelector('[type="submit"]');
      button.disabled = true;
      try {
        const data = await ctx.adminRequest("/api/admin/watchdog", {
          method: "PUT",
          body: JSON.stringify({ enabled: ctx.$("#watchdog-enabled").checked, timeoutSeconds: Number(ctx.$("#watchdog-timeout").value) }),
        });
        formDirty = false;
        renderWatchdog(data);
        ctx.showNotice("Настройки автовосстановления сохранены");
      } catch (error) { ctx.showNotice(error.message, "error"); }
      finally { button.disabled = false; }
    });
  }

  function bindBotActions() {
    document.querySelectorAll("[data-bot-action]").forEach((button) => {
      button.addEventListener("click", async () => {
        const action = button.dataset.botAction;
        button.disabled = true;
        try {
          const result = await ctx.adminRequest(`/api/admin/bot/${action}`, { method: "POST" });
          ctx.showNotice(result.message); setTimeout(ctx.refresh, 500);
        } catch (error) { ctx.showNotice(error.message, "error"); await ctx.refresh(); }
      });
    });
  }

  function bindDownloadDiagnostics() {
    ctx.$("#download-diagnostics").addEventListener("click", async (event) => {
      const button = event.currentTarget; button.disabled = true;
      try {
        const response = await fetch("/api/admin/diagnostic-bundle", { headers: { Authorization: `Bearer ${ctx.adminToken}` } });
        if (!response.ok) {
          const body = await response.json().catch(() => ({}));
          throw new Error(body.message || `HTTP ${response.status}`);
        }
        const blob = await response.blob();
        const match = (response.headers.get("Content-Disposition") || "").match(/filename="([^"]+)"/);
        const link = document.createElement("a");
        link.href = URL.createObjectURL(blob); link.download = match?.[1] || "hkc-diagnostics.zip";
        document.body.append(link); link.click(); link.remove();
        setTimeout(() => URL.revokeObjectURL(link.href), 1000);
        ctx.showNotice("Диагностический архив подготовлен");
      } catch (error) { ctx.showNotice(error.message, "error"); }
      finally { button.disabled = false; }
    });
  }
  return { renderBot, renderSecurity, renderWatchdog, bindWatchdogForm, bindBotActions, bindDownloadDiagnostics };
}
