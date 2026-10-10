// Вход, регистрация и сессионные запросы пользовательской панели.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createAuth(ctx) {
  // Общий запрос пользовательского API: HTTP 401 открывает вход, остальные ошибки превращаются в исключение с
  // сообщением сервера.
  async function request(path, options = {}) {
    const response = await fetch(path, options);
    if (response.status === 401) {
      ctx.authenticated = false;
      if (!ctx.authDialog.open) ctx.openAuth();
      throw new Error("требуется вход");
    }
    const body = await response.json();
    if (!response.ok)
      throw new Error(body.message || `HTTP ${response.status}`);
    return body;
  }

  // Открывает диалог доступа и настраивает поля под выбранный способ входа.
  function openAuth(mode = ctx.authMode) {
    ctx.authMode = mode;
    const registering = mode === "register";
    ctx.$("#auth-title").textContent = registering
      ? "Регистрация по инвайту"
      : "Вход";
    ctx.$("#auth-note").textContent = registering
      ? "Инвайт одноразовый. Придумайте собственные логин и пароль."
      : "Войдите в аккаунт, созданный по инвайту.";
    ctx.$("#invite").hidden = !registering;
    ctx.$("#auth-password-confirm").hidden = !registering;
    ctx.$("#auth-password-confirm").required = registering;
    ctx.$("#auth-password").autocomplete = registering
      ? "new-password"
      : "current-password";
    ctx.$("#auth-switch").textContent = registering
      ? "У меня уже есть аккаунт"
      : "У меня есть инвайт";
    ctx.$("#auth-error").textContent = "";
    if (registering && ctx.initialInvite && !ctx.$("#invite").value)
      ctx.$("#invite").value = ctx.initialInvite;
    if (!ctx.authDialog.open) ctx.authDialog.showModal();
  }

  // Проверяет сессию, применяет ограничения роли, загружает начальные данные и подключает поток журнала.
  async function bootstrap() {
    try {
      const me = await ctx.request("/api/auth/me");
      ctx.authenticated = true;
      ctx.$("#username").textContent = me.username;
      const viewer = me.role === "viewer";
      document.querySelectorAll("[data-action]").forEach((button) => {
        if (viewer) {
          button.hidden = true;
        }
      });
      await Promise.all([
        ctx.refreshPanelVersion(),
        ctx.refreshStatus(),
        ctx.loadHistory(),
        ctx.refreshSystem(),
      ]);
      if (ctx.currentView === "incidents") ctx.refreshIncidents();
      if (ctx.currentView === "system") {
        ctx.refreshHistory();
        ctx.refreshDetails();
      }
      if (ctx.currentView === "modules") ctx.refreshModules();
      ctx.connectStream();
    } catch (error) {
      if (!ctx.authDialog.open) ctx.showNotice(error.message, "error");
    }
  }
  // Переключает форму между входом и регистрацией по приглашению.
  function bindAuthSwitch() {
    ctx
      .$("#auth-switch")
      .addEventListener("click", () =>
        ctx.openAuth(ctx.authMode === "login" ? "register" : "login"),
      );
  }
  // Отправляет учётные данные; при регистрации сначала сверяет повтор пароля, после успеха заново загружает
  // страницу данных.
  function bindAuthForm() {
    ctx.$("#auth-form").addEventListener("submit", async (event) => {
      event.preventDefault();
      const payload = {
        username: ctx.$("#auth-username").value.trim(),
        password: ctx.$("#auth-password").value,
      };
      if (ctx.authMode === "register") {
        payload.invite = ctx.$("#invite").value.trim();
        if (payload.password !== ctx.$("#auth-password-confirm").value) {
          ctx.$("#auth-error").textContent = "пароли не совпадают";
          return;
        }
      }
      try {
        const response = await fetch(`/api/auth/${ctx.authMode}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify(payload),
        });
        const result = await response.json();
        if (!response.ok)
          throw new Error(result.message || "ошибка авторизации");
        ctx.authDialog.close();
        await ctx.bootstrap();
      } catch (error) {
        ctx.$("#auth-error").textContent = error.message;
      }
    });
  }

  // Завершает серверную сессию, закрывает SSE и возвращает диалог входа.
  function bindLogout() {
    ctx.$("#logout").addEventListener("click", async () => {
      await ctx.request("/api/auth/logout", { method: "POST" });
      ctx.authenticated = false;
      if (ctx.stream) ctx.stream.close();
      ctx.openAuth("login");
    });
  }

  return {
    request,
    openAuth,
    bootstrap,
    bindAuthSwitch,
    bindAuthForm,
    bindLogout,
  };
}
