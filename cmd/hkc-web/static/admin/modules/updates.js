// Страница обновлений: текущая версия, что нового, установка с живым прогрессом,
// выбор другой версии и откат к резервной сборке. Опрос идёт своим таймером, а не общим
// обновлением админки: во время перезапуска панели запросы падают, и ход установки
// должен переживать эти секунды.
export function createUpdates(ctx) {
  const installSteps = [
    ["prepare", "Подготовка"],
    ["download", "Загрузка"],
    ["verify", "Проверка подлинности"],
    ["backup", "Резервная копия"],
    ["switch", "Установка"],
    ["health", "Запуск"],
    ["sources", "Исходники"],
    ["done", "Готово"],
  ];
  const rollbackSteps = [
    ["prepare", "Подготовка"],
    ["verify", "Проверка сборки"],
    ["backup", "Резервная копия"],
    ["switch", "Возврат"],
    ["health", "Запуск"],
    ["done", "Готово"],
  ];
  const finished = ["complete", "rolled_back", "failed"];
  const state = {
    overview: null,
    job: null,
    lastOverview: 0,
    busy: false,
    failures: 0,
    watching: false,
    shownPercent: 0,
    percentFrame: 0,
    releasesSignature: "",
    backupsSignature: "",
    versionsSignature: "",
    logSignature: "",
    celebrated: "",
    showAllReleases: false,
  };
  const reduced = () => window.prefersReducedMotion?.() ?? false;

  function setText(selector, value) {
    const element = ctx.$(selector);
    if (element && element.textContent !== value) element.textContent = value;
  }

  function formatSize(bytes) {
    if (!bytes) return "0 Б";
    const units = ["Б", "КБ", "МБ", "ГБ"];
    let value = bytes;
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) {
      value /= 1024;
      unit++;
    }
    return `${value.toFixed(unit < 2 ? 0 : 1)} ${units[unit]}`;
  }

  function formatElapsed(ms) {
    const seconds = Math.max(0, Math.round(ms / 1000));
    return `${Math.floor(seconds / 60)}:${String(seconds % 60).padStart(2, "0")}`;
  }

  // Плавный счётчик процентов и дуга кольца: дуга меняется переходом CSS, число — кадрами.
  function setPercent(percent) {
    percent = Math.max(0, Math.min(100, Math.round(percent)));
    const ring = ctx.$("#upd-ring-value");
    const length = 2 * Math.PI * 52;
    ring.style.strokeDasharray = `${length}`;
    ring.style.strokeDashoffset = `${length * (1 - percent / 100)}`;
    ctx.$("#upd-bar-fill").style.width = `${percent}%`;
    ctx.$(".upd-bar").setAttribute("aria-valuenow", String(percent));
    cancelAnimationFrame(state.percentFrame);
    const from = state.shownPercent;
    if (reduced() || from === percent) {
      state.shownPercent = percent;
      setText("#upd-percent", `${percent}%`);
      return;
    }
    const started = performance.now();
    const tick = (now) => {
      const progress = Math.min(1, (now - started) / 650);
      const value = Math.round(from + (percent - from) * (1 - Math.pow(1 - progress, 3)));
      state.shownPercent = value;
      setText("#upd-percent", `${value}%`);
      if (progress < 1) state.percentFrame = requestAnimationFrame(tick);
    };
    state.percentFrame = requestAnimationFrame(tick);
  }

  // Салют из частиц при успешном завершении.
  function celebrate() {
    if (reduced()) return;
    const burst = ctx.$("#upd-burst");
    burst.replaceChildren();
    const colors = ["var(--accent)", "var(--ok)", "var(--warn)", "var(--chart-1)", "var(--chart-2)"];
    for (let index = 0; index < 28; index++) {
      const particle = document.createElement("i");
      const angle = (index / 28) * Math.PI * 2 + Math.random() * 0.3;
      const distance = 70 + Math.random() * 60;
      particle.style.setProperty("--dx", `${Math.cos(angle) * distance}px`);
      particle.style.setProperty("--dy", `${Math.sin(angle) * distance}px`);
      particle.style.setProperty("--spin", `${Math.random() * 540 - 270}deg`);
      particle.style.background = colors[index % colors.length];
      particle.style.animationDelay = `${Math.random() * 120}ms`;
      burst.append(particle);
    }
    setTimeout(() => burst.replaceChildren(), 1600);
  }

  function heroState(name) {
    const hero = ctx.$("#upd-hero");
    if (hero.dataset.state === name) return;
    hero.dataset.state = name;
    if (reduced()) return;
    hero.classList.remove("upd-enter");
    void hero.offsetWidth;
    hero.classList.add("upd-enter");
  }

  // Верхняя карточка: версия, статус и главное действие.
  function renderHero(overview, job) {
    const installed = overview.installed || {};
    const latest = overview.latest || {};
    const running = overview.running || (job && !finished.includes(job.phase) && job.phase);
    setText("#upd-current", installed.version || "—");
    const install = ctx.$("#upd-install");
    const target = ctx.$("#upd-target");
    const flow = ctx.$("#upd-flow");
    const checked = latest.checkedAt && !latest.checkedAt.startsWith("0001")
      ? `Проверено ${ctx.formatDate(latest.checkedAt)}`
      : "";
    setText("#upd-checked", latest.checking ? "Проверяем…" : checked);
    if (running) {
      heroState("running");
      setText("#upd-eyebrow", job?.action === "rollback" ? "Откат" : "Обновление");
      setText("#upd-title", job?.version ? `${job.action === "rollback" ? "Возвращаем" : "Устанавливаем"} ${job.version}` : "Выполняется…");
      setText("#upd-subtitle", "Не закрывайте страницу — или закройте: установка продолжится без неё.");
      target.hidden = !job?.version;
      flow.hidden = !job?.version;
      setText("#upd-target", job?.version || "");
      install.hidden = true;
      setPercent(job?.progress || 0);
      return;
    }
    if (job && finished.includes(job.phase) && job.updatedAt && Date.now() - Date.parse(job.updatedAt) < 10 * 60000) {
      if (job.phase === "complete") {
        heroState("done");
        setText("#upd-eyebrow", "Готово");
        setText("#upd-title", job.action === "rollback" ? `Откат выполнен: ${job.version || installed.version}` : `Установлена ${job.version || installed.version}`);
        setText("#upd-subtitle", job.warnings?.length ? "Работает, но есть замечания — они ниже." : "Панель перезапущена и работает на новой версии.");
        if (state.celebrated !== job.updatedAt) {
          state.celebrated = job.updatedAt;
          celebrate();
        }
      } else {
        heroState("failed");
        setText("#upd-eyebrow", job.phase === "rolled_back" ? "Возвращена прежняя версия" : "Не получилось");
        setText("#upd-title", job.phase === "rolled_back" ? "Новая версия не запустилась" : "Установка остановлена");
        setText("#upd-subtitle", job.message || "Подробности — в журнале ниже.");
      }
      setPercent(100);
    } else if (overview.updateAvailable) {
      heroState("available");
      setText("#upd-eyebrow", "Доступно обновление");
      setText("#upd-title", `Вышла ${latest.version}`);
      setText("#upd-subtitle", "Посмотрите, что нового, и обновитесь в один клик. Текущая версия сохранится для отката.");
      setPercent(0);
    } else if (latest.error) {
      heroState("error");
      setText("#upd-eyebrow", "Нет связи с GitHub");
      setText("#upd-title", "Не удалось проверить обновления");
      setText("#upd-subtitle", latest.error);
      setPercent(0);
    } else if (latest.version) {
      heroState("current");
      setText("#upd-eyebrow", "Версия панели");
      setText("#upd-title", "У вас последняя версия");
      setText("#upd-subtitle", "Новые версии проверяются каждые 10 минут; о выходе придёт уведомление.");
      setPercent(100);
    } else {
      heroState("loading");
      setText("#upd-title", "Проверяем обновления…");
      setText("#upd-subtitle", "Смотрим, что есть на GitHub.");
    }
    const available = overview.updateAvailable && !overview.blockers?.length;
    target.hidden = !overview.updateAvailable;
    flow.hidden = !overview.updateAvailable;
    setText("#upd-target", latest.version || "");
    install.hidden = !overview.updateAvailable;
    install.disabled = !available;
    setText("#upd-install", `Обновить до ${latest.version || ""}`);
  }

  function renderBlockers(blockers) {
    const list = ctx.$("#upd-blockers");
    list.hidden = !blockers.length;
    const signature = JSON.stringify(blockers);
    if (list.dataset.signature === signature) return;
    list.dataset.signature = signature;
    list.replaceChildren(
      ...blockers.map((item) => {
        const element = document.createElement("li");
        const text = document.createElement("strong");
        text.textContent = item.text;
        element.append(text);
        if (item.fix) {
          const fix = document.createElement("span");
          fix.textContent = item.fix;
          element.append(fix);
        }
        return element;
      }),
    );
  }

  // Шаги установки: пройденные, текущий и ожидающие; линия между ними заполняется.
  function renderProgress(job) {
    const card = ctx.$("#upd-progress");
    const active = job && job.phase && (!finished.includes(job.phase) || (job.updatedAt && Date.now() - Date.parse(job.updatedAt) < 10 * 60000));
    card.hidden = !active;
    if (!active) return;
    card.dataset.phase = job.phase;
    const steps = job.action === "rollback" ? rollbackSteps : installSteps;
    const list = ctx.$("#upd-steps");
    if (list.dataset.kind !== (job.action || "install")) {
      list.dataset.kind = job.action || "install";
      list.replaceChildren(
        ...steps.map(([key, label]) => {
          const item = document.createElement("li");
          item.dataset.step = key;
          item.innerHTML = '<span class="upd-step-dot"><svg viewBox="0 0 24 24"><path d="m5 12.5 4.5 4.5L19 7.5"/></svg></span><b></b><small></small>';
          item.querySelector("b").textContent = label;
          return item;
        }),
      );
    }
    const keys = steps.map(([key]) => key);
    let current = keys.indexOf(job.step);
    if (current < 0) current = 0;
    const failed = job.phase === "failed" || job.phase === "rolled_back";
    const done = job.phase === "complete";
    const stamps = {};
    for (const event of job.events || []) if (event.step && !stamps[event.step]) stamps[event.step] = event.at;
    [...list.children].forEach((item, index) => {
      let status = index < current ? "done" : index === current ? "active" : "pending";
      if (done) status = "done";
      if (failed && index === current) status = "error";
      if (item.dataset.status !== status) item.dataset.status = status;
      const stamp = stamps[keys[index]];
      const next = stamps[keys[index + 1]];
      let note = "";
      if (status === "done" && stamp && next) note = formatElapsed(Date.parse(next) - Date.parse(stamp));
      if (status === "active" && keys[index] === "download" && job.total)
        note = `${formatSize(job.downloaded)} из ${formatSize(job.total)}`;
      const small = item.querySelector("small");
      if (small.textContent !== note) small.textContent = note;
    });
    list.style.setProperty("--count", String(keys.length));
    list.style.setProperty("--filled-ratio", String(done ? 1 : current / Math.max(1, keys.length - 1)));
    const label = steps[current]?.[1] || "";
    setText("#upd-progress-eyebrow", job.action === "rollback" ? "Откат" : "Установка");
    setText("#upd-step-title", done ? "Готово" : failed ? "Остановлено" : `${label}…`);
    setText("#upd-step-message", job.message || "—");
    const started = Date.parse(job.startedAt || job.updatedAt || "");
    const ended = finished.includes(job.phase) ? Date.parse(job.updatedAt) : Date.now();
    setText("#upd-elapsed", Number.isFinite(started) ? formatElapsed(ended - started) : "");
    if (!document.querySelector("#upd-hero[data-state='running']")) setPercent(job.progress || 0);
    const warnings = ctx.$("#upd-warnings");
    const items = job.warnings || [];
    warnings.hidden = !items.length;
    if (warnings.dataset.signature !== JSON.stringify(items)) {
      warnings.dataset.signature = JSON.stringify(items);
      warnings.replaceChildren(...items.map((text) => Object.assign(document.createElement("li"), { textContent: text })));
    }
    const events = job.events || [];
    const signature = JSON.stringify(events.map((event) => event.at + event.message));
    if (signature !== state.logSignature) {
      state.logSignature = signature;
      ctx.$("#upd-log").replaceChildren(
        ...events.map((event) => {
          const item = document.createElement("li");
          item.dataset.phase = event.phase;
          const time = document.createElement("time");
          time.textContent = new Date(event.at).toLocaleTimeString("ru-RU");
          const text = document.createElement("span");
          text.textContent = event.message;
          item.append(time, text);
          return item;
        }),
      );
    }
  }

  // «Что нового»: версии новее установленной раскрыты и отмечены, прошлые — по кнопке.
  function renderReleases(overview) {
    const installed = overview.installed?.version || "";
    const releases = overview.releases || [];
    const signature = JSON.stringify([installed, releases.map((item) => item.version), state.showAllReleases]);
    if (signature === state.releasesSignature) return;
    state.releasesSignature = signature;
    const container = ctx.$("#upd-releases");
    if (!releases.length) {
      const empty = document.createElement("p");
      empty.className = "muted";
      empty.textContent = overview.latest?.error ? "Список изменений появится, когда GitHub станет доступен." : "Загружаем список изменений…";
      container.replaceChildren(empty);
      ctx.$("#upd-releases-more").hidden = true;
      return;
    }
    const compare = (a, b) => {
      const pa = a.slice(1).split(".").map(Number);
      const pb = b.slice(1).split(".").map(Number);
      for (let index = 0; index < 3; index++) if (pa[index] !== pb[index]) return pa[index] - pb[index];
      return 0;
    };
    const newer = releases.filter((item) => !/^v\d/.test(installed) || compare(item.version, installed) > 0);
    const visible = state.showAllReleases ? releases : newer.length ? newer : releases.slice(0, 1);
    container.replaceChildren(
      ...visible.map((release, index) => {
        const card = document.createElement("article");
        card.className = "upd-release";
        card.style.setProperty("--delay", `${Math.min(index, 6) * 60}ms`);
        const isNew = newer.includes(release);
        const isCurrent = release.version === installed;
        const head = document.createElement("header");
        const title = document.createElement("strong");
        title.textContent = release.version;
        head.append(title);
        if (isNew || isCurrent) {
          const badge = document.createElement("span");
          badge.className = isNew ? "upd-badge new" : "upd-badge";
          badge.textContent = isNew ? "новое" : "установлена";
          head.append(badge);
        }
        const date = document.createElement("time");
        date.textContent = release.date || "";
        head.append(date);
        card.append(head);
        for (const section of release.sections || []) {
          const heading = document.createElement("h4");
          heading.textContent = section.title;
          const list = document.createElement("ul");
          for (const text of section.items) {
            const item = document.createElement("li");
            // Разметка `код` из CHANGELOG показывается моноширинно; остальное — обычным текстом.
            text.split(/(`[^`]+`)/).forEach((part) => {
              if (part.startsWith("`") && part.endsWith("`") && part.length > 2) {
                const code = document.createElement("code");
                code.textContent = part.slice(1, -1);
                item.append(code);
              } else item.append(part.replace(/\*\*/g, ""));
            });
            list.append(item);
          }
          card.append(heading, list);
        }
        return card;
      }),
    );
    const more = ctx.$("#upd-releases-more");
    more.hidden = state.showAllReleases || releases.length <= visible.length;
  }

  function renderVersions(overview) {
    const select = ctx.$("#upd-version");
    const installed = overview.installed?.version;
    const versions = (overview.releases || []).map((item) => item.version);
    const signature = JSON.stringify([installed, versions]);
    if (signature === state.versionsSignature) return;
    state.versionsSignature = signature;
    select.replaceChildren(
      ...versions.map((version) => {
        const option = document.createElement("option");
        option.value = version;
        option.textContent = version === installed ? `${version} — установлена` : version === overview.latest?.version ? `${version} — последняя` : version;
        option.disabled = version === installed;
        return option;
      }),
    );
    const firstOther = versions.find((version) => version !== installed);
    if (firstOther) select.value = firstOther;
  }

  function renderBackups(backups) {
    const list = ctx.$("#upd-backups");
    const signature = JSON.stringify(backups);
    if (signature === state.backupsSignature) return;
    state.backupsSignature = signature;
    if (!backups.length) {
      const empty = document.createElement("li");
      empty.className = "muted";
      empty.textContent = "Резервных копий пока нет — они появятся после первого обновления.";
      list.replaceChildren(empty);
      return;
    }
    list.replaceChildren(
      ...backups.map((backup) => {
        const item = document.createElement("li");
        const info = document.createElement("div");
        const version = document.createElement("strong");
        version.textContent = backup.version || backup.name;
        const meta = document.createElement("small");
        meta.textContent = `${ctx.formatDate(backup.createdAt)} · ${formatSize(backup.size)}`;
        info.append(version, meta);
        const button = document.createElement("button");
        button.type = "button";
        button.className = "compact";
        button.textContent = "Вернуть";
        button.addEventListener("click", () => rollback(backup));
        item.append(info, button);
        return item;
      }),
    );
  }

  function renderTech(overview) {
    const list = ctx.$("#upd-tech");
    const rows = [
      ["Установлена", `${overview.installed?.version || "—"} · ${overview.installed?.commit?.slice(0, 12) || "—"}${overview.installed?.modified ? " · изменена" : ""}`],
      ["Последний релиз", `${overview.latest?.version || "—"} · ${overview.latest?.commit?.slice(0, 12) || "—"}`],
      ["Установка из панели", overview.enabled ? "настроена" : "не настроена"],
      ...(overview.sources || []).map((source, index) => [
        index ? "Доп. копия исходников" : "Исходники",
        `${source.path} · ${source.error ? "ошибка доступа" : `${source.branch} · ${source.commit?.slice(0, 12)}${source.dirty ? " · есть изменения" : ""}`}`,
      ]),
    ];
    const signature = JSON.stringify(rows);
    if (list.dataset.signature === signature) return;
    list.dataset.signature = signature;
    list.replaceChildren(
      ...rows.flatMap(([term, value]) => [
        Object.assign(document.createElement("dt"), { textContent: term }),
        Object.assign(document.createElement("dd"), { textContent: value }),
      ]),
    );
  }

  function render() {
    const overview = state.overview;
    if (!overview) return;
    const job = state.job ?? overview.job;
    renderHero(overview, job);
    renderBlockers(overview.blockers || []);
    renderProgress(job);
    renderReleases(overview);
    renderVersions(overview);
    renderBackups(overview.backups || []);
    renderTech(overview);
    const blocked = Boolean(overview.blockers?.length) || Boolean(overview.running);
    ctx.$("#upd-version-install").disabled = blocked || !ctx.$("#upd-version").value;
    document.querySelectorAll("#upd-backups button").forEach((button) => (button.disabled = blocked));
  }

  // Один цикл опроса: во время работы службы — ход раз в секунду, иначе обзор раз в 10 секунд.
  async function poll(force = false) {
    if (state.busy || !ctx.adminToken) return;
    const visible = !document.querySelector("#admin-updates")?.hidden;
    if (!visible && !state.watching) return;
    state.busy = true;
    try {
      if (state.watching) {
        const progress = await ctx.adminRequest("/api/admin/updates/progress");
        state.failures = 0;
        ctx.$("#upd-reconnect").hidden = true;
        state.job = progress.job;
        if (state.overview) state.overview.running = progress.running;
        if (!progress.running && finished.includes(progress.job?.phase)) {
          state.watching = false;
          ctx.updateRunning = false;
          force = true;
        }
      }
      if (force || (!state.watching && Date.now() - state.lastOverview > 10000)) {
        state.overview = await ctx.adminRequest("/api/admin/updates");
        state.lastOverview = Date.now();
        state.job = state.overview.job;
        if (state.overview.running && !state.watching) {
          state.watching = true;
          ctx.updateRunning = true;
        }
      }
      render();
    } catch (error) {
      state.failures++;
      // Перезапуск панели — ожидаемая пауза: показываем экран ожидания, а не ошибку.
      if (state.watching) ctx.$("#upd-reconnect").hidden = false;
      else if (state.failures === 1) ctx.showNotice(`Обновления: ${error.message}`, "error");
    } finally {
      state.busy = false;
    }
  }

  async function start(path, body, title, message, accept) {
    if (!(await ctx.confirmAction(title, message, { accept, tone: "primary" }))) return;
    try {
      const result = await ctx.adminRequest(path, { method: "POST", body: JSON.stringify(body) });
      ctx.showNotice(result.message);
      state.watching = true;
      ctx.updateRunning = true;
      state.job = { phase: "checking", step: "prepare", progress: 1, action: body.backup ? "rollback" : "install", version: body.version || "", message: "Служба обновления запускается…", startedAt: new Date().toISOString(), events: [] };
      if (state.overview) state.overview.running = true;
      render();
      ctx.$("#upd-progress").scrollIntoView({ behavior: reduced() ? "auto" : "smooth", block: "center" });
    } catch (error) {
      ctx.showNotice(error.message, "error");
    }
  }

  function install(version) {
    const installed = state.overview?.installed?.version || "текущая";
    start(
      "/api/admin/updates/install",
      { version },
      `Установить ${version}?`,
      `Сейчас работает ${installed}. Она сохранится в резервную копию, панель перезапустится на ${version}. Если новая версия не запустится, вернётся прежняя.`,
      "Установить",
    );
  }

  function rollback(backup) {
    start(
      "/api/admin/updates/rollback",
      { backup: backup.name },
      `Вернуть ${backup.version || backup.name}?`,
      "Текущая версия тоже сохранится, так что откат можно будет отменить. Панель перезапустится.",
      "Вернуть",
    );
  }

  // Совместимость с общим обновлением админки: оно вызывает этот метод каждую секунду.
  async function refreshUpdates() {
    await poll();
  }

  function bindUpdatesCheck() {
    const button = ctx.$("#upd-check");
    button.addEventListener("click", async () => {
      button.disabled = true;
      try {
        await ctx.adminRequest("/api/admin/updates/check", { method: "POST" });
        setTimeout(() => poll(true), 2500);
        await poll(true);
      } catch (error) {
        ctx.showNotice(error.message, "error");
      } finally {
        button.disabled = false;
      }
    });
  }

  function bindUpdatesInstall() {
    ctx.$("#upd-install").addEventListener("click", () => {
      const version = state.overview?.latest?.version;
      if (version) install(version);
    });
    ctx.$("#upd-version-install").addEventListener("click", () => {
      const version = ctx.$("#upd-version").value;
      if (version) install(version);
    });
    ctx.$("#upd-version").addEventListener("change", render);
    ctx.$("#upd-releases-more").addEventListener("click", () => {
      state.showAllReleases = true;
      render();
    });
    // Свой таймер продолжает опрос, когда общее обновление админки падает из-за перезапуска.
    setInterval(() => poll(), 1000);
  }

  return { refreshUpdates, bindUpdatesCheck, bindUpdatesInstall };
}
