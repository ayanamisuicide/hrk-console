import { createDiagnosticsBindings } from "./modules/diagnostics.js";
import { createUsers } from "./modules/users.js";
import { createAudit } from "./modules/audit.js";
import { createConfig } from "./modules/config.js";
import { createOperations } from "./modules/operations.js";
import { createNavigation } from "./modules/navigation.js";
import { createAuth } from "./modules/auth.js";
import { createUpdates } from "./modules/updates.js";

// Состояние принадлежит этой странице; модули получают его явно через ctx.
const ctx = {};
Object.assign(
  ctx,
  createUsers(ctx),
  createAudit(ctx),
  createConfig(ctx),
  createOperations(ctx),
  createNavigation(ctx),
  createAuth(ctx),
  createUpdates(ctx),
);
Object.assign(ctx, createDiagnosticsBindings(ctx));

ctx.$ = (selector) => document.querySelector(selector);

ctx.authDialog = ctx.$("#admin-auth");

ctx.animateValue = window.motionValue;

document.documentElement.dataset.theme =
  localStorage.getItem("hkc-theme") || "dark";

ctx.bindAdminTheme();

// Токен хранится в рамках вкладки; тема и выбранный раздел — в localStorage.
ctx.adminToken = sessionStorage.getItem("hkc-admin-token") || "";

ctx.refreshTimer = undefined;

ctx.refreshBusy = false;

// Сигнатуры данных предотвращают лишнюю пересборку списков при секундном опросе.
ctx.usersSignature = "";

ctx.invitesSignature = "";

ctx.backupsSignature = "";

ctx.configHistorySignature = "";

ctx.securitySignature = "";

ctx.configSignature = "";

ctx.bindAdminNavigation();

ctx.auditVisible = 8;

ctx.auditSignature = "";

ctx.auditEvents = [];

ctx.auditOpen = new Set();

ctx.bindAuditMore();

ctx.bindBotActions();

ctx.bindAdminAuthForm();

ctx.bindInviteForm();

ctx.bindAdminLogout();

ctx.bindAdminRefresh();

ctx.bindCreateBackup();

ctx.bindDownloadDiagnostics();

ctx.bindWatchdogForm();

ctx.bindConfigPreview();

ctx.bindConfigForm();

ctx.bindDiagnosticOutput();

ctx.terminalForm = ctx.$("#terminal-form");

ctx.terminalCommand = ctx.$("#terminal-command");

ctx.terminalOutput = ctx.$("#terminal-output");

ctx.terminalMeta = ctx.$("#terminal-meta");

ctx.bindTerminalRun();

ctx.bindTerminalForm();

ctx.bindTerminalClear();

ctx.bindUpdatesCheck();

ctx.bindUpdatesInstall();

// Все поля и подписки готовы; refresh сам проверит токен и занятость.
ctx.refresh();

ctx.refreshTimer = setInterval(ctx.refresh, 1000);
