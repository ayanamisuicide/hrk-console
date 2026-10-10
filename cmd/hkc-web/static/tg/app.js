// Мини-приложение Telegram: вход по подписанным данным запуска, три экрана и живые данные.
// Данные бота и журнала выводятся только через textContent.
import { createSparkline } from "/live-chart.js";

const tg = window.Telegram?.WebApp;
const $ = (selector) => document.querySelector(selector);
const state = {
  token: "",
  tab: "home",
  overview: null,
  confirm: true,
  busy: "",
  modules: null,
  moduleFilter: "all",
  moduleQuery: "",
  open: new Set(),
  logLevel: "all",
  logLines: [],
  timers: [],
  sparks: new Map(),
  failures: 0,
};

const haptic = {
  select: () => tg?.HapticFeedback?.selectionChanged(),
  tap: () => tg?.HapticFeedback?.impactOccurred("medium"),
  done: (ok) => tg?.HapticFeedback?.notificationOccurred(ok ? "success" : "error"),
};

const setText = (element, text) => {
  if (element && element.textContent !== String(text)) element.textContent = text;
};

// Данные запуска: от моста, а если он не загрузился — из адреса, куда их кладёт клиент Telegram.
function initData() {
  if (tg?.initData) return tg.initData;
  return new URLSearchParams(location.hash.slice(1)).get("tgWebAppData") || "";
}

function gate(kind, title, text, extra = {}) {
  const element = $("#gate");
  element.hidden = false;
  element.dataset.state = kind;
  setText($("#gate-title"), title);
  setText($("#gate-text"), text);
  const copy = $("#gate-copy");
  copy.hidden = !extra.copy;
  if (extra.copy) {
    copy.textContent = extra.copy;
    copy.onclick = () => {
      navigator.clipboard?.writeText(extra.copy).then(() => toast("Скопировано"));
      haptic.select();
    };
  }
  $("#gate-retry").hidden = !extra.retry;
  $("#app").hidden = true;
  $("#tabbar").hidden = true;
}

let toastTimer;
function toast(message, kind = "ok") {
  const element = $("#toast");
  element.hidden = false;
  element.dataset.kind = kind;
  element.textContent = message;
  // Перезапуск анимации для повторного сообщения.
  element.style.animation = "none";
  void element.offsetWidth;
  element.style.animation = "";
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => (element.hidden = true), 3200);
}

async function login() {
  const data = initData();
  if (!data) {
    gate("outside", "Откройте из Telegram", "Это приложение работает только внутри Telegram: кнопка «Панель» в чате с ботом или команда /app.");
    return false;
  }
  let response;
  try {
    response = await fetch("/api/tg/session", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ initData: data }),
    });
  } catch {
    gate("error", "Панель не отвечает", "Проверьте, что сервер работает и устройство подключено к сети (например, к Tailscale).", { retry: true });
    return false;
  }
  const body = await response.json().catch(() => ({}));
  if (response.status === 403) {
    gate("forbidden", "Нет доступа", "Этого аккаунта нет в списке администраторов. Добавьте строку ниже в /etc/hkc/hkc.env и перезапустите панель.", {
      copy: `HKC_TELEGRAM_ADMIN_IDS="${body.id}"`,
    });
    return false;
  }
  if (response.status === 503) {
    gate("unavailable", "Приложение выключено", body.message ? `${body.message[0].toUpperCase()}${body.message.slice(1)}.` : "Управление из Telegram выключено.", { retry: true });
    return false;
  }
  if (!response.ok) {
    gate("error", "Не удалось войти", body.message || `Ошибка ${response.status}`, { retry: true });
    return false;
  }
  state.token = body.token;
  return true;
}

// Запрос с сессией. Истёкшая сессия обновляется один раз по тем же данным запуска.
async function api(path, options = {}, retry = true) {
  const response = await fetch(path, {
    ...options,
    headers: { ...(options.headers || {}), Authorization: `Bearer ${state.token}` },
  });
  if (response.status === 401 && retry && (await login())) return api(path, options, false);
  const body = await response.json().catch(() => ({}));
  if (response.status === 503) {
    gate("unavailable", "Приложение выключено", body.message || "Управление из Telegram выключено.", { retry: true });
    stopPolling();
  }
  if (!response.ok) throw Object.assign(new Error(body.message || `Ошибка ${response.status}`), { status: response.status });
  return body;
}

/* ---------- Главная ---------- */

const formatBytes = (bytes) => {
  if (!(bytes > 0)) return "—";
  const units = ["Б", "КБ", "МБ", "ГБ", "ТБ"];
  const index = Math.min(units.length - 1, Math.floor(Math.log(bytes) / Math.log(1024)));
  const value = bytes / 1024 ** index;
  return `${value >= 100 || index === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[index]}`;
};
const formatDuration = (seconds) => {
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  if (days) return `${days} д ${hours} ч`;
  const minutes = Math.floor((seconds % 3600) / 60);
  return hours ? `${hours} ч ${minutes} мин` : `${minutes} мин`;
};
const level = (value) => (value >= 90 ? "bad" : value >= 75 ? "warn" : "");

function renderOverview(data) {
  state.overview = data;
  state.confirm = data.confirm;
  const bot = data.bot;
  const hero = $("#hero");
  hero.dataset.state = state.busy ? "busy" : bot.running ? "running" : "stopped";
  setText($("#bot-title"), state.busy ? { start: "Запускаем…", stop: "Останавливаем…", restart: "Перезапускаем…" }[state.busy] : bot.running ? "Работает" : "Остановлен");
  setText($("#bot-subtitle"), bot.running ? `${bot.uptime} · PID ${bot.pid}` : "Процесс Heroku не запущен");
  setText($("#bot-cpu"), bot.running && bot.cpuPercent != null ? `${bot.cpuPercent.toFixed(1)}%` : "—");
  setText($("#bot-rss"), bot.running ? formatBytes(bot.rssBytes) : "—");
  setText($("#bot-version"), bot.version || "—");
  document.querySelectorAll("[data-action]").forEach((button) => {
    const action = button.dataset.action;
    button.disabled = Boolean(state.busy) || (action === "start" ? bot.running : !bot.running);
    if (state.busy === action) button.dataset.busy = "";
    else delete button.dataset.busy;
  });
  // Главная кнопка — та, что сейчас нужнее.
  $('[data-action="start"]').classList.toggle("primary", !bot.running);
  $('[data-action="restart"]').classList.toggle("primary", bot.running);

  const watchdog = data.watchdog || {};
  const row = $("#watchdog");
  const suspended = watchdog.state === "suspended";
  const recovering = ["waiting", "recovering"].includes(watchdog.state);
  row.dataset.state = !watchdog.enabled ? "off" : suspended ? "bad" : recovering ? "warn" : "on";
  setText($("#watchdog-pill"), !watchdog.enabled ? "Выкл" : suspended ? "Пауза" : recovering ? "Чинит" : "Вкл");
  setText($("#watchdog-text"), watchdog.enabled ? watchdog.message || "Следит за процессом" : "Бот не поднимется сам после падения");

  const system = data.system;
  setText($("#host-line"), data.host || "");
  for (const [key, value] of [["cpu", system.cpuPercent], ["memory", system.memoryPercent], ["disk", system.diskPercent]]) {
    $(`[data-metric="${key}"]`).dataset.level = system.supported ? level(value) : "";
    if (!state.sparks.size || !system.supported) setText($(`#metric-${key}`), system.supported ? `${value.toFixed(0)}%` : "—");
  }
  setText(
    $("#system-line"),
    system.supported
      ? `Память ${formatBytes(system.memoryUsedBytes)} из ${formatBytes(system.memoryTotalBytes)} · диск ${formatBytes(system.diskUsedBytes)} из ${formatBytes(system.diskTotalBytes)} · нагрузка ${system.load1.toFixed(2)} на ${system.cores} ядр. · сервер работает ${formatDuration(system.uptimeSeconds)}`
      : "Ресурсы сервера доступны только на Linux.",
  );
  setText($("#who"), `${data.user.name}${data.panel?.version ? ` · hrk-console ${data.panel.version}` : ""}`);
}

async function refreshOverview() {
  try {
    renderOverview(await api("/api/tg/overview"));
    state.failures = 0;
    delete $("#live").dataset.offline;
    setText($("#live-text"), "Live");
  } catch (error) {
    if (++state.failures === 2) toast(`Нет связи: ${error.message}`, "error");
    $("#live").dataset.offline = "";
    setText($("#live-text"), "Нет связи");
  }
}

// Спарклайны берут живую историю; цифры в карточках идут вместе с ними.
async function refreshHistory() {
  try {
    const history = await api("/api/tg/system/history?range=live");
    document.querySelectorAll("[data-spark]").forEach((canvas) => {
      const key = canvas.dataset.spark;
      if (!state.sparks.has(key)) {
        const color = { cpu: "--chart-1", memory: "--chart-2", disk: "--chart-3" }[key];
        state.sparks.set(key, createSparkline(canvas, color, { onValue: (value) => setText($(`#metric-${key}`), `${value.toFixed(0)}%`) }));
      }
      state.sparks.get(key).setData(history.points || [], key);
    });
  } catch {
    /* Графики вторичны: связь покажет обзор. */
  }
}

async function refreshIncidents() {
  try {
    const { incidents = [] } = await api("/api/tg/incidents");
    const list = $("#incidents");
    list.replaceChildren();
    $("#incidents-empty").hidden = incidents.length > 0;
    for (const item of incidents.slice(0, 4)) {
      const row = document.createElement("button");
      row.type = "button";
      row.className = "incident";
      row.dataset.level = item.level;
      const icon = document.createElement("span");
      icon.className = "row-icon";
      icon.textContent = "!";
      const main = document.createElement("span");
      main.className = "row-main";
      const title = document.createElement("strong");
      title.textContent = item.title;
      const detail = document.createElement("small");
      detail.textContent = `${item.module} · ${item.end || item.start}`;
      main.append(title, detail);
      const count = document.createElement("span");
      count.className = "count";
      count.textContent = item.count > 1 ? `×${item.count}` : "";
      row.append(icon, main, count);
      row.addEventListener("click", () => {
        state.logLevel = item.level === "WARNING" ? "WARNING" : "ERROR";
        syncSegmented("[data-logs-level]", "logsLevel", state.logLevel);
        switchTab("logs");
      });
      list.append(row);
    }
  } catch {
    /* Не критично. */
  }
}

async function runAction(action) {
  const questions = {
    restart: "Перезапустить бота? Он будет недоступен несколько секунд.",
    stop: state.overview?.watchdog?.enabled
      ? "Остановить бота? Автовосстановление включено и через время запустит его снова."
      : "Остановить бота?",
  };
  haptic.tap();
  if (state.confirm && questions[action]) {
    const confirmed = await new Promise((resolve) =>
      tg?.showConfirm ? tg.showConfirm(questions[action], resolve) : resolve(window.confirm(questions[action])),
    );
    if (!confirmed) return;
  }
  state.busy = action;
  if (state.overview) renderOverview(state.overview);
  try {
    const result = await api(`/api/tg/bot/${action}`, { method: "POST" });
    haptic.done(true);
    toast(result.message[0].toUpperCase() + result.message.slice(1));
  } catch (error) {
    haptic.done(false);
    toast(error.message, "error");
  } finally {
    state.busy = "";
    await refreshOverview();
  }
}

/* ---------- Модули ---------- */

const moduleLabels = { ready: "Готов", loading: "Загружается", error: "Ошибка", suspended: "Приостановлен", unloaded: "Выгружен" };
const isProblem = (item) => item.state === "error" || item.state === "suspended";

function renderModules() {
  const snapshot = state.modules;
  if (!snapshot) return;
  const modules = snapshot.modules || [];
  setText($("#modules-message"), snapshot.message || "");
  const problems = modules.filter(isProblem).length;
  setText($('[data-tally="ready"] strong'), modules.filter((item) => item.state === "ready").length);
  setText($('[data-tally="problems"] strong'), problems);
  setText($('[data-tally="loading"] strong'), modules.filter((item) => item.state === "loading").length);
  const badge = $("#modules-badge");
  badge.hidden = problems === 0;
  setText(badge, problems);

  const query = state.moduleQuery.trim().toLowerCase();
  const visible = modules
    .filter((item) => state.moduleFilter !== "problems" || isProblem(item))
    .filter((item) => state.moduleFilter !== "installed" || item.kind !== "core")
    .filter((item) => !query || item.name.toLowerCase().includes(query))
    .sort((a, b) => Number(isProblem(b)) - Number(isProblem(a)) || a.name.localeCompare(b.name, "ru"));
  $("#modules-empty").hidden = visible.length > 0 || !modules.length;
  const list = $("#modules");
  list.hidden = !visible.length;
  list.replaceChildren(
    ...visible.map((item) => {
      const card = document.createElement("div");
      card.className = "module";
      card.dataset.state = item.state;
      const opened = state.open.has(item.id);
      if (opened) card.dataset.open = "";
      const head = document.createElement("button");
      head.type = "button";
      const icon = document.createElement("span");
      icon.className = "row-icon";
      icon.textContent = (item.name[0] || "?").toUpperCase();
      const main = document.createElement("span");
      main.className = "row-main";
      const name = document.createElement("strong");
      name.textContent = item.name;
      const detail = document.createElement("small");
      detail.textContent = `${item.kind === "core" ? "Встроенный" : "Установленный"}${item.version ? ` · v${item.version}` : ""}`;
      main.append(name, detail);
      const pill = document.createElement("span");
      pill.className = "pill";
      pill.textContent = moduleLabels[item.state] || "Неизвестно";
      head.append(icon, main, pill);
      card.append(head);
      if (item.error) {
        const chevron = document.createElement("span");
        chevron.className = "chevron";
        chevron.textContent = "›";
        head.append(chevron);
        head.addEventListener("click", () => {
          haptic.select();
          if (state.open.has(item.id)) state.open.delete(item.id);
          else state.open.add(item.id);
          renderModules();
        });
        if (opened) {
          const error = document.createElement("pre");
          error.className = "module-error";
          error.textContent = item.error;
          card.append(error);
        }
      }
      return card;
    }),
  );
}

async function refreshModules() {
  try {
    state.modules = await api("/api/tg/modules");
    renderModules();
  } catch {
    /* Покажет связь на главной. */
  }
}

/* ---------- Журнал ---------- */

const logLevel = (line) => line.match(/\[([A-Z]+)\]/)?.[1] || "OTHER";

function renderLogs() {
  const log = $("#log");
  const pinned = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
  const lines = state.logLines.filter((line) => {
    if (state.logLevel === "all") return true;
    const value = logLevel(line);
    return state.logLevel === "ERROR" ? value === "ERROR" || value === "CRITICAL" : value === state.logLevel;
  });
  log.replaceChildren(
    ...lines.map((line) => {
      const row = document.createElement("p");
      row.dataset.level = logLevel(line);
      const match = line.match(/^\d{4}-\d{2}-\d{2} (\d{2}:\d{2}:\d{2}) (.*)$/s);
      if (match) {
        const time = document.createElement("time");
        time.textContent = match[1];
        row.append(time, ` ${match[2]}`);
      } else row.textContent = line;
      return row;
    }),
  );
  if (!lines.length) {
    const empty = document.createElement("p");
    empty.textContent = state.logLevel === "all" ? "Журнал пуст." : "Таких строк в последних 200 нет.";
    log.append(empty);
  }
  if (pinned) log.scrollTop = log.scrollHeight;
}

async function refreshLogs() {
  try {
    const { lines = [] } = await api("/api/tg/logs?limit=200");
    if (lines.length === state.logLines.length && lines.at(-1) === state.logLines.at(-1)) return;
    state.logLines = lines;
    renderLogs();
  } catch (error) {
    setText($("#logs-message"), `Журнал недоступен: ${error.message}`);
  }
}

/* ---------- Навигация и опрос ---------- */

function syncSegmented(selector, key, value) {
  document.querySelectorAll(selector).forEach((button) => button.setAttribute("aria-selected", String(button.dataset[key] === value)));
}

function stopPolling() {
  state.timers.forEach(clearInterval);
  state.timers = [];
}

// Опрашиваем только то, что видно: так приложение не тратит батарею и трафик зря.
function startPolling() {
  stopPolling();
  const every = (ms, fn) => {
    fn();
    state.timers.push(setInterval(() => !document.hidden && fn(), ms));
  };
  every(2000, refreshOverview);
  if (state.tab === "home") {
    every(1000, refreshHistory);
    every(15000, refreshIncidents);
  }
  if (state.tab === "modules") every(3000, refreshModules);
  else every(15000, refreshModules); // для значка с числом проблем
  if (state.tab === "logs") every(3000, refreshLogs);
}

function switchTab(tab) {
  if (state.tab !== tab) haptic.select();
  state.tab = tab;
  document.querySelectorAll("[data-screen]").forEach((screen) => (screen.hidden = screen.dataset.screen !== tab));
  document.querySelectorAll("[data-tab]").forEach((button) => {
    if (button.dataset.tab === tab) button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  });
  window.scrollTo({ top: 0 });
  if (tab === "modules") renderModules();
  if (tab === "logs") {
    renderLogs();
    const log = $("#log");
    log.scrollTop = log.scrollHeight;
  }
  startPolling();
}

function bind() {
  document.querySelectorAll("[data-tab]").forEach((button) => button.addEventListener("click", () => switchTab(button.dataset.tab)));
  document.querySelectorAll("[data-action]").forEach((button) => button.addEventListener("click", () => runAction(button.dataset.action)));
  $("#watchdog").addEventListener("click", () => {
    haptic.select();
    toast("Автовосстановление настраивается в админке панели.");
  });
  document.querySelectorAll("[data-modules-filter]").forEach((button) =>
    button.addEventListener("click", () => {
      haptic.select();
      state.moduleFilter = button.dataset.modulesFilter;
      syncSegmented("[data-modules-filter]", "modulesFilter", state.moduleFilter);
      renderModules();
    }),
  );
  $("#modules-search").addEventListener("input", (event) => {
    state.moduleQuery = event.target.value;
    renderModules();
  });
  document.querySelectorAll("[data-logs-level]").forEach((button) =>
    button.addEventListener("click", () => {
      haptic.select();
      state.logLevel = button.dataset.logsLevel;
      syncSegmented("[data-logs-level]", "logsLevel", state.logLevel);
      renderLogs();
      $("#log").scrollTop = $("#log").scrollHeight;
    }),
  );
  const log = $("#log");
  log.addEventListener("scroll", () => {
    $("#log-jump").hidden = log.scrollHeight - log.scrollTop - log.clientHeight < 80;
  });
  $("#log-jump").addEventListener("click", () => log.scrollTo({ top: log.scrollHeight, behavior: "smooth" }));
  $("#open-panel").addEventListener("click", () => {
    const url = `${location.origin}/`;
    if (tg?.openLink) tg.openLink(url);
    else window.open(url, "_blank", "noopener");
  });
  $("#gate-retry").addEventListener("click", start);
  document.addEventListener("visibilitychange", () => !document.hidden && state.token && refreshOverview());
}

function applyTelegramChrome() {
  if (!tg) return;
  tg.ready();
  tg.expand();
  // Журнал прокручивается вертикально — свайп не должен сворачивать приложение.
  tg.disableVerticalSwipes?.();
  const scheme = () => (document.documentElement.dataset.scheme = tg.colorScheme === "light" ? "light" : "dark");
  scheme();
  tg.onEvent?.("themeChanged", scheme);
  try {
    tg.setHeaderColor?.("secondary_bg_color");
    tg.setBackgroundColor?.("secondary_bg_color");
  } catch {
    /* Старые клиенты не умеют менять цвета. */
  }
}

async function start() {
  gate("loading", "Подключаемся…", "Проверяем доступ через Telegram.");
  if (!(await login())) return;
  $("#gate").hidden = true;
  $("#app").hidden = false;
  $("#tabbar").hidden = false;
  switchTab(state.tab);
}

applyTelegramChrome();
bind();
start();
