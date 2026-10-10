// Административный токен и общий цикл загрузки разделов админки.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createAuth(ctx) {
  // Добавляет административный Bearer-токен. При HTTP 401 удаляет сохранённый токен и снова запрашивает
  // доступ.
  async function adminRequest(path, options = {}) {
    const headers = new Headers(options.headers || {});
    headers.set("Authorization", `Bearer ${ctx.adminToken}`);
    if (options.body) headers.set("Content-Type", "application/json");
    const response = await fetch(path, { ...options, headers });
    const body = await response.json();
    if (response.status === 401) {
      sessionStorage.removeItem("hkc-admin-token");
      ctx.adminToken = "";
      ctx.openAuth("Неверный или изменившийся административный токен");
      throw new Error(body.message || "требуется административный токен");
    }
    if (!response.ok)
      throw new Error(body.message || `HTTP ${response.status}`);
    return body;
  }

  // Открывает диалог административного токена и показывает причину повторного входа.
  function openAuth(message = "") {
    ctx.$("#admin-auth-error").textContent = message;
    if (!ctx.authDialog.open) ctx.authDialog.showModal();
  }

  // Обновляет административные разделы параллельными запросами; флаг занятости защищает от перекрытия циклов.
  async function refresh() {
    if (ctx.refreshBusy || document.hidden) return;
    if (!ctx.adminToken) {
      ctx.openAuth();
      return;
    }
    ctx.refreshBusy = true;
    try {
      const [
        data,
        audit,
        backups,
        configHistory,
        security,
        watchdog,
        alerts,
        config,
        telegram,
      ] = await Promise.all([
        ctx.adminRequest("/api/admin/overview"),
        ctx.adminRequest("/api/admin/audit"),
        ctx.adminRequest("/api/admin/backups"),
        ctx.adminRequest("/api/admin/config/history"),
        ctx.adminRequest("/api/admin/security"),
        ctx.adminRequest("/api/admin/watchdog"),
        // Уведомления необязательны: ошибка этого запроса не должна ломать остальную админку.
        ctx.adminRequest("/api/admin/alerts").catch(() => null),
        ctx.adminRequest("/api/admin/config"),
        // Раздел Telegram тоже необязателен для остальной админки.
        ctx.adminRequest("/api/admin/telegram").catch(() => null),
      ]);
      ctx.animateValue(ctx.$("#users-count"), data.users.length);
      ctx.animateValue(
        ctx.$("#online-count"),
        data.users.filter((user) => user.online).length,
      );
      ctx.animateValue(ctx.$("#invites-count"), data.invites.length);
      ctx.renderBot(data.bot);
      ctx.renderUsers(data.users);
      ctx.renderInvites(data.invites);
      ctx.renderAudit(audit.events || []);
      ctx.renderBackups(backups.backups || []);
      ctx.renderConfigHistory(configHistory.history || []);
      ctx.renderSecurity(security);
      ctx.renderWatchdog(watchdog);
      ctx.renderAlerts(alerts);
      ctx.renderConfig(config);
      ctx.renderTelegram(telegram);
      await ctx.refreshUpdates();
    } catch (error) {
      // Во время обновления панель перезапускается: падение запросов ожидаемо, его показывает страница обновлений.
      if (ctx.adminToken && !ctx.updateRunning && !ctx.panelRestarting) ctx.showNotice(error.message, "error");
    } finally {
      ctx.refreshBusy = false;
    }
  }
  // Проверяет введённый токен перед сохранением в sessionStorage текущей вкладки.
  function bindAdminAuthForm() {
    ctx.$("#admin-auth-form").addEventListener("submit", async (event) => {
      event.preventDefault();
      ctx.adminToken = ctx.$("#admin-token").value.trim();
      try {
        await ctx.adminRequest("/api/admin/overview");
        sessionStorage.setItem("hkc-admin-token", ctx.adminToken);
        ctx.authDialog.close();
        await ctx.refresh();
      } catch (error) {
        ctx.$("#admin-auth-error").textContent = error.message;
      }
    });
  }

  // Удаляет административный токен из вкладки и снова открывает вход.
  function bindAdminLogout() {
    ctx.$("#admin-logout").addEventListener("click", () => {
      sessionStorage.removeItem("hkc-admin-token");
      ctx.adminToken = "";
      ctx.openAuth();
    });
  }

  // На время ручного обновления отключает кнопку и затем возвращает её подпись.
  function bindAdminRefresh() {
    ctx.$("#admin-refresh").addEventListener("click", async (event) => {
      const button = event.currentTarget;
      button.disabled = true;
      button.textContent = "Обновление…";
      await ctx.refresh();
      button.textContent = "↻ Обновить";
      button.disabled = false;
    });
  }

  return {
    adminRequest,
    openAuth,
    refresh,
    bindAdminAuthForm,
    bindAdminLogout,
    bindAdminRefresh,
  };
}
