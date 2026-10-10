// Мини-приложение Telegram: вход по подписанным данным запуска, три экрана, шторки и живые данные.
// Данные бота и журнала выводятся только через textContent; разметка иконок — константы.
import { createSparkline } from "/live-chart.js";

const tg = window.Telegram?.WebApp;
const $ = (selector) => document.querySelector(selector);
const reduced = () => matchMedia("(prefers-reduced-motion: reduce)").matches;
const TABS = ["home", "modules", "logs"];

const state = {
  token: "",
  tab: "home",
  ready: false,
  overview: null,
  confirm: true,
  busy: "",
  history: [],
  modules: null,
  moduleFilter: "all",
  moduleQuery: "",
  logLevel: "all",
  logQuery: "",
  logLines: [],
  rendered: [],
  unread: 0,
  timers: [],
  sparks: new Map(),
  failures: 0,
  sheet: null,
  uptime: null,
};

const haptic = {
  select: () => tg?.HapticFeedback?.selectionChanged(),
  light: () => tg?.HapticFeedback?.impactOccurred("light"),
  tap: () => tg?.HapticFeedback?.impactOccurred("medium"),
  done: (ok) => tg?.HapticFeedback?.notificationOccurred(ok ? "success" : "error"),
};

const icons = {
  restart: '<svg viewBox="0 0 24 24"><path d="M20 12a8 8 0 1 1-2.4-5.7L20 8.6"/><path d="M20 3.5v5.1h-5.1"/></svg>',
  stop: '<svg viewBox="0 0 24 24"><rect x="7" y="7" width="10" height="10" rx="2.2" fill="currentColor" stroke="none"/></svg>',
  cpu: '<svg viewBox="0 0 24 24"><rect x="6" y="6" width="12" height="12" rx="2"/><path d="M9 2v4M15 2v4M9 18v4M15 18v4M2 9h4M2 15h4M18 9h4M18 15h4"/></svg>',
  memory: '<svg viewBox="0 0 24 24"><rect x="3" y="7" width="18" height="10" rx="2"/><path d="M7 7v10M11 7v10M15 7v10"/></svg>',
  disk: '<svg viewBox="0 0 24 24"><ellipse cx="12" cy="6" rx="8" ry="3"/><path d="M4 6v6c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12v6c0 1.7 3.6 3 8 3s8-1.3 8-3v-6"/></svg>',
  alert: '<svg viewBox="0 0 24 24"><path d="M12 4 3 20h18z"/><path d="M12 10v4M12 17v.5"/></svg>',
};

/* ---------- Помощники ---------- */

// Создаёт узел из свойств и детей; текст всегда идёт через textContent.
function h(tag, attrs = {}, ...kids) {
  const element = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (key === "class") element.className = value;
    else if (key === "text") element.textContent = value;
    else if (key === "html") element.innerHTML = value;
    else if (key.startsWith("on")) element.addEventListener(key.slice(2), value);
    else if (value !== false && value != null) element.setAttribute(key, value === true ? "" : value);
  }
  element.append(...kids.filter((kid) => kid != null));
  return element;
}

const setText = (element, text) => {
  if (element && element.textContent !== String(text)) element.textContent = text;
};

// Смена текста с короткой анимацией: используется для заголовков состояний.
function swapText(element, text) {
  if (element.textContent === String(text)) return;
  const first = !element.textContent || element.textContent === "—";
  element.textContent = text;
  if (first || reduced()) return;
  element.classList.remove("swap");
  void element.offsetWidth;
  element.classList.add("swap");
}

// Число плавно «доезжает» до нового значения.
function tween(element, to, format, ms = 650) {
  const from = element._value;
  element._value = to;
  if (!Number.isFinite(from) || reduced() || from === to) {
    element.textContent = format(to);
    return;
  }
  const started = performance.now();
  cancelAnimationFrame(element._frame);
  const step = (now) => {
    const t = Math.min(1, (now - started) / ms);
    const eased = 1 - (1 - t) ** 3;
    element.textContent = format(from + (to - from) * eased);
    if (t < 1) element._frame = requestAnimationFrame(step);
  };
  element._frame = requestAnimationFrame(step);
}

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
const months = ["янв", "фев", "мар", "апр", "мая", "июн", "июл", "авг", "сен", "окт", "ноя", "дек"];
// Время приходит строкой сервера без часового пояса: показываем его как есть, без пересчёта.
const formatWhen = (value) => {
  const match = /^(\d{4})-(\d{2})-(\d{2}) (\d{2}:\d{2})/.exec(value || "");
  return match ? `${Number(match[3])} ${months[Number(match[2]) - 1]}, ${match[4]}` : value || "—";
};
const level = (value) => (value >= 90 ? "bad" : value >= 75 ? "warn" : "");
const hue = (name) => {
  let hash = 0;
  for (const char of name || "") hash = (hash * 31 + char.codePointAt(0)) >>> 0;
  return hash % 360;
};
const initials = (name) => {
  const words = (name || "?").replace(/([a-zа-я])([A-ZА-Я])/g, "$1 $2").split(/[\s_.-]+/).filter(Boolean);
  return (words.length > 1 ? words[0][0] + words[1][0] : (name || "?").slice(0, 2)).toLocaleUpperCase();
};
const avatar = (name) => {
  const element = h("span", { class: "avatar", "aria-hidden": "true", text: initials(name) });
  element.style.setProperty("--hue", hue(name));
  return element;
};

// Данные запуска: от моста, а если он не загрузился — из адреса, куда их кладёт клиент Telegram.
function initData() {
  if (tg?.initData) return tg.initData;
  return new URLSearchParams(location.hash.slice(1)).get("tgWebAppData") || "";
}

/* ---------- Вход, запросы, шторка ошибок ---------- */

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
  for (const id of ["#app", "#tabbar", "#bar"]) $(id).hidden = true;
}

let toastTimer;
function toast(message, kind = "ok") {
  const element = $("#toast");
  clearTimeout(toastTimer);
  element.hidden = false;
  element.dataset.kind = kind;
  element.removeAttribute("data-leaving");
  element.textContent = message;
  element.style.animation = "none";
  void element.offsetWidth;
  element.style.animation = "";
  toastTimer = setTimeout(() => {
    element.dataset.leaving = "";
    setTimeout(() => (element.hidden = true), 280);
  }, 3000);
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
    gate("error", "Панель не отвечает", "Проверьте, что сервер работает и устройство подключено к сети.", { retry: true });
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

/* ---------- Шторка ---------- */

const backHandler = () => closeSheet();

// Открывает шторку; fill наполняет тело и может вернуть { onClose }.
function openSheet(fill) {
  const layer = $("#sheet-layer");
  const body = $("#sheet-body");
  const sheet = $("#sheet");
  if (state.sheet) closeSheet(undefined);
  body.replaceChildren();
  state.sheet = fill(body) || {};
  [...body.children].forEach((child, index) => child.style.setProperty("--stagger", index));
  layer.hidden = false;
  sheet.scrollTop = 0;
  sheet.style.removeProperty("--drag");
  sheet.removeAttribute("data-dragging");
  requestAnimationFrame(() => requestAnimationFrame(() => (layer.dataset.open = "")));
  tg?.BackButton?.show();
  tg?.BackButton?.onClick(backHandler);
  haptic.light();
}

function closeSheet(result) {
  if (!state.sheet) return;
  const api = state.sheet;
  state.sheet = null;
  const layer = $("#sheet-layer");
  layer.removeAttribute("data-open");
  tg?.BackButton?.offClick(backHandler);
  tg?.BackButton?.hide();
  setTimeout(() => {
    if (!state.sheet) layer.hidden = true;
  }, reduced() ? 20 : 480);
  api.onClose?.(result);
}

function bindSheet() {
  const sheet = $("#sheet");
  const grip = $("#sheet-grip");
  $("#sheet-backdrop").addEventListener("click", () => closeSheet());
  let start = null;
  grip.addEventListener("pointerdown", (event) => {
    start = { y: event.clientY, at: performance.now(), dy: 0 };
    grip.setPointerCapture(event.pointerId);
    sheet.dataset.dragging = "";
  });
  grip.addEventListener("pointermove", (event) => {
    if (!start) return;
    start.dy = Math.max(0, event.clientY - start.y);
    sheet.style.setProperty("--drag", `${start.dy}px`);
  });
  const release = () => {
    if (!start) return;
    const speed = start.dy / Math.max(1, performance.now() - start.at);
    const close = start.dy > 110 || (speed > 0.55 && start.dy > 24);
    start = null;
    sheet.removeAttribute("data-dragging");
    if (close) closeSheet();
    else sheet.style.setProperty("--drag", "0px");
  };
  grip.addEventListener("pointerup", release);
  grip.addEventListener("pointercancel", release);
  document.addEventListener("keydown", (event) => event.key === "Escape" && closeSheet());
}

// Подтверждение опасного действия: возвращает true, если нажали основную кнопку.
function confirmSheet(action) {
  const texts = {
    restart: ["Перезапустить бота?", "Бот будет недоступен несколько секунд, затем запустится снова.", "Перезапустить", "primary", "restart", ""],
    stop: [
      "Остановить бота?",
      state.overview?.watchdog?.enabled
        ? "Автовосстановление включено и через время запустит бота снова. Чтобы он не поднимался, выключите его в админке."
        : "Бот останется остановленным, пока вы не запустите его снова.",
      "Остановить",
      "danger",
      "stop",
      "bad",
    ],
  };
  const [title, text, accept, tone, icon, iconTone] = texts[action];
  return new Promise((resolve) => {
    openSheet((body) => {
      body.append(
        h("div", { class: "sheet-head" }, h("span", { class: `sheet-icon ${iconTone}`, html: icons[icon] }), h("div", {}, h("h3", { id: "sheet-title", text: title }), h("small", { text: state.overview?.host || "" }))),
        h("p", { class: "sheet-text", text }),
        h(
          "div",
          { class: "sheet-buttons" },
          h("button", { class: "sbtn", type: "button", text: "Отмена", onclick: () => closeSheet(false) }),
          h("button", { class: `sbtn ${tone}`, type: "button", text: accept, onclick: () => closeSheet(true) }),
        ),
      );
      return { onClose: (result) => resolve(result === true) };
    });
  });
}

/* ---------- Главная ---------- */

// Время работы приходит строкой («34м 05с» или «1ч 12м»); секунды досчитываем на месте между опросами.
function parseUptime(text) {
  if (!text || text === "—") return null;
  const part = (suffix) => Number(new RegExp(`(\\d+)${suffix}`).exec(text)?.[1] || 0);
  const seconds = part("ч") * 3600 + part("м") * 60 + part("с");
  return { seconds, ticking: !text.includes("ч"), at: performance.now() };
}

function uptimeText() {
  const u = state.uptime;
  if (!u) return "";
  if (!u.ticking) return state.overview?.bot?.uptime || "";
  const total = u.seconds + Math.floor((performance.now() - u.at) / 1000);
  const minutes = Math.floor(total / 60);
  const seconds = String(total % 60).padStart(2, "0");
  return minutes ? `${minutes}м ${seconds}с` : `${total}с`;
}

function renderSubtitle() {
  const bot = state.overview?.bot;
  if (!bot) return;
  setText($("#bot-subtitle"), bot.running ? `${uptimeText() || bot.uptime} · PID ${bot.pid}` : "Процесс Heroku не запущен");
}

function renderOverview(data) {
  const previous = state.overview;
  state.overview = data;
  state.confirm = data.confirm;
  const bot = data.bot;
  if (bot.running && (!previous?.bot?.running || previous.bot.uptime !== bot.uptime)) state.uptime = parseUptime(bot.uptime);
  if (!bot.running) state.uptime = null;

  const hero = $("#hero");
  hero.dataset.state = state.busy ? "busy" : bot.running ? "running" : "stopped";
  swapText($("#bot-title"), state.busy ? { start: "Запускаем…", stop: "Останавливаем…", restart: "Перезапускаем…" }[state.busy] : bot.running ? "Работает" : "Остановлен");
  renderSubtitle();
  if (bot.running && bot.cpuPercent != null) tween($("#bot-cpu"), bot.cpuPercent, (v) => `${v.toFixed(1)}%`);
  else setText($("#bot-cpu"), "—");
  if (bot.running && bot.rssBytes) tween($("#bot-rss"), bot.rssBytes, formatBytes);
  else setText($("#bot-rss"), "—");
  setText($("#bot-version"), bot.version || "—");
  document.querySelectorAll("[data-action]").forEach((button) => {
    const action = button.dataset.action;
    button.disabled = Boolean(state.busy) || (action === "start" ? bot.running : !bot.running);
    if (state.busy === action) button.dataset.busy = "";
    else delete button.dataset.busy;
  });
  // Главная кнопка — та, что сейчас нужнее.
  $('[data-action="start"]').classList.toggle("lead", !bot.running);
  $('[data-action="restart"]').classList.toggle("lead", bot.running);

  const watchdog = data.watchdog || {};
  const suspended = watchdog.state === "suspended";
  const recovering = ["waiting", "recovering"].includes(watchdog.state);
  $("#watchdog").dataset.state = !watchdog.enabled ? "off" : suspended ? "bad" : recovering ? "warn" : "on";
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
  setText($("#bar-sub"), `${data.host || "сервер"}${data.panel?.version ? ` · ${data.panel.version}` : ""}`);
}

async function refreshOverview() {
  try {
    renderOverview(await api("/api/tg/overview"));
    state.failures = 0;
    $("#live").removeAttribute("data-offline");
    setText($("#live-text"), "Live");
  } catch (error) {
    if (++state.failures === 2) toast(`Нет связи: ${error.message}`, "error");
    $("#live").dataset.offline = "";
    setText($("#live-text"), "Нет связи");
  } finally {
    markReady();
  }
}

// Спарклайны берут живую историю; цифры в карточках идут вместе с ними.
async function refreshHistory() {
  try {
    const history = await api("/api/tg/system/history?range=live");
    state.history = history.points || [];
    document.querySelectorAll("[data-spark]").forEach((canvas) => {
      const key = canvas.dataset.spark;
      if (!state.sparks.has(key)) {
        const color = { cpu: "--chart-1", memory: "--chart-2", disk: "--chart-3" }[key];
        state.sparks.set(key, createSparkline(canvas, color, { onValue: (value) => setText($(`#metric-${key}`), `${value.toFixed(0)}%`) }));
      }
      state.sparks.get(key).setData(state.history, key);
    });
    state.sheet?.onHistory?.();
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
    incidents.slice(0, 4).forEach((item, index) => {
      const row = h(
        "button",
        { type: "button", class: "row incident", "data-level": item.level },
        h("span", { class: "row-icon", text: "!" }),
        h("span", { class: "row-main" }, h("strong", { text: item.title }), h("small", { text: `${item.module} · ${formatWhen(item.end || item.start)}` })),
        item.count > 1 ? h("span", { class: "count", text: `×${item.count}` }) : null,
      );
      row.style.animation = `rise 420ms var(--ease) ${index * 50}ms both`;
      row.addEventListener("click", () => openIncident(item));
      list.append(row);
    });
  } catch {
    /* Не критично. */
  }
}

function openIncident(item) {
  const warning = item.level === "WARNING";
  openSheet((body) => {
    body.append(
      h("div", { class: "sheet-head" }, h("span", { class: `sheet-icon ${warning ? "warn" : "bad"}`, html: icons.alert }), h("div", {}, h("h3", { id: "sheet-title", text: item.title }), h("small", { text: `${item.level} · ${item.module}` }))),
      h(
        "dl",
        { class: "facts" },
        fact("Когда", formatWhen(item.start)),
        fact("Событий", String(item.count)),
        item.restarts ? fact("Перезапусков", String(item.restarts)) : null,
      ),
      item.context ? h("pre", { class: "code", text: `Перед ошибкой:\n${item.context}` }) : null,
      h(
        "div",
        { class: "sheet-buttons single" },
        h("button", { class: "sbtn primary", type: "button", text: "Показать в журнале", onclick: () => { closeSheet(); showInLog(item.module, warning ? "WARNING" : "ERROR"); } }),
      ),
    );
  });
}

const fact = (label, value) => h("div", { class: "fact" }, h("dt", { text: label }), h("dd", { text: value }));

// Шторка метрики: большое число, 5-минутный график и сводка по замерам.
function openMetric(key) {
  const meta = {
    cpu: { title: "CPU", icon: "cpu", color: "--chart-1", tone: "" },
    memory: { title: "Память", icon: "memory", color: "--chart-2", tone: "ok" },
    disk: { title: "Диск", icon: "disk", color: "--chart-3", tone: "warn" },
  }[key];
  openSheet((body) => {
    const value = h("div", { class: "bigvalue", text: "—" });
    const canvas = h("canvas", { class: "bigchart", "aria-label": `График: ${meta.title}, 5 минут` });
    const facts = h("dl", { class: "facts" });
    const system = state.overview?.system;
    const extra = system?.supported
      ? key === "memory"
        ? `${formatBytes(system.memoryUsedBytes)} из ${formatBytes(system.memoryTotalBytes)}`
        : key === "disk"
          ? `${formatBytes(system.diskUsedBytes)} из ${formatBytes(system.diskTotalBytes)}`
          : `нагрузка ${system.load1.toFixed(2)} · ${system.cores} ядр.`
      : "";
    const fill = () => {
      const values = state.history.map((point) => Number(point[key]) || 0);
      facts.replaceChildren();
      if (!values.length) return;
      const avg = values.reduce((a, b) => a + b, 0) / values.length;
      facts.append(fact("Минимум", `${Math.min(...values).toFixed(0)}%`), fact("Среднее", `${avg.toFixed(0)}%`), fact("Максимум", `${Math.max(...values).toFixed(0)}%`));
    };
    const spark = createSparkline(canvas, meta.color, { windowMs: 300000, onValue: (v) => setText(value, `${v.toFixed(0)}%`) });
    body.append(
      h("div", { class: "sheet-head" }, h("span", { class: `sheet-icon ${meta.tone}`, html: icons[meta.icon] }), h("div", {}, h("h3", { id: "sheet-title", text: meta.title }), h("small", { text: extra || "последние 5 минут" }))),
      value,
      canvas,
      facts,
    );
    fill();
    requestAnimationFrame(() => spark.setData(state.history, key));
    return {
      onHistory: () => {
        spark.setData(state.history, key);
        fill();
      },
    };
  });
}

async function runAction(action) {
  haptic.tap();
  if (state.confirm && action !== "start" && !(await confirmSheet(action))) return;
  state.busy = action;
  if (state.overview) renderOverview(state.overview);
  try {
    const result = await api(`/api/tg/bot/${action}`, { method: "POST" });
    haptic.done(true);
    toast(result.message[0].toUpperCase() + result.message.slice(1));
    const hero = $("#hero");
    hero.dataset.done = "";
    setTimeout(() => hero.removeAttribute("data-done"), 1500);
  } catch (error) {
    haptic.done(false);
    toast(error.message, "error");
  } finally {
    state.busy = "";
    await refreshOverview();
  }
}

/* ---------- Модули ---------- */

const moduleLabels = { ready: "Работает", loading: "Загружается", error: "Ошибка", suspended: "Приостановлен", unloaded: "Выгружен" };
const isProblem = (item) => item.state === "error" || item.state === "suspended";
const category = (item) => (isProblem(item) ? "problems" : ["ready", "loading", "unloaded"].includes(item.state) ? item.state : "unknown");
const modEls = new Map();

function makeMod(item) {
  const element = h(
    "button",
    { type: "button", class: "mod", "data-id": item.id },
    avatar(item.name),
    h("span", { class: "row-main" }, h("strong"), h("small")),
    h("span", { class: "pill" }),
    h("span", { class: "chevron", "aria-hidden": "true", text: "›" }),
  );
  element.addEventListener("click", () => openModule(element.dataset.id));
  return element;
}

function updateMod(element, item) {
  const signature = JSON.stringify(item);
  if (element.dataset.signature === signature) return;
  const before = element.dataset.state;
  element.dataset.signature = signature;
  element.dataset.state = item.state;
  element.dataset.category = category(item);
  setText(element.querySelector("strong"), item.name);
  setText(element.querySelector("small"), `${item.kind === "core" ? "Встроенный" : "Установленный"}${item.version ? ` · v${item.version}` : ""}`);
  const pill = element.querySelector(".pill");
  setText(pill, moduleLabels[item.state] || "Неизвестно");
  if (before && before !== item.state) {
    element.classList.remove("flash");
    void element.offsetWidth;
    element.classList.add("flash");
    haptic.select();
  }
}

function renderModules() {
  const snapshot = state.modules;
  if (!snapshot) return;
  const modules = snapshot.modules || [];
  setText($("#modules-message"), snapshot.message || "");
  const problems = modules.filter(isProblem).length;
  tween($('[data-tally="ready"] strong'), modules.filter((item) => item.state === "ready").length, (v) => String(Math.round(v)), 500);
  tween($('[data-tally="problems"] strong'), problems, (v) => String(Math.round(v)), 500);
  tween($('[data-tally="loading"] strong'), modules.filter((item) => item.state === "loading").length, (v) => String(Math.round(v)), 500);
  const badge = $("#modules-badge");
  badge.hidden = problems === 0;
  setText(badge, problems);

  const query = state.moduleQuery.trim().toLowerCase();
  const rank = (item) => (isProblem(item) ? 0 : item.state === "loading" ? 1 : item.state === "ready" ? 2 : 3);
  const visible = modules
    .filter((item) => state.moduleFilter !== "problems" || isProblem(item))
    .filter((item) => state.moduleFilter !== "installed" || item.kind !== "core")
    .filter((item) => !query || item.name.toLowerCase().includes(query))
    .sort((a, b) => rank(a) - rank(b) || a.name.localeCompare(b.name, "ru"));
  const list = $("#modules");
  const keep = new Set(visible.map((item) => item.id));
  for (const [id, element] of modEls) {
    if (!modules.some((item) => item.id === id)) {
      element.remove();
      modEls.delete(id);
    } else if (!keep.has(id)) element.remove();
  }
  visible.forEach((item, index) => {
    let element = modEls.get(item.id);
    if (!element) {
      element = makeMod(item);
      modEls.set(item.id, element);
    }
    updateMod(element, item);
    if (list.children[index] !== element) list.insertBefore(element, list.children[index] || null);
  });
  $("#modules-empty").hidden = visible.length > 0 || !modules.length;
  list.hidden = !visible.length;
  if (state.sheet?.moduleId) state.sheet.refresh?.();
}

function openModule(id) {
  const find = () => state.modules?.modules?.find((item) => item.id === id);
  if (!find()) return;
  openSheet((body) => {
    let last = "";
    const render = () => {
      const item = find();
      if (!item) return closeSheet();
      // Опрос идёт каждые 3 секунды: перерисовываем, только если модуль изменился.
      const signature = JSON.stringify(item);
      if (signature === last) return;
      last = signature;
      body.replaceChildren(
        h(
          "div",
          { class: "sheet-head" },
          h("span", { class: "sheet-icon" }, avatar(item.name)),
          h("div", {}, h("h3", { id: "sheet-title", text: item.name }), h("small", { text: moduleLabels[item.state] || "Неизвестно" })),
        ),
        h("p", { class: "sheet-text", text: { ready: "Модуль загрузился, прошёл инициализацию и доступен боту.", loading: "Импорт или инициализация ещё идут.", error: "Модуль не удалось загрузить или инициализировать.", suspended: "Модуль сам приостановил свою инициализацию.", unloaded: "Модуль выгружен в этом запуске." }[item.state] || "Загрузчик передал неизвестное состояние." }),
        h("dl", { class: "facts" }, fact("Тип", item.kind === "core" ? "Встроенный" : "Установленный"), fact("Версия", item.version ? `v${item.version}` : "—")),
        item.error ? h("pre", { class: "code bad", text: item.error }) : null,
        h(
          "div",
          { class: `sheet-buttons${item.error ? "" : " single"}` },
          item.error ? h("button", { class: "sbtn", type: "button", text: "Скопировать", onclick: (event) => copy(item.error, event.currentTarget) }) : null,
          h("button", { class: "sbtn primary", type: "button", text: item.error ? "Найти в журнале" : "Открыть журнал", onclick: () => { closeSheet(); showInLog(item.name, item.state === "error" ? "ERROR" : "all"); } }),
        ),
      );
      [...body.children].forEach((child, index) => child.style.setProperty("--stagger", index));
    };
    render();
    return { moduleId: id, refresh: render };
  });
}

async function copy(text, button) {
  try {
    await navigator.clipboard.writeText(text);
    const original = button.textContent;
    button.textContent = "Скопировано ✓";
    haptic.done(true);
    setTimeout(() => (button.textContent = original), 1500);
  } catch {
    toast("Не удалось скопировать", "error");
  }
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

function visibleLines() {
  const query = state.logQuery.toLowerCase();
  return state.logLines.filter((line) => {
    if (query && !line.toLowerCase().includes(query)) return false;
    if (state.logLevel === "all") return true;
    const value = logLevel(line);
    return state.logLevel === "ERROR" ? value === "ERROR" || value === "CRITICAL" : value === state.logLevel;
  });
}

function lineElement(line, fresh) {
  const row = h("p", { "data-level": logLevel(line), class: fresh ? "fresh" : "" });
  const match = line.match(/^\d{4}-\d{2}-\d{2} (\d{2}:\d{2}:\d{2}) (.*)$/s);
  if (match) row.append(h("time", { text: match[1] }), ` ${match[2]}`);
  else row.textContent = line;
  return row;
}

function logBottom(smooth = false) {
  const log = $("#log");
  log.style.scrollBehavior = smooth ? "smooth" : "auto";
  log.scrollTop = log.scrollHeight;
  if (!smooth) requestAnimationFrame(() => (log.style.scrollBehavior = ""));
}

// Новые строки дописываются, а не перерисовывают весь журнал: прокрутка и анимация не сбиваются.
function renderLogs(forceFull = false) {
  const log = $("#log");
  const lines = visibleLines();
  const pinned = log.scrollHeight - log.scrollTop - log.clientHeight < 40;
  const previous = state.rendered;
  let appended = 0;
  let overlap = -1;
  if (!forceFull && previous.length) {
    const tail = previous.slice(-3);
    for (let i = lines.length - 1; i >= tail.length - 1; i--) {
      if (tail.every((line, k) => lines[i - (tail.length - 1) + k] === line)) {
        overlap = i;
        break;
      }
    }
  }
  if (overlap >= 0) {
    const fresh = lines.slice(overlap + 1);
    const drop = previous.length + fresh.length - lines.length;
    for (let i = 0; i < drop; i++) log.firstElementChild?.remove();
    log.append(...fresh.map((line) => lineElement(line, true)));
    appended = fresh.length;
  } else {
    log.replaceChildren(
      ...(lines.length ? lines.map((line) => lineElement(line, false)) : [h("div", { class: "none", text: state.logQuery || state.logLevel !== "all" ? "Таких строк в последних 200 нет" : "Журнал пуст" })]),
    );
    if (pinned || forceFull) logBottom();
  }
  state.rendered = lines;
  if (overlap >= 0 && appended) {
    if (pinned) logBottom(true);
    else state.unread += appended;
  }
  updateJump();

  for (const name of ["ERROR", "WARNING"]) {
    const count = state.logLines.filter((line) => (name === "ERROR" ? ["ERROR", "CRITICAL"].includes(logLevel(line)) : logLevel(line) === name)).length;
    const badge = $(`[data-log-count="${name}"]`);
    const text = count ? String(count) : "";
    if (badge.textContent !== text) {
      const grew = Number(badge.textContent || 0) < count;
      badge.textContent = text;
      if (grew && !reduced()) {
        badge.removeAttribute("data-bump");
        void badge.offsetWidth;
        badge.dataset.bump = "";
      }
    }
  }
  const chip = $("#log-chip");
  chip.hidden = !state.logQuery;
  setText(chip.querySelector("span"), state.logQuery ? `модуль: ${state.logQuery}` : "");
}

function updateJump() {
  const log = $("#log");
  const away = log.scrollHeight - log.scrollTop - log.clientHeight > 80;
  $("#log-jump").hidden = !away;
  const unread = $("#log-unread");
  unread.hidden = !(state.unread > 0 && away);
  setText(unread, state.unread);
  if (!away) state.unread = 0;
}

async function refreshLogs() {
  try {
    const { lines = [] } = await api("/api/tg/logs?limit=200");
    if (lines.length === state.logLines.length && lines.at(-1) === state.logLines.at(-1) && state.rendered.length) return;
    state.logLines = lines;
    renderLogs();
  } catch (error) {
    setText($("#logs-message"), `Журнал недоступен: ${error.message}`);
  }
}

// Переход в журнал с фильтром: из карточки модуля или происшествия.
function showInLog(query, levelName) {
  state.logQuery = query || "";
  state.logLevel = levelName || "all";
  syncSegmented($('[aria-label="Уровень журнала"]'), "logsLevel", state.logLevel);
  switchTab("logs");
  renderLogs(true);
}

/* ---------- Навигация и опрос ---------- */

// Ставит бегунок сегментированного переключателя под выбранную кнопку.
function syncSegmented(container, key, value) {
  const buttons = [...container.querySelectorAll("button")];
  buttons.forEach((button) => button.setAttribute("aria-selected", String(button.dataset[key] === value)));
  const index = Math.max(0, buttons.findIndex((button) => button.dataset[key] === value));
  container.style.setProperty("--n", buttons.length);
  container.style.setProperty("--i", index);
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
  // Секунды времени работы идут между опросами; тик чаще секунды, чтобы цифра менялась ровно в срок.
  state.timers.push(setInterval(() => !document.hidden && state.tab === "home" && state.overview?.bot?.running && renderSubtitle(), 250));
}

// Экран въезжает, только когда его показывают после скрытия; элементы идут по очереди.
function showScreen(tab, direction) {
  document.querySelectorAll("[data-screen]").forEach((screen) => {
    const active = screen.dataset.screen === tab && state.ready;
    const wasHidden = screen.hidden;
    screen.hidden = !active;
    if (!active) return screen.removeAttribute("data-enter");
    if (!wasHidden) return;
    screen.dataset.enter = direction < 0 ? "back" : "fwd";
    [...screen.children].filter((child) => !child.hidden).forEach((child, index) => child.style.setProperty("--stagger", Math.min(index, 9)));
    setTimeout(() => screen.removeAttribute("data-enter"), 1100);
  });
}

function switchTab(tab) {
  const direction = TABS.indexOf(tab) - TABS.indexOf(state.tab);
  if (direction !== 0) haptic.select();
  state.tab = tab;
  document.querySelectorAll("[data-tab]").forEach((button) => {
    if (button.dataset.tab === tab) button.setAttribute("aria-current", "page");
    else button.removeAttribute("aria-current");
  });
  $(".tabbar-inner").style.setProperty("--i", TABS.indexOf(tab));
  showScreen(tab, direction);
  window.scrollTo({ top: 0 });
  if (tab === "modules") renderModules();
  if (tab === "logs") {
    renderLogs(true);
    requestAnimationFrame(() => logBottom());
  }
  startPolling();
}

// Первые данные пришли (или не пришли): скелетон растворяется, экран въезжает.
function markReady() {
  if (state.ready) return;
  state.ready = true;
  const skeleton = $("#skeleton");
  skeleton.dataset.leaving = "";
  setTimeout(() => (skeleton.hidden = true), reduced() ? 20 : 280);
  showScreen(state.tab, 1);
}

function bind() {
  document.querySelectorAll("[data-tab]").forEach((button) => button.addEventListener("click", () => switchTab(button.dataset.tab)));
  document.querySelectorAll("[data-action]").forEach((button) => {
    button.addEventListener("pointerdown", (event) => {
      const box = button.getBoundingClientRect();
      button.style.setProperty("--rx", `${event.clientX - box.left}px`);
      button.style.setProperty("--ry", `${event.clientY - box.top}px`);
      button.classList.remove("rip");
      void button.offsetWidth;
      button.classList.add("rip");
    });
    button.addEventListener("click", () => runAction(button.dataset.action));
  });
  document.querySelectorAll("[data-metric]").forEach((button) => button.addEventListener("click", () => openMetric(button.dataset.metric)));
  $("#watchdog").addEventListener("click", () => {
    haptic.select();
    toast("Автовосстановление настраивается в админке панели.");
  });
  const modulesTabs = $('[aria-label="Фильтр модулей"]');
  modulesTabs.querySelectorAll("button").forEach((button) =>
    button.addEventListener("click", () => {
      haptic.select();
      state.moduleFilter = button.dataset.modulesFilter;
      syncSegmented(modulesTabs, "modulesFilter", state.moduleFilter);
      renderModules();
    }),
  );
  syncSegmented(modulesTabs, "modulesFilter", "all");
  $("#modules-search").addEventListener("input", (event) => {
    state.moduleQuery = event.target.value;
    renderModules();
  });
  const logTabs = $('[aria-label="Уровень журнала"]');
  logTabs.querySelectorAll("button").forEach((button) =>
    button.addEventListener("click", () => {
      haptic.select();
      state.logLevel = button.dataset.logsLevel;
      syncSegmented(logTabs, "logsLevel", state.logLevel);
      renderLogs(true);
    }),
  );
  syncSegmented(logTabs, "logsLevel", "all");
  $("#log-chip").addEventListener("click", () => {
    state.logQuery = "";
    haptic.select();
    renderLogs(true);
  });
  const log = $("#log");
  log.addEventListener("scroll", updateJump, { passive: true });
  $("#log-jump").addEventListener("click", () => {
    haptic.light();
    logBottom(true);
  });
  $("#open-panel").addEventListener("click", () => {
    const url = `${location.origin}/`;
    if (tg?.openLink) tg.openLink(url);
    else window.open(url, "_blank", "noopener");
  });
  $("#gate-retry").addEventListener("click", start);
  document.addEventListener("visibilitychange", () => !document.hidden && state.token && refreshOverview());
  // Шапка получает стекло, когда содержимое уходит под неё.
  addEventListener("scroll", () => ($("#bar").toggleAttribute("data-scrolled", scrollY > 6)), { passive: true });
  bindSheet();
}

function applyTelegramChrome() {
  const apply = (scheme) => (document.documentElement.dataset.scheme = scheme === "light" ? "light" : "dark");
  if (!tg) {
    const query = matchMedia("(prefers-color-scheme: light)");
    apply(query.matches ? "light" : "dark");
    query.addEventListener?.("change", () => apply(query.matches ? "light" : "dark"));
    return;
  }
  tg.ready();
  tg.expand();
  // Журнал и шторки прокручиваются вертикально — свайп не должен сворачивать приложение.
  tg.disableVerticalSwipes?.();
  apply(tg.colorScheme);
  tg.onEvent?.("themeChanged", () => apply(tg.colorScheme));
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
  state.ready = false;
  $("#skeleton").hidden = false;
  $("#skeleton").removeAttribute("data-leaving");
  for (const id of ["#app", "#tabbar", "#bar"]) $(id).hidden = false;
  switchTab(state.tab);
}

applyTelegramChrome();
bind();
start();
