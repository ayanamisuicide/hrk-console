import { createServiceBindings } from "./modules/service.js";
import { createLiveChart } from "./live-chart.js";
import { createSparkline } from "./sparkline.js";
import { createJournal } from "./modules/journal.js";
import { createSystem } from "./modules/system.js";
import { createIncidents } from "./modules/incidents.js";
import { createNavigation } from "./modules/navigation.js";
import { createAuth } from "./modules/auth.js";
import { createModules } from "./modules/modules.js";
import { createDigest } from "./modules/digest.js";
import { createTelegramShell, telegramMode } from "./modules/telegram-shell.js";

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
  createDigest(ctx),
  createTelegramShell(ctx),
);
Object.assign(ctx, createServiceBindings(ctx));

ctx.$ = (selector) => document.querySelector(selector);

ctx.notice = ctx.$("#notice");

ctx.filterInput = ctx.$("#filter");

ctx.autoscroll = ctx.$("#autoscroll");

ctx.timestamps = ctx.$("#timestamps");

ctx.authDialog = ctx.$("#auth-dialog");

ctx.initialInvite = new URLSearchParams(location.search).get("invite") || "";

ctx.authMode = ctx.initialInvite ? "register" : "login";

ctx.authenticated = false;

// Мини-приложение Telegram — та же страница по адресу /tg/ со входом через Telegram.
ctx.telegramMode = telegramMode;

ctx.stream = undefined;

// Мини-приложение открывается на «Системе»: журнал не должен успеть записать в адрес свой раздел.
ctx.currentView = telegramMode ? "system" : "logs";

ctx.lastStatus = null;

ctx.statusBusy = false;

ctx.incidentsBusy = false;

ctx.incidentsSignature = "";

ctx.savedFilters = {};

ctx.historyRange = "live";

ctx.historyView = "resources";

// Скрытые серии графика переживают переключение вида и диапазона в пределах вкладки.
ctx.hiddenSeries = new Set();

try {
  ctx.historyView = localStorage.getItem("hkc-history-view") || "resources";
} catch (_) {
  /* Без хранилища используется вид по умолчанию. */
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

ctx.bindJournal();

ctx.bindViewLinks();

ctx.setJournalNavOpen(localStorage.getItem("hkc-journal-nav-open") === "true");

ctx.bindJournalNavToggle();

ctx.setView(ctx.initialView() || (telegramMode ? "system" : localStorage.getItem("hkc-view")) || "logs");


ctx.bindSearch();

ctx.systemBusy = false;

ctx.bindSystemRefresh();

ctx.bindIncidentsRefresh();
ctx.bindModules();
ctx.bindDigest();

ctx.historyBusy = false;

ctx.detailsBusy = false;

ctx.createLiveChart = createLiveChart;

ctx.createSparkline = createSparkline;

ctx.sparkBusy = false;

ctx.bindHistoryChart();

ctx.commands = [
  { name: "Открыть журнал", run: () => ctx.setView("logs") },
  { name: "Открыть происшествия", run: () => ctx.setView("incidents") },
  { name: "Открыть сводку", run: () => ctx.setView("digest") },
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

ctx.bindAuthSwitch();

ctx.bindAuthForm();

ctx.bindLogout();

ctx.bindCopyConsoleLink();

// Подписки готовы: проверяем сессию и только после входа загружаем данные.
// В мини-приложении сначала вход через Telegram, затем та же загрузка.
if (telegramMode) ctx.startTelegram();
else ctx.bootstrap();

// Частые обновления запускаются лишь при нужном состоянии страницы.
setInterval(() => {
  if (ctx.authenticated && !document.hidden) ctx.refreshStatus();
}, 1000);

// Частые обновления запускаются лишь при нужном состоянии страницы.
// Живой диапазон опрашивается раз в секунду, длинные — раз в 5 секунд: их точки
// усреднены по интервалам и чаще не меняются. Подробности тяжелее и идут раз в 5 секунд.
let systemTick = 0;
setInterval(() => {
  if (ctx.authenticated && ctx.currentView === "system") {
    ctx.refreshSystem();
    if (ctx.historyRange === "live" || systemTick % 5 === 0) ctx.refreshHistory();
    // Спарклайны всегда показывают последние минуты и обновляются каждую секунду.
    if (ctx.historyRange !== "live") ctx.refreshSparks();
    if (systemTick % 5 === 0) ctx.refreshDetails();
    systemTick++;
  } else systemTick = 0;
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

// Сводка собирается по архиву журнала: раз в полминуты хватает.
setInterval(() => {
  if (ctx.authenticated && ctx.currentView === "digest" && !document.hidden)
    ctx.refreshDigest();
}, 30000);

// «Управление» в мини-приложении: состояние Telegram, тишины и автовосстановления.
setInterval(() => {
  if (ctx.authenticated && ctx.currentView === "control" && !document.hidden)
    ctx.refreshControl?.();
}, 3000);

// Мини-приложению работа без сети не нужна: оно открывается только из Telegram.
if ("serviceWorker" in navigator && !telegramMode)
  navigator.serviceWorker.register("/sw.js").catch(() => {});
