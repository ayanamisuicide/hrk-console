// Раздел «Telegram» админки. Сама страница — общий модуль: в мини-приложении это раздел
// «Управление», и обе показывают одно и то же.
import { createTelegramPage } from "../../modules/telegram-page.js";

export function createTelegram(ctx) {
  let page;

  function bindTelegram() {
    page = createTelegramPage({
      root: ctx.$("#admin-telegram"),
      request: ctx.adminRequest,
      notice: ctx.showNotice,
      confirm: ctx.confirmAction,
      formatDate: ctx.formatDate,
    });
    page.mount();
  }

  function renderTelegram(data) {
    page?.render(data);
  }

  return { renderTelegram, bindTelegram };
}
