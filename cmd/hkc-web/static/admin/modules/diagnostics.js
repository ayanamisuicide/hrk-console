// Диагностические команды и подтверждаемая произвольная команда терминала.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createDiagnosticsBindings(ctx) {
  // Запускает фиксированную диагностическую команду и показывает текстовый результат.
  function bindDiagnosticOutput() {
    document.querySelectorAll("[data-diagnostic]").forEach((button) =>
      button.addEventListener("click", async () => {
        button.disabled = true;
        ctx.$("#diagnostic-output").textContent = "Выполняется проверка…";
        try {
          const result = await ctx.adminRequest(
            `/api/admin/diagnostics/${button.dataset.diagnostic}`,
            { method: "POST" },
          );
          ctx.$("#diagnostic-output").textContent =
            result.output || "Команда не вернула данные.";
        } catch (error) {
          ctx.$("#diagnostic-output").textContent = error.message;
        }
        button.disabled = false;
      }),
    );
  }

  // Отправляет подтверждённую команду терминала; блокирует повторный запуск до получения результата.
  function bindTerminalRun() {
    ctx.terminalForm.addEventListener("submit", async (event) => {
      event.preventDefault();
      const command = ctx.terminalCommand.value.trim();
      if (!command) {
        ctx.terminalCommand.focus();
        return;
      }
      const potentiallyDangerous =
        /(?:^|[\s;&|])(rm|sudo|chmod|chown|dd|mkfs|reboot|shutdown|kill|pkill|truncate|git\s+reset|git\s+clean)(?:\s|$)/i.test(
          command,
        );
      const warning = potentiallyDangerous
        ? "⚠ Команда похожа на потенциально опасную. Проверьте её особенно внимательно. "
        : "";
      if (
        !(await ctx.confirmAction(
          "Выполнить команду в WSL?",
          `${warning}Команда будет запущена с правами службы панели в каталоге Heroku: ${command}`,
        ))
      )
        return;
      const button = ctx.$("#terminal-run");
      button.disabled = true;
      ctx.terminalCommand.disabled = true;
      ctx.terminalMeta.textContent = "Выполняется…";
      ctx.terminalOutput.textContent = `$ ${command}\n`;
      try {
        const result = await ctx.adminRequest("/api/admin/terminal", {
          method: "POST",
          body: JSON.stringify({ command, confirmed: true }),
        });
        ctx.terminalOutput.textContent +=
          result.output || "(команда не вернула вывод)";
        ctx.terminalMeta.textContent = `${result.actor || "администратор"} · exit ${result.exitCode} · ${result.durationMs} мс${result.timedOut ? " · таймаут" : ""}`;
      } catch (error) {
        ctx.terminalOutput.textContent += error.message;
        ctx.terminalMeta.textContent = "Ошибка выполнения";
      }
      ctx.terminalCommand.disabled = false;
      button.disabled = false;
      ctx.terminalCommand.focus();
    });
  }

  // Связывает отправку формы и клавиатурный запуск с одной кнопкой исполнения.
  function bindTerminalForm() {
    ctx.terminalCommand.addEventListener("keydown", (event) => {
      if (event.key === "Enter" && event.ctrlKey) {
        event.preventDefault();
        ctx.terminalForm.requestSubmit();
      }
    });
  }

  // Очищает только отображаемый вывод и метаданные терминала.
  function bindTerminalClear() {
    ctx.$("#terminal-clear").addEventListener("click", () => {
      ctx.terminalOutput.textContent = "Вывод очищен.";
      ctx.terminalMeta.textContent = "Готово к команде";
    });
  }
  return {
    bindDiagnosticOutput,
    bindTerminalRun,
    bindTerminalForm,
    bindTerminalClear,
  };
}
