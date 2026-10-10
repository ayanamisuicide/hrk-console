// Кнопки управления ботом через защищённый пользовательский API.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createServiceBindings(ctx) {
  // Отправляет выбранное действие бота, блокируя кнопку на время запроса и обновляя статус после ответа.
  function bindBotActions() {
    document.querySelectorAll("[data-action]").forEach((button) => {
      button.addEventListener("click", async () => {
        const action = button.dataset.action;
        button.disabled = true;
        button.classList.add("loading");
        try {
          const result = await ctx.request(`/api/bot/${action}`, {
            method: "POST",
          });
          ctx.showNotice(result.message);
        } catch (error) {
          ctx.showNotice(error.message, "error");
        } finally {
          button.classList.remove("loading");
          setTimeout(ctx.refreshStatus, 500);
        }
      });
    });
  }
  return { bindBotActions };
}
