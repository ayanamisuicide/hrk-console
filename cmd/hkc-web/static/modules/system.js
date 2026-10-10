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

  // Скорость в байтах в секунду с двоичными единицами.
  function formatRate(bytes) {
    if (!bytes) return "0 Б/с";
    return `${ctx.formatBytes(bytes)}/с`;
  }

  // Подпись сетки: целые проценты, дробная часть только у мелких шагов.
  function formatAxisPercent(value) {
    const rounded = Math.round(value * 10) / 10;
    return `${Number.isInteger(rounded) ? rounded : rounded.toFixed(1)}%`;
  }

  function formatPercent(value) {
    return `${(Number(value) || 0).toFixed(value >= 10 ? 0 : 1)}%`;
  }

  // Описания видов графика: серии, оси и форматирование значений.
  const historyViews = {
    resources: {
      key: "resources",
      title: "Ресурсы хоста",
      thresholds: true,
      axes: { left: { minMax: 8, cap: 100, format: formatAxisPercent } },
      series: [
        { key: "cpu", label: "CPU", color: "--chart-1", axis: "left", envelope: "cpuMax", format: formatPercent },
        { key: "memory", label: "Память", short: "RAM", color: "--chart-2", axis: "left", format: formatPercent },
        { key: "disk", label: "Диск", color: "--chart-3", axis: "left", format: formatPercent, fill: false },
      ],
    },
    bot: {
      key: "bot",
      title: "Процесс бота",
      axes: {
        left: { minMax: 4, format: formatAxisPercent },
        right: { minMax: 16 * 1048576, unit: 1048576, format: (v) => ctx.formatBytes(v) },
      },
      series: [
        { key: "botCpu", label: "CPU бота", color: "--chart-1", axis: "left", format: formatPercent },
        { key: "botRss", label: "Память бота", color: "--chart-2", axis: "right", format: (v) => ctx.formatBytes(v) },
      ],
    },
    network: {
      key: "network",
      title: "Сеть",
      axes: {
        left: { minMax: 1024, unit: 1024, format: (v) => (v ? formatRate(v) : "0") },
        right: { minMax: 50, format: (v) => `${Math.round(v)} мс` },
      },
      series: [
        { key: "rx", label: "Приём", color: "--chart-1", axis: "left", format: formatRate },
        { key: "tx", label: "Отправка", color: "--chart-2", axis: "left", format: formatRate },
        { key: "tgMs", label: "Задержка Telegram", short: "Telegram", color: "--chart-3", axis: "right", format: (v) => (v ? `${v.toFixed(0)} мс` : "нет связи"), fill: false, dashed: true, skipZero: true },
      ],
    },
  };

  // Обновляет карточки ресурсов из одного серверного замера; скрытая страница не запускает запрос.
  async function refreshSystem() {
    if (ctx.systemBusy || document.hidden) return;
    ctx.systemBusy = true;
    try {
      const data = await ctx.request("/api/system");
      ctx.lastSystem = data;
      const cpu = data.cpuPercent || 0;
      const ram = ctx.percent(data.memoryUsedBytes, data.memoryTotalBytes);
      const disk = ctx.percent(data.diskUsedBytes, data.diskTotalBytes);
      if (data.supported)
        ctx.animateNumber(ctx.$("#system-cpu"), cpu, { decimals: 1, suffix: "%" });
      else ctx.animateValue(ctx.$("#system-cpu"), "—");
      if (data.memoryTotalBytes)
        ctx.animateNumber(ctx.$("#system-ram"), ram, { decimals: 1, suffix: "%" });
      else ctx.animateValue(ctx.$("#system-ram"), "—");
      if (data.diskTotalBytes)
        ctx.animateNumber(ctx.$("#system-disk"), disk, { decimals: 1, suffix: "%" });
      else ctx.animateValue(ctx.$("#system-disk"), "—");
      setMeter("#system-cpu-bar", cpu);
      setMeter("#system-ram-bar", ram);
      setMeter("#system-disk-bar", disk);
      ctx.pulseText(
        ctx.$("#system-cpu-meta"),
        data.supported ? `${data.cpuCores} логических CPU · нагрузка ${Number(data.load1 || 0).toFixed(2)}` : "Метрики доступны в Linux/WSL",
      );
      ctx.pulseText(
        ctx.$("#system-ram-meta"),
        `${ctx.formatBytes(data.memoryUsedBytes)} из ${ctx.formatBytes(data.memoryTotalBytes)}`,
      );
      ctx.pulseText(
        ctx.$("#system-disk-meta"),
        `свободно ${ctx.formatBytes(data.diskFreeBytes)} из ${ctx.formatBytes(data.diskTotalBytes)}`,
      );
      for (const period of [1, 5, 15]) {
        if (data.supported)
          ctx.animateNumber(ctx.$(`#system-load-${period}`), Number(data[`load${period}`] || 0), { decimals: 2 });
        else ctx.animateValue(ctx.$(`#system-load-${period}`), "—");
      }
      ctx.$("#system-host").textContent = data.hostname || "—";
      ctx.$("#system-platform").textContent = `${data.os}/${data.arch}`;
      ctx.$("#system-kernel").textContent =
        data.kernel || (data.supported ? "—" : "Метрики доступны в Linux/WSL");
      ctx.pulseText(ctx.$("#system-uptime"), ctx.formatUptime(data.uptimeSeconds));
      ctx.pulseText(
        ctx.$("#system-sampled"),
        data.supported
          ? `Обновлено ${new Date(data.sampledAt).toLocaleTimeString("ru-RU")}`
          : "Метрики доступны в Linux/WSL",
      );
      renderMemory(data);
    } catch (error) {
      ctx.$("#system-sampled").textContent = `Ошибка: ${error.message}`;
    } finally {
      ctx.systemBusy = false;
    }
  }

  // Полоса заполнения окрашивается по уровню: норма, внимание, плохо.
  function setMeter(selector, value) {
    const bar = ctx.$(selector);
    bar.style.width = `${Math.min(100, Math.max(0, value))}%`;
    bar.dataset.level = value >= 90 ? "bad" : value >= 75 ? "warn" : "ok";
  }

  // Составная полоса: сегменты сохраняются между опросами, меняется только ширина.
  function renderStack(bar, segments) {
    const total = segments.reduce((sum, item) => sum + item.value, 0) || 1;
    segments.forEach((segment, index) => {
      let element = bar.children[index];
      if (!element) {
        element = document.createElement("i");
        bar.append(element);
      }
      element.dataset.tone = segment.tone;
      element.style.width = `${(segment.value / total) * 100}%`;
      element.title = `${segment.label}: ${segment.text}`;
    });
    while (bar.children.length > segments.length) bar.lastElementChild.remove();
  }

  function renderLegend(list, segments) {
    const signature = JSON.stringify(segments.map((item) => [item.label, item.text, item.tone]));
    if (list.dataset.signature === signature) return;
    list.dataset.signature = signature;
    list.replaceChildren(
      ...segments.map((segment) => {
        const item = document.createElement("li");
        item.dataset.tone = segment.tone;
        const label = document.createElement("span");
        label.textContent = segment.label;
        const value = document.createElement("b");
        value.textContent = segment.text;
        item.append(label, value);
        return item;
      }),
    );
  }

  function renderMemory(data) {
    if (!data.memoryTotalBytes) return;
    const cached = Math.min(data.memoryCachedBytes || 0, data.memoryAvailableBytes || 0);
    const free = Math.max(0, (data.memoryAvailableBytes || 0) - cached);
    const segments = [
      { label: "Занято", value: data.memoryUsedBytes, text: ctx.formatBytes(data.memoryUsedBytes), tone: "used" },
      { label: "Кеш (освобождается)", value: cached, text: ctx.formatBytes(cached), tone: "cache" },
      { label: "Свободно", value: free, text: ctx.formatBytes(free), tone: "free" },
    ];
    renderStack(ctx.$("#memory-bar"), segments);
    renderLegend(ctx.$("#memory-legend"), segments);
    ctx.$("#memory-total").textContent = `всего ${ctx.formatBytes(data.memoryTotalBytes)}`;
    ctx.$("#memory-swap").textContent = !data.swapTotalBytes
      ? "не настроен"
      : data.swapUsedBytes
        ? `${ctx.formatBytes(data.swapUsedBytes)} из ${ctx.formatBytes(data.swapTotalBytes)} (${formatPercent(ctx.percent(data.swapUsedBytes, data.swapTotalBytes))})`
        : `не используется · ${ctx.formatBytes(data.swapTotalBytes)}`;
  }

  // Подробности: сводка, процесс бота, сеть, диск, WSL и процессы. Опрос раз в 5 секунд.
  async function refreshDetails() {
    if (ctx.detailsBusy || document.hidden) return;
    ctx.detailsBusy = true;
    try {
      const data = await ctx.request("/api/system/details");
      renderSummary(data.summary || {});
      renderBot(data.bot || {});
      renderNetwork(data.network || {}, data.probes || []);
      renderDisk(data);
      renderWSL(data.wsl || {});
      renderProcesses(data.processes || [], data.processesHidden);
    } catch (error) {
      ctx.$("#system-summary-title").textContent = `Подробности недоступны: ${error.message}`;
      ctx.$("#system-summary").dataset.level = "bad";
    } finally {
      ctx.detailsBusy = false;
    }
  }

  function renderSummary(summary) {
    const banner = ctx.$("#system-summary");
    const level = summary.level || "ok";
    if (banner.dataset.level !== level) {
      banner.dataset.level = level;
      if (!window.prefersReducedMotion?.()) {
        banner.classList.remove("summary-changed");
        void banner.offsetWidth;
        banner.classList.add("summary-changed");
      }
    }
    ctx.pulseText(ctx.$("#system-summary-title"), summary.title || "Всё в порядке");
    const list = ctx.$("#system-summary-items");
    const items = summary.items || [];
    const signature = JSON.stringify(items);
    if (list.dataset.signature === signature) return;
    list.dataset.signature = signature;
    list.replaceChildren(
      ...(items.length ? items : [{ level: "ok", text: "Бот работает, ресурсы в норме, Telegram доступен" }]).map((item) => {
        const element = document.createElement("li");
        element.dataset.level = item.level;
        element.textContent = item.text;
        return element;
      }),
    );
  }

  function renderBot(bot) {
    const badge = ctx.$("#bot-state");
    badge.textContent = bot.running ? "работает" : "остановлен";
    badge.dataset.level = bot.running ? "ok" : "bad";
    const set = (selector, value) => ctx.pulseText(ctx.$(selector), value);
    set("#bot-pid", bot.running ? String(bot.pid) : "—");
    set("#bot-uptime", bot.running ? bot.uptime || "—" : "—");
    set("#bot-cpu", bot.running ? formatPercent(bot.cpuPercent) : "—");
    set("#bot-rss", bot.running ? ctx.formatBytes(bot.rssBytes) : "—");
    set("#bot-threads", bot.running ? String(bot.threads) : "—");
    set("#bot-children", bot.running ? String(bot.children) : "—");
    set("#bot-files", bot.running ? (bot.openFiles >= 0 ? String(bot.openFiles) : "нет доступа") : "—");
    if (bot.running) ctx.animateNumber(ctx.$("#system-bot-rss"), (bot.rssBytes || 0) / 1048576, { formatter: (v) => `${v.toFixed(0)} МБ` });
    else ctx.animateValue(ctx.$("#system-bot-rss"), "—");
    const total = ctx.lastSystem?.memoryTotalBytes || 0;
    setMeter("#system-bot-bar", total && bot.running ? ctx.percent(bot.rssBytes, total) : 0);
    ctx.pulseText(
      ctx.$("#system-bot-meta"),
      bot.running ? `CPU ${formatPercent(bot.cpuPercent)} · ${bot.threads} потоков` : "процесс не найден",
    );
    const trend = ctx.$("#bot-trend");
    const perHour = bot.rssTrendPerHour || 0;
    trend.dataset.level = "";
    if (!bot.running) trend.textContent = "Бот не запущен.";
    else if (!bot.rssTrendFit) trend.textContent = "Тренд памяти появится через 10 минут работы.";
    else if (bot.rssTrendFit >= 0.8 && perHour > bot.rssBytes * 0.05) {
      trend.textContent = `Память растёт на ${ctx.formatBytes(perHour)} в час — похоже на утечку. Перезапуск освободит память.`;
      trend.dataset.level = "warn";
    } else if (Math.abs(perHour) < 1048576) trend.textContent = "Память стабильна за последний час.";
    else trend.textContent = `За последний час память ${perHour > 0 ? "растёт" : "снижается"} примерно на ${ctx.formatBytes(Math.abs(perHour))} в час.`;
  }

  function renderNetwork(network, probes) {
    ctx.pulseText(ctx.$("#net-rx"), formatRate(network.rxRate));
    ctx.pulseText(ctx.$("#net-tx"), formatRate(network.txRate));
    ctx.pulseText(ctx.$("#net-rx-total"), `всего ${ctx.formatBytes(network.rxTotal)}`);
    ctx.pulseText(ctx.$("#net-tx-total"), `всего ${ctx.formatBytes(network.txTotal)}`);
    const reachable = probes.filter((probe) => probe.ok);
    const badge = ctx.$("#network-state");
    badge.textContent = !probes.length ? "проверяем" : reachable.length ? `доступно ${reachable.length} из ${probes.length}` : "нет связи";
    badge.dataset.level = !probes.length ? "" : reachable.length === probes.length ? "ok" : reachable.length ? "warn" : "bad";
    const list = ctx.$("#probe-list");
    const worst = Math.max(150, ...reachable.map((probe) => probe.latencyMs));
    const best = Math.min(...reachable.map((probe) => probe.latencyMs));
    probes.forEach((probe, index) => {
      let item = list.children[index];
      if (!item) {
        item = document.createElement("li");
        item.innerHTML = "<span></span><i><b></b></i><strong></strong>";
        list.append(item);
      }
      item.querySelector("span").textContent = probe.name;
      item.title = probe.ok ? probe.address : `${probe.address}: ${probe.error}`;
      item.dataset.level = !probe.ok ? "bad" : probe.latencyMs > 250 ? "warn" : "ok";
      item.classList.toggle("best", probe.ok && probe.latencyMs === best);
      item.querySelector("b").style.width = probe.ok ? `${Math.max(4, (probe.latencyMs / worst) * 100)}%` : "100%";
      item.querySelector("strong").textContent = probe.ok ? `${Math.round(probe.latencyMs)} мс` : "нет ответа";
    });
    while (list.children.length > probes.length) list.lastElementChild.remove();
  }

  const diskTones = ["d1", "d2", "d3", "d4", "d5", "d6"];
  function renderDisk(data) {
    const usage = data.disk || {};
    const entries = usage.entries || [];
    const scanned = usage.scannedAt && !usage.scannedAt.startsWith("0001");
    ctx.$("#disk-scanned").textContent = scanned
      ? `${usage.partial ? "частично · " : ""}проверено ${new Date(usage.scannedAt).toLocaleTimeString("ru-RU", { hour: "2-digit", minute: "2-digit" })}`
      : "считаем…";
    const top = entries.slice(0, 5);
    const rest = entries.slice(5).reduce((sum, entry) => sum + entry.bytes, 0);
    const segments = top.map((entry, index) => ({
      label: entry.dir ? `${entry.name}/` : entry.name,
      value: entry.bytes,
      text: ctx.formatBytes(entry.bytes),
      tone: diskTones[index],
    }));
    if (rest) segments.push({ label: "Остальное", value: rest, text: ctx.formatBytes(rest), tone: "d6" });
    if (!scanned) segments.splice(0);
    renderStack(ctx.$("#disk-bar"), segments.length ? segments : [{ label: "Считаем", value: 1, text: "…", tone: "free" }]);
    renderLegend(ctx.$("#disk-list"), segments);
    const forecast = data.forecast || {};
    const note = ctx.$("#disk-forecast");
    note.dataset.level = "";
    if (forecast.daysToFull > 0) {
      note.textContent = `Растёт на ${ctx.formatBytes(forecast.growthPerDay)} в сутки — при таком темпе место кончится через ${forecast.daysToFull < 1 ? `${Math.round(forecast.daysToFull * 24)} ч` : `${forecast.daysToFull.toFixed(1)} дн`}.`;
      note.dataset.level = forecast.daysToFull < 7 ? "warn" : "";
    } else if (forecast.basisHours >= 1)
      note.textContent = `За ${Math.round(forecast.basisHours)} ч заметного роста нет — место не заканчивается.`;
    else note.textContent = "Прогноз появится через час наблюдений.";
    ctx.$("#disk-inodes").textContent = data.inodesTotal
      ? `Индексные дескрипторы: занято ${formatPercent(ctx.percent(data.inodesTotal - data.inodesFree, data.inodesTotal))} из ${Number(data.inodesTotal).toLocaleString("ru-RU")}`
      : "";
  }

  function renderWSL(wsl) {
    const note = ctx.$("#memory-wsl");
    note.hidden = !wsl.detected;
    if (!wsl.detected) return;
    const limits = [
      wsl.memory && `память ${wsl.memory}`,
      wsl.processors && `CPU ${wsl.processors}`,
      wsl.swap && `swap ${wsl.swap}`,
    ].filter(Boolean);
    note.textContent = limits.length
      ? `WSL: лимиты из ${wsl.configPath} — ${limits.join(", ")}. Объём выше — это вся память виртуальной машины.`
      : `WSL без своих лимитов${wsl.configPath ? ` в ${wsl.configPath}` : ""}: виртуальная машина получает до половины памяти Windows. Задать предел можно параметром memory= в разделе [wsl2] файла .wslconfig.`;
  }

  function renderProcesses(processes, hidden) {
    ctx.$("#processes-hidden").hidden = !hidden;
    ctx.$(".process-table").hidden = Boolean(hidden);
    const body = ctx.$("#process-rows");
    const botPid = ctx.lastStatus?.pid;
    processes.forEach((process, index) => {
      let row = body.children[index];
      if (!row) {
        row = document.createElement("tr");
        row.innerHTML = "<td></td><td></td><td></td><td></td>";
        body.append(row);
      }
      const cells = row.children;
      cells[0].textContent = process.name;
      cells[1].textContent = process.pid;
      cells[2].textContent = formatPercent(process.cpuPercent);
      cells[3].textContent = ctx.formatBytes(process.rssBytes);
      row.classList.toggle("is-bot", process.pid === botPid);
    });
    while (body.children.length > processes.length) body.lastElementChild.remove();
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

  function ensureChart() {
    if (!ctx.historyChart)
      ctx.historyChart = ctx.createLiveChart(ctx.$("#history-chart"), {
        onZoomChange: (zoomed) => {
          ctx.$("#history-zoom-reset").hidden = !zoomed;
        },
      });
    return ctx.historyChart;
  }

  // Легенда с переключением серий и статистикой диапазона.
  function renderHistoryLegend(view, stats) {
    const legend = ctx.$("#history-legend");
    if (legend.dataset.view !== view.key) {
      legend.dataset.view = view.key;
      legend.replaceChildren(
        ...view.series.map((series) => {
          const button = document.createElement("button");
          button.type = "button";
          button.className = "legend-chip";
          button.dataset.series = series.key;
          button.dataset.color = series.color.slice(2);
          button.setAttribute("aria-pressed", String(!ctx.hiddenSeries.has(series.key)));
          button.innerHTML = "<i></i><span></span><small></small>";
          button.querySelector("span").textContent = series.label;
          button.addEventListener("click", () => {
            const hidden = !ctx.hiddenSeries.has(series.key);
            if (hidden) ctx.hiddenSeries.add(series.key);
            else ctx.hiddenSeries.delete(series.key);
            button.setAttribute("aria-pressed", String(!hidden));
            ctx.historyChart?.setHidden(series.key, hidden);
          });
          ctx.historyChart?.setHidden(series.key, ctx.hiddenSeries.has(series.key));
          return button;
        }),
      );
    }
    for (const series of view.series) {
      const item = stats?.[series.key];
      const text = item
        ? `ср ${series.format(item.avg)} · макс ${series.format(item.max)} · p95 ${series.format(item.p95)}`
        : "нет данных";
      const small = legend.querySelector(`[data-series="${series.key}"] small`);
      if (small && small.textContent !== text) small.textContent = text;
    }
  }

  // Запрашивает выбранный диапазон и передаёт его графику; спарклайны берут последние 5 минут.
  async function refreshHistory() {
    if (ctx.historyBusy || document.hidden) return;
    ctx.historyBusy = true;
    try {
      const data = await ctx.request(`/api/system/history?range=${ctx.historyRange}`);
      const points = data.points || [];
      const view = historyViews[ctx.historyView] || historyViews.resources;
      const chart = ensureChart();
      chart.setData(data, view);
      renderHistoryLegend(view, data.stats);
      const interval = data.intervalSeconds > 1 ? `шаг ${data.intervalSeconds.toLocaleString("ru-RU")} с` : "шаг 1 секунда";
      ctx.$("#history-count").textContent = points.length
        ? `${Number(data.sampleCount ?? points.length).toLocaleString("ru-RU")} замеров · ${interval} · событий ${(data.events || []).length}`
        : "История появится после первых замеров.";
      if (ctx.historyRange === "live") renderSparks(points);
      else if (Date.now() - (ctx.sparkFetchedAt || 0) > 4000) {
        ctx.sparkFetchedAt = Date.now();
        const live = await ctx.request("/api/system/history?range=live");
        renderSparks(live.points || []);
      }
    } catch (error) {
      ctx.$("#history-count").textContent = `История недоступна: ${error.message}`;
    } finally {
      ctx.historyBusy = false;
    }
  }

  function renderSparks(points) {
    const recent = points.slice(-120);
    document.querySelectorAll("[data-spark]").forEach((canvas) => {
      const key = canvas.dataset.spark;
      const color = { cpu: "--chart-1", memory: "--chart-2", disk: "--chart-3", botRss: "--accent" }[key];
      ctx.drawSparkline(canvas, recent.map((point) => Number(point[key]) || 0), color);
    });
  }

  // Подключает ручное обновление ресурсов.
  function bindSystemRefresh() {
    ctx.$("#system-refresh").addEventListener("click", () => {
      ctx.refreshSystem();
      ctx.refreshDetails();
      ctx.refreshHistory();
    });
  }

  // Переключает вид и диапазон графика; выбор вида сохраняется в браузере.
  function bindHistoryChart() {
    document.querySelectorAll("[data-history-range]").forEach((button) =>
      button.addEventListener("click", () => {
        ctx.historyRange = button.dataset.historyRange;
        document
          .querySelectorAll("[data-history-range]")
          .forEach((item) => item.classList.toggle("active", item === button));
        ctx.refreshHistory();
      }),
    );
    const selectView = (key) => {
      ctx.historyView = historyViews[key] ? key : "resources";
      document.querySelectorAll("[data-history-view]").forEach((item) => {
        const active = item.dataset.historyView === ctx.historyView;
        item.classList.toggle("active", active);
        item.setAttribute("aria-selected", String(active));
      });
    };
    selectView(ctx.historyView);
    document.querySelectorAll("[data-history-view]").forEach((button) =>
      button.addEventListener("click", () => {
        selectView(button.dataset.historyView);
        try {
          localStorage.setItem("hkc-history-view", ctx.historyView);
        } catch (_) {
          /* Выбор вида — удобство; без хранилища он просто не запомнится. */
        }
        ctx.refreshHistory();
      }),
    );
    ctx.$("#history-zoom-reset").addEventListener("click", () => ctx.historyChart?.resetZoom());
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
    refreshDetails,
    refreshPanelVersion,
    refreshHistory,
    bindSystemRefresh,
    bindHistoryChart,
  };
}
