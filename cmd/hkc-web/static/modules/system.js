// Состояние бота и ресурсов хоста, числовые переходы и история графика.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createSystem(ctx) {
  // Интерполирует число через requestAnimationFrame, отменяя предыдущий кадр для элемента. При уменьшенном
  // движении или скрытой странице обновляет сразу.
  function animateNumber(
    element,
    target,
    { decimals = 0, suffix = "", formatter } = {},
  ) {
    if (!element || !Number.isFinite(target)) return;
    const format =
      formatter || ((value) => `${value.toFixed(decimals)}${suffix}`);
    const reduced =
      window.prefersReducedMotion?.() ??
      window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    const previousState = ctx.numericAnimations.get(element);
    if (previousState?.frame) cancelAnimationFrame(previousState.frame);
    const parsed = Number.parseFloat(element.dataset.motionValue);
    const from = Number.isFinite(parsed) ? parsed : target;
    element.dataset.motionValue = String(target);
    if (reduced || document.hidden || from === target) {
      element.textContent = format(target);
      return;
    }
    const state = { frame: 0 };
    ctx.numericAnimations.set(element, state);
    const started = performance.now();
    const duration = 760;
    element.classList.add("number-tweening");
    const draw = (now) => {
      const progress = Math.min(1, (now - started) / duration);
      const eased = 1 - Math.pow(1 - progress, 4);
      element.textContent = format(from + (target - from) * eased);
      if (progress < 1) state.frame = requestAnimationFrame(draw);
      else {
        element.textContent = format(target);
        element.classList.remove("number-tweening");
        state.frame = 0;
      }
    };
    state.frame = requestAnimationFrame(draw);
  }

  // Меняет текст только при отличии и кратко выделяет его, если анимация разрешена.
  function pulseText(element, value) {
    if (!element || element.textContent === value) return;
    element.textContent = value;
    if (window.prefersReducedMotion?.() || document.hidden) return;
    element.classList.remove("live-text-update");
    void element.offsetWidth;
    element.classList.add("live-text-update");
  }

  // Обновляет статус и доступность действий бота; флаг занятости сбрасывается даже после ошибки.
  async function refreshStatus() {
    if (ctx.statusBusy) return;
    ctx.statusBusy = true;
    try {
      const status = await ctx.request("/api/status");
      ctx.lastStatus = status;
      ctx.$("#status-dot").classList.toggle("online", status.running);
      ctx.$("#status-card").classList.toggle("online", status.running);
      ctx.animateValue(
        ctx.$("#status-label"),
        status.running ? "бот запущен" : "бот остановлен",
      );
      ctx.$("#status-meta").textContent = status.running
        ? `PID ${status.pid} · ${status.uptime}`
        : "процесс не найден";
      ctx.$("#version").textContent =
        `Панель ${window.panelVersion || "—"} · бот ${status.version || "не определён"}`;
      ctx.$("#heroku-dir").textContent = status.herokuDir;
      document.querySelector('[data-action="start"]').disabled = status.running;
      document.querySelector('[data-action="stop"]').disabled = !status.running;
      document.querySelector('[data-action="restart"]').disabled =
        !status.running;
      if (!status.running && status.startupLog)
        ctx.$("#status-meta").title = status.startupLog;
    } catch (error) {
      ctx.$("#status-label").textContent = "сервер недоступен";
      ctx.$("#status-meta").textContent = error.message;
    } finally {
      ctx.statusBusy = false;
    }
  }

  // Показывает байты как мегабайты, а отсутствие измерения — тире.
  function formatMemory(bytes) {
    return bytes ? `${(bytes / 1048576).toFixed(1)} МБ` : "—";
  }

  // Выбирает читаемую двоичную единицу объёма до терабайтов.
  function formatBytes(bytes) {
    if (!bytes) return "—";
    const units = ["Б", "КБ", "МБ", "ГБ", "ТБ"];
    let value = bytes;
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) {
      value /= 1024;
      unit++;
    }
    return `${value.toFixed(unit < 2 ? 0 : 1)} ${units[unit]}`;
  }

  // Переводит секунды в дни, часы и минуты без пустых старших разрядов.
  function formatUptime(seconds) {
    if (!seconds) return "—";
    const days = Math.floor(seconds / 86400);
    const hours = Math.floor((seconds % 86400) / 3600);
    const minutes = Math.floor((seconds % 3600) / 60);
    return [
      days ? `${days} д` : "",
      hours ? `${hours} ч` : "",
      `${minutes} мин`,
    ]
      .filter(Boolean)
      .join(" ");
  }

  // Вычисляет процент с ограничением 0–100 и защитой от деления на ноль.
  function percent(used, total) {
    return total ? Math.min(100, Math.max(0, (used / total) * 100)) : 0;
  }

  // Обновляет карточки ресурсов из одного серверного замера; скрытая страница не запускает запрос.
  async function refreshSystem() {
    if (ctx.systemBusy || document.hidden) return;
    ctx.systemBusy = true;
    try {
      const data = await ctx.request("/api/system");
      const cpu = data.cpuPercent || 0;
      const ram = ctx.percent(data.memoryUsedBytes, data.memoryTotalBytes);
      const disk = ctx.percent(data.diskUsedBytes, data.diskTotalBytes);
      if (data.supported)
        ctx.animateNumber(ctx.$("#system-cpu"), cpu, {
          decimals: 1,
          suffix: "%",
        });
      else ctx.animateValue(ctx.$("#system-cpu"), "—");
      if (data.memoryTotalBytes)
        ctx.animateNumber(ctx.$("#system-ram"), ram, {
          decimals: 1,
          suffix: "%",
        });
      else ctx.animateValue(ctx.$("#system-ram"), "—");
      if (data.diskTotalBytes)
        ctx.animateNumber(ctx.$("#system-disk"), disk, {
          decimals: 1,
          suffix: "%",
        });
      else ctx.animateValue(ctx.$("#system-disk"), "—");
      ctx.$("#system-cpu-bar").style.width = `${cpu}%`;
      ctx.$("#system-ram-bar").style.width = `${ram}%`;
      ctx.$("#system-disk-bar").style.width = `${disk}%`;
      ctx.pulseText(
        ctx.$("#system-cpu-meta"),
        data.supported
          ? `${data.cpuCores} логических CPU`
          : "Метрики доступны в Linux/WSL",
      );
      ctx.pulseText(
        ctx.$("#system-ram-meta"),
        `${ctx.formatBytes(data.memoryUsedBytes)} из ${ctx.formatBytes(data.memoryTotalBytes)} · свободно ${ctx.formatBytes(data.memoryAvailableBytes)}`,
      );
      ctx.pulseText(
        ctx.$("#system-disk-meta"),
        `${ctx.formatBytes(data.diskUsedBytes)} из ${ctx.formatBytes(data.diskTotalBytes)} · свободно ${ctx.formatBytes(data.diskFreeBytes)}`,
      );
      for (const period of [1, 5, 15]) {
        if (data.supported)
          ctx.animateNumber(
            ctx.$(`#system-load-${period}`),
            Number(data[`load${period}`] || 0),
            { decimals: 2 },
          );
        else ctx.animateValue(ctx.$(`#system-load-${period}`), "—");
      }
      ctx.$("#system-host").textContent = data.hostname || "—";
      ctx.$("#system-platform").textContent = `${data.os}/${data.arch}`;
      ctx.$("#system-kernel").textContent =
        data.kernel || (data.supported ? "—" : "Метрики доступны в Linux/WSL");
      ctx.pulseText(
        ctx.$("#system-uptime"),
        ctx.formatUptime(data.uptimeSeconds),
      );
      ctx.pulseText(
        ctx.$("#system-sampled"),
        data.supported
          ? `Обновлено ${new Date(data.sampledAt).toLocaleTimeString("ru-RU")}`
          : "Метрики доступны в Linux/WSL",
      );
    } catch (error) {
      ctx.$("#system-sampled").textContent = `Ошибка: ${error.message}`;
    } finally {
      ctx.systemBusy = false;
    }
  }

  // Получает версию самой панели отдельно от версии бота; ошибка не блокирует статус бота.
  async function refreshPanelVersion() {
    try {
      const data = await ctx.request("/api/version");
      window.panelVersion = data.version || "—";
      if (ctx.lastStatus)
        ctx.$("#version").textContent =
          `Панель ${window.panelVersion} · бот ${ctx.lastStatus.version || "не определён"}`;
    } catch (_) {
      /* Состояние бота остаётся доступным независимо от запроса версии панели. */
    }
  }

  // Запрашивает выбранный диапазон, обновляет SVG-серии и отмечает смену ненулевого PID.
  async function refreshHistory() {
    if (ctx.historyBusy || document.hidden) return;
    ctx.historyBusy = true;
    try {
      const data = await ctx.request(
        `/api/system/history?range=${ctx.historyRange}`,
      );
      const points = data.points || [];
      const chart = ctx.$("#history-chart");
      ctx.$("#history-count").textContent =
        `${Number(data.sampleCount ?? points.length).toLocaleString("ru-RU")} замеров · шаг 1 секунда`;
      if (points.length < 2) {
        chart.textContent =
          "История появится после второго замера (около 1 секунды).";
        return;
      }
      const ns = "http://www.w3.org/2000/svg";
      const svg = ctx.ensureHistorySVG(chart);
      const start = new Date(points[0].at).getTime();
      const span = Math.max(1, new Date(points.at(-1).at).getTime() - start);
      const x = (point) =>
        ((new Date(point.at).getTime() - start) / span) * 1000;
      for (const key of Object.keys(ctx.historyColors))
        ctx.morphHistorySeries(
          svg.querySelector(`[data-series="${key}"]`),
          svg.querySelector(`[data-dot="${key}"]`),
          ctx.resampleSeries(points, key),
        );
      const markerGroup = svg.querySelector(".history-markers");
      markerGroup.replaceChildren();
      for (let i = 1; i < points.length; i++)
        if (
          points[i].pid &&
          points[i - 1].pid &&
          points[i].pid !== points[i - 1].pid
        ) {
          const marker = document.createElementNS(ns, "line");
          marker.setAttribute("x1", x(points[i]));
          marker.setAttribute("x2", x(points[i]));
          marker.setAttribute("y1", "0");
          marker.setAttribute("y2", "200");
          marker.setAttribute("stroke", "var(--bad)");
          marker.setAttribute("stroke-dasharray", "5 5");
          markerGroup.append(marker);
        }
    } catch (error) {
      ctx.$("#history-chart").textContent =
        `История недоступна: ${error.message}`;
    } finally {
      ctx.historyBusy = false;
    }
  }
  // Подключает ручное обновление ресурсов.
  function bindSystemRefresh() {
    ctx.$("#system-refresh").addEventListener("click", ctx.refreshSystem);
  }

  // Меняет временной диапазон, очищает старый SVG и загружает новую историю.
  function bindHistoryChart() {
    document.querySelectorAll("[data-history-range]").forEach((button) =>
      button.addEventListener("click", () => {
        ctx.historyRange = button.dataset.historyRange;
        document
          .querySelectorAll("[data-history-range]")
          .forEach((item) => item.classList.toggle("active", item === button));
        ctx.$("#history-chart").replaceChildren();
        ctx.refreshHistory();
      }),
    );
  }

  return {
    animateNumber,
    pulseText,
    refreshStatus,
    formatMemory,
    formatBytes,
    formatUptime,
    percent,
    refreshSystem,
    refreshPanelVersion,
    refreshHistory,
    bindSystemRefresh,
    bindHistoryChart,
  };
}
