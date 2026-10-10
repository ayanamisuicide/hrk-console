// Сравнение сборки и исходников с релизом и переход к отдельной странице установки.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createUpdates(ctx) {
  // Сверяет установленную сборку, стабильный релиз и чистоту копий исходников; отключает установку при
  // небезопасных условиях.
  async function refreshUpdates() {
    try {
      const data = await ctx.adminRequest("/api/admin/updates");
      const short = (value) => (value ? value.slice(0, 12) : "неизвестен");
      const installed = data.installed;
      ctx.$("#update-installed").textContent =
        installed.version + (installed.modified ? " · изменена" : "");
      ctx.$("#update-installed-commit").textContent = short(installed.commit);
      for (const key of ["source", "local"]) {
        const copy = data[key];
        ctx.$("#update-" + key).textContent = !copy.configured
          ? "Не подключена"
          : copy.error
            ? "Ошибка доступа"
            : copy.dirty
              ? "Есть свои изменения"
              : copy.commit === installed.commit
                ? "Совпадает со сборкой"
                : "Отличается от сборки";
        ctx.$("#update-" + key + "-commit").textContent =
          copy.error ||
          short(copy.commit) + (copy.branch ? " / " + copy.branch : "");
      }
      const remote = data.github;
      ctx.$("#update-release").textContent =
        remote.version || (remote.checking ? "Проверяем…" : "Нет данных");
      ctx.$("#update-release-commit").textContent = short(remote.commit);
      ctx.$("#update-main").textContent =
        "GitHub main: " +
        short(remote.main) +
        (remote.checkedAt && !remote.checkedAt.startsWith("0001")
          ? " · Сверка: " + ctx.formatDate(remote.checkedAt)
          : "");
      const blocked = [data.source, data.local].some(
        (copy) =>
          copy.configured &&
          (copy.dirty || copy.error || copy.branch !== "main"),
      );
      const busy = ["checking", "downloading", "restarting"].includes(
        data.job?.phase,
      );
      let message = remote.error
        ? "Сверка с GitHub не выполнена: " + remote.error
        : remote.checking
          ? "Сверяем GitHub…"
          : remote.commit === installed.commit
            ? "Сборка совпадает со стабильным релизом GitHub."
            : remote.commit
              ? "На GitHub доступен другой стабильный релиз: " +
                remote.version +
                "."
              : "Ожидаем результат сверки.";
      if (blocked)
        message +=
          " Обновление заблокировано: сначала сохраните локальные изменения и выберите main.";
      if (!data.enabled)
        message += " Служба установки на этом сервере не настроена.";
      ctx.$("#update-summary").textContent = message;
      ctx.$("#update-job").textContent = data.job
        ? data.job.message +
          (data.job.updatedAt ? " · " + ctx.formatDate(data.job.updatedAt) : "")
        : "";
      const syncNeeded = [data.source, data.local].some(
        (copy) => copy.configured && copy.commit !== remote.commit,
      );
      ctx.$("#updates-install").textContent =
        remote.commit === installed.commit && syncNeeded
          ? "Открыть синхронизацию"
          : "Открыть окно обновления";
      ctx.$("#updates-install").disabled =
        !data.enabled ||
        blocked ||
        busy ||
        !!remote.error ||
        remote.checking ||
        !remote.commit ||
        (remote.commit === installed.commit && !syncNeeded) ||
        installed.modified;
    } catch (error) {
      ctx.$("#update-summary").textContent =
        "Не удалось получить состояние обновлений: " + error.message;
      ctx.$("#updates-install").disabled = true;
    }
  }

  // Открывает отдельную страницу установки, которая продолжит наблюдение при перезапуске панели.
  function openUpdateWindow() {
    const progress = window.open(
      "/admin/update.html",
      "hkc-update-progress",
      "width=980,height=780",
    );
    if (progress) progress.focus();
    else
      ctx.showNotice(
        "Разрешите всплывающие окна для панели обновления",
        "error",
      );
  }
  // Запрашивает повторную проверку релиза и показывает её результат.
  function bindUpdatesCheck() {
    ctx.$("#updates-check").addEventListener("click", async () => {
      ctx.$("#updates-check").disabled = true;
      try {
        await ctx.adminRequest("/api/admin/updates/check", { method: "POST" });
        await ctx.refreshUpdates();
      } catch (error) {
        ctx.showNotice(error.message, "error");
      } finally {
        ctx.$("#updates-check").disabled = false;
      }
    });
  }

  // Открывает отдельный интерфейс подтверждения и наблюдения за установкой.
  function bindUpdatesInstall() {
    ctx.$("#updates-install").addEventListener("click", ctx.openUpdateWindow);
  }

  return {
    refreshUpdates,
    openUpdateWindow,
    bindUpdatesCheck,
    bindUpdatesInstall,
  };
}
