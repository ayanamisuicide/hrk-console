import { createServiceBindings } from "./modules/service.js";
import * as historyChart from "./history-chart.js";
import { createJournal } from "./modules/journal.js";
import { createSystem } from "./modules/system.js";
import { createIncidents } from "./modules/incidents.js";
import { createNavigation } from "./modules/navigation.js";
import { createAuth } from "./modules/auth.js";
import { createModules } from "./modules/modules.js";

// Состояние принадлежит этой странице; модули получают его явно через ctx.
// Фабрики лишь создают замыкания. Сначала собираем функции, затем задаём
// ссылки DOM и состояние; до этого события и запросы не запускаются.
const ctx = {};
Object.assign(
  ctx,
  createJournal(ctx),
  createSystem(ctx),
  createIncidents(ctx),
  createNavigation(ctx),
  createAuth(ctx),
  createModules(ctx),
);
Object.assign(ctx, createServiceBindings(ctx));

ctx.$ = (selector) => document.querySelector(selector);

ctx.logEl = ctx.$("#log");

ctx.notice = ctx.$("#notice");

ctx.filterInput = ctx.$("#filter");

ctx.autoscroll = ctx.$("#autoscroll");

ctx.timestamps = ctx.$("#timestamps");

ctx.authDialog = ctx.$("#auth-dialog");

ctx.initialInvite = new URLSearchParams(location.search).get("invite") || "";

// В памяти хранится ограниченная история экрана; это не лимит файла на сервере.
ctx.maxLines = 1500;

ctx.authMode = ctx.initialInvite ? "register" : "login";

ctx.authenticated = false;

ctx.allLines = [];

ctx.stream = undefined;

ctx.activeLevel = "ALL";

// Пауза задерживает отображение: SSE остаётся подключённым и пишет в pausedLines.
ctx.streamPaused = false;

ctx.pausedLines = [];

ctx.logClearedByUser = false;

ctx.currentView = "logs";

ctx.lastStatus = null;

ctx.statusBusy = false;

ctx.incidentsBusy = false;

ctx.incidentsSignature = "";

ctx.savedFilters = {};

ctx.bookmarks = new Set();

ctx.bookmarksOnly = false;

ctx.historyRange = "live";

try {
  ctx.bookmarks = new Set(
    JSON.parse(localStorage.getItem("hkc-log-bookmarks") || "[]"),
  );
} catch (_) {
  localStorage.removeItem("hkc-log-bookmarks");
}

ctx.animateValue = window.motionValue;

// WeakMap связывает кадры с DOM-элементами, не удерживая удалённые элементы.
ctx.numericAnimations = new WeakMap();

try {
  const stored = JSON.parse(localStorage.getItem("hkc-log-presets") || "{}");
  if (stored && typeof stored === "object" && !Array.isArray(stored))
    ctx.savedFilters = stored;
} catch (_) {
  localStorage.removeItem("hkc-log-presets");
}

ctx.refreshPresetOptions();

ctx.bindViewLinks();

ctx.setJournalNavOpen(localStorage.getItem("hkc-journal-nav-open") === "true");

ctx.bindJournalNavToggle();

ctx.setView(localStorage.getItem("hkc-view") || "logs");


ctx.bindSearch();

ctx.bindFilterChip();

ctx.bindPauseStream();

ctx.systemBusy = false;

ctx.bindSystemRefresh();

ctx.bindIncidentsRefresh();
ctx.bindModules();

ctx.historyBusy = false;

({
  historyColors: ctx.historyColors,
  resampleSeries: ctx.resampleSeries,
  ensureHistorySVG: ctx.ensureHistorySVG,
  morphHistorySeries: ctx.morphHistorySeries,
} = historyChart);

ctx.bindHistoryChart();

ctx.bindBookmarksOnly();

ctx.bindExportLogs();

for (const selector of ["#module-filter", "#time-from", "#time-to"])
  document.querySelector(selector).addEventListener("change", ctx.renderLines);

ctx.bindPresetSave();

ctx.bindPresetSelect();

ctx.bindPresetDelete();

ctx.commands = [
  { name: "Открыть журнал", run: () => ctx.setView("logs") },
  { name: "Открыть происшествия", run: () => ctx.setView("incidents") },
  { name: "Открыть состояние системы", run: () => ctx.setView("system") },
  { name: "Открыть модули бота", run: () => ctx.setView("modules") },
  {
    name: "Найти в журнале",
    run: () => {
      ctx.setView("logs");
      ctx.filterInput.focus();
    },
  },
  {
    name: "Открыть админку",
    run: () => {
      location.href = "/admin/";
    },
  },
  { name: "Сменить тему", run: () => ctx.$("#theme-toggle").click() },
];

ctx.commandIndex = 0;

ctx.bindCommandOpen();

ctx.bindCommandQuery();

ctx.bindCommandQueryKeyboard();

ctx.bindBotActions();

ctx.bindClear();

ctx.bindSearchKeyboard();

ctx.bindTimestamps();

ctx.bindAuthSwitch();

ctx.bindAuthForm();

ctx.bindLogout();

ctx.bindCopyConsoleLink();

// Подписки готовы: проверяем сессию и только после входа загружаем данные.
ctx.bootstrap();

// Частые обновления запускаются лишь при нужном состоянии страницы.
setInterval(() => {
  if (ctx.authenticated && !document.hidden) ctx.refreshStatus();
}, 1000);

// Частые обновления запускаются лишь при нужном состоянии страницы.
setInterval(() => {
  if (ctx.authenticated && ctx.currentView === "system") {
    ctx.refreshSystem();
    ctx.refreshHistory();
  }
}, 1000);

// Частые обновления запускаются лишь при нужном состоянии страницы.
setInterval(() => {
  if (ctx.authenticated && ctx.currentView === "incidents" && !document.hidden)
    ctx.refreshIncidents();
}, 1000);

// Работа без сети необязательна: отсутствие поддержки не мешает онлайн-панели.
setInterval(() => {
  if (ctx.authenticated && ctx.currentView === "modules" && !document.hidden)
    ctx.refreshModules();
}, 1000);

if ("serviceWorker" in navigator)
  navigator.serviceWorker.register("/sw.js").catch(() => {});
