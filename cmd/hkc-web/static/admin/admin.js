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

ctx.$ = (selector) => document.querySelector(selector);

ctx.authDialog = ctx.$("#admin-auth");

ctx.animateValue = window.motionValue;

// Токен хранится в рамках вкладки; выбранный раздел — в localStorage.
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

ctx.auditPage = 0;

ctx.auditSignature = "";

ctx.auditEvents = [];

ctx.bindAuditMore();

ctx.bindBotActions();

ctx.bindAdminAuthForm();

ctx.bindInviteForm();

ctx.bindAdminLogout();

ctx.bindAdminRefresh();

ctx.bindCreateBackup();

ctx.bindWatchdogForm();

ctx.bindAlertsForm();

ctx.bindConfigPreview();

ctx.bindConfigForm();

ctx.bindUpdatesCheck();

ctx.bindUpdatesInstall();

ctx.bindPanelRestart();

// Все поля и подписки готовы; refresh сам проверит токен и занятость.
ctx.refresh();

ctx.refreshTimer = setInterval(ctx.refresh, 1000);
