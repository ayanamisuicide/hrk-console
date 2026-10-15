// Мини-приложение Telegram — это сама панель, открытая по адресу /tg/. Отличается только вход:
// вместо логина и пароля клиент Telegram подписывает данные запуска, сервер проверяет подпись и
// выдаёт сессию администратору из HKC_TELEGRAM_ADMIN_IDS. Сессия идёт заголовком во всех запросах
// панели, тема — из Telegram, а вместо ссылки на админку — раздел «Управление» (страница Telegram).
import { openEventStream } from "./event-stream.js";
import { createTelegramPage } from "./telegram-page.js";

export const telegramMode = location.pathname.startsWith("/tg/");

// Telegram кладёт данные запуска в адрес (#tgWebAppData=…); панель потом пишет в адрес свой
// раздел журнала, поэтому их запоминаем сразу при загрузке.
const launchData = new URLSearchParams(location.hash.slice(1)).get("tgWebAppData") || "";

export function createTelegramShell(ctx) {
  let token = "";
  let tg = null;
  let page = null;
  let controlData = null;
  let chrome = false;

  // Мост Telegram нужен только здесь. Если он не загрузится, данные запуска есть в адресе.
  function loadBridge() {
    if (window.Telegram?.WebApp) return Promise.resolve();
    return new Promise((resolve) => {
      const script = document.createElement("script");
      script.src = "https://telegram.org/js/telegram-web-app.js";
      const done = () => resolve();
      script.addEventListener("load", done);
      script.addEventListener("error", done);
      setTimeout(done, 4000);
      document.head.append(script);
    });
  }

  function initData() {
    return tg?.initData || launchData;
  }

  // Тема панели следует за Telegram; шапка Telegram — цвета фона панели.
  function applyChrome() {
    const root = document.documentElement;
    root.dataset.shell = "telegram";
    const apply = (scheme) => {
      root.dataset.theme = scheme === "light" ? "light" : "dark";
      try {
        const color = getComputedStyle(document.body).backgroundColor;
        const rgb = color.match(/\d+/g)?.slice(0, 3).map(Number);
        if (rgb) {
          const hex = `#${rgb.map((n) => n.toString(16).padStart(2, "0")).join("")}`;
          tg?.setHeaderColor?.(hex);
          tg?.setBackgroundColor?.(hex);
        }
      } catch (_) {
        /* Старые клиенты не умеют менять цвета. */
      }
    };
    if (tg) {
      tg.ready();
      tg.expand();
      // Журнал прокручивается вертикально — свайп не должен сворачивать приложение.
      tg.disableVerticalSwipes?.();
      apply(tg.colorScheme);
      tg.onEvent?.("themeChanged", () => apply(tg.colorScheme));
    } else {
      const query = matchMedia("(prefers-color-scheme: light)");
      apply(query.matches ? "light" : "dark");
    }
    // Чего в мини-приложении нет: выхода, ссылки на админку, переключателя темы и ссылки на страницу.
    for (const selector of ["#logout", "#admin-link", "#theme-toggle", "#copy-console-link"]) {
      const element = ctx.$(selector);
      if (element) element.hidden = true;
    }
    ctx.$('[data-view="control"]').hidden = false;
  }

  // Окно входа: в том же виде, что вход панели. Пока оно открыто, панель ничего не опрашивает.
  function gate(title, text, { copy = "", retry = false } = {}) {
    ctx.authenticated = false;
    ctx.stream?.close();
    const dialog = ctx.$("#tg-gate");
    ctx.$("#tg-gate-title").textContent = title;
    ctx.$("#tg-gate-text").textContent = text;
    const copyButton = ctx.$("#tg-gate-copy");
    copyButton.hidden = !copy;
    copyButton.textContent = copy;
    copyButton.onclick = () => navigator.clipboard?.writeText(copy).then(() => (copyButton.textContent = "Скопировано ✓"));
    ctx.$("#tg-gate-retry").hidden = !retry;
    if (!dialog.open) dialog.showModal();
  }

  async function login() {
    const data = initData();
    if (!data) {
      gate("Откройте из Telegram", "Это панель для Telegram: кнопка «Панель» в чате с ботом или команда /app.");
      return false;
    }
    let response;
    try {
      response = await fetch("/api/tg/session", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ initData: data }),
      });
    } catch (_) {
      gate("Панель не отвечает", "Проверьте, что сервер работает и устройство подключено к сети.", { retry: true });
      return false;
    }
    const body = await response.json().catch(() => ({}));
    if (response.status === 403) {
      gate("Нет доступа", "Этого аккаунта нет в списке администраторов. Добавьте строку ниже в /etc/hkc/hkc.env и перезапустите панель.", {
        copy: `HKC_TELEGRAM_ADMIN_IDS="${body.id}"`,
      });
      return false;
    }
    if (response.status === 503) {
      const reason = body.message ? `${body.message[0].toUpperCase()}${body.message.slice(1)}.` : "Управление из Telegram выключено.";
      gate("Приложение выключено", reason, { retry: true });
      return false;
    }
    if (!response.ok) {
      gate("Не удалось войти", body.message || `Ошибка ${response.status}`, { retry: true });
      return false;
    }
    token = body.token;
    if (ctx.$("#tg-gate").open) ctx.$("#tg-gate").close();
    return true;
  }

  // Повторный вход один на все запросы, которые одновременно узнали, что сессия истекла.
  let relogin = null;
  async function loginAgain() {
    relogin ||= login().finally(() => (relogin = null));
    return relogin;
  }

  // Запрос панели с сессией Telegram. Истёкшая сессия (или панель, перезапущенная обновлением)
  // обновляется один раз по тем же данным запуска.
  async function request(path, options = {}, retry = true) {
    const headers = new Headers(options.headers || {});
    headers.set("Authorization", `Bearer ${token}`);
    if (options.body) headers.set("Content-Type", "application/json");
    const response = await fetch(path, { ...options, headers });
    if ((response.status === 401 || response.status === 503) && retry && (await loginAgain())) return request(path, options, false);
    const body = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(body.message || `HTTP ${response.status}`);
    return body;
  }

  function confirmAction(title, message, { accept = "Подтвердить", tone = "danger" } = {}) {
    const dialog = ctx.$("#confirm-dialog");
    ctx.$("#confirm-title").textContent = title;
    ctx.$("#confirm-message").textContent = message;
    const button = ctx.$("#confirm-accept");
    button.textContent = accept;
    button.className = tone;
    dialog.returnValue = "";
    dialog.showModal();
    return new Promise((resolve) => dialog.addEventListener("close", () => resolve(dialog.returnValue === "confirm"), { once: true }));
  }

  // Остановка и перезапуск спрашивают подтверждение, если в настройках Telegram оно включено.
  async function confirmBotAction(action) {
    if (action === "start") return true;
    if (controlData && !controlData.settings?.confirm) return true;
    const stop = action === "stop";
    return confirmAction(
      stop ? "Остановить бота?" : "Перезапустить бота?",
      stop
        ? controlData?.watchdog?.enabled
          ? "Автовосстановление включено и через время запустит бота снова. Чтобы он не поднимался, выключите его в «Управлении»."
          : "Бот останется остановленным, пока вы не запустите его снова."
        : "Бот будет недоступен несколько секунд, затем запустится снова.",
      { accept: stop ? "Остановить" : "Перезапустить", tone: stop ? "danger" : "primary" },
    );
  }

  const formatDate = (value) =>
    value ? new Intl.DateTimeFormat("ru-RU", { dateStyle: "medium", timeStyle: "short" }).format(new Date(value)) : "никогда";

  async function refreshControl() {
    try {
      controlData = await request("/api/telegram");
      page?.render(controlData);
    } catch (error) {
      if (ctx.currentView === "control") ctx.showNotice(error.message, "error");
    }
  }

  async function startTelegram() {
    if (!chrome) {
      chrome = true;
      await loadBridge();
      tg = window.Telegram?.WebApp || null;
      applyChrome();
    }
    ctx.$("#tg-gate-retry").onclick = () => startTelegram();
    gate("Подключаемся…", "Проверяем доступ через Telegram.");
    if (!(await login())) return;
    ctx.request = request;
    ctx.openStream = (url) => openEventStream(url, () => ({ Authorization: `Bearer ${token}` }));
    ctx.confirmBotAction = confirmBotAction;
    ctx.refreshControl = refreshControl;
    if (!page) {
      page = createTelegramPage({ root: ctx.$("#control-page"), request, notice: ctx.showNotice, confirm: confirmAction, formatDate });
      page.mount();
    }
    refreshControl();
    await ctx.bootstrap();
  }

  return { startTelegram };
}
