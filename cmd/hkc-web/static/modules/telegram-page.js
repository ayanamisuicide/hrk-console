// Страница «Telegram»: состояние бота, пошаговая настройка, команды, тишина, автовосстановление,
// обновление, сводка и действия из Telegram. Одна и та же в админке и в мини-приложении (раздел
// «Управление»): разметка, порядок карточек и запросы общие, отличается только способ входа.
// Секреты сюда не приходят: сервер сообщает только, задано ли значение.
//
// env: { root, request(path, options), notice(message, kind), confirm(title, text, options), formatDate(value) }.
export function createTelegramPage(env) {
  let signature = "";
  let saving = false;
  let data = null;
  const $ = (selector) => env.root.querySelector(selector);

  const heroes = {
    loading: ["Получаем состояние…", "Смотрим, что настроено на сервере."],
    off: ["Бот не подключён", "Создайте бота у @BotFather и добавьте токен на сервер — первый шаг ниже."],
    disabled: ["Управление выключено", "Бот подключён. Включите управление, чтобы отдавать команды из Telegram."],
    connecting: ["Подключаемся к Telegram…", "Знакомимся с ботом и публикуем меню команд."],
    running: ["На связи", "Бот слушает команды."],
    error: ["Нет связи с Telegram", ""],
    conflict: ["Бот занят другой программой", ""],
  };

  const copyButton = (text) => {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "compact tg-copy";
    button.textContent = "Копировать";
    button.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(text);
        button.textContent = "Скопировано";
        setTimeout(() => (button.textContent = "Копировать"), 1600);
      } catch {
        env.notice("Не удалось скопировать — выделите строку вручную", "error");
      }
    });
    return button;
  };

  const snippet = (text) => {
    const wrap = document.createElement("div");
    wrap.className = "tg-snippet";
    const code = document.createElement("code");
    code.textContent = text;
    wrap.append(code, copyButton(text));
    return wrap;
  };

  // Шаги настройки: каждый говорит, что уже сделано и что сделать дальше.
  function steps(data) {
    const admins = data.admins || [];
    return [
      {
        done: data.token,
        bad: Boolean(data.tokenIssue),
        title: "Бот",
        text: data.tokenIssue
          ? `${data.tokenIssue}.`
          : data.token
            ? data.bot?.username
              ? `Подключён @${data.bot.username}.`
              : "Токен задан."
            : "Создайте отдельного бота у @BotFather — не инлайн-бота Heroku: двоих слушателей у одного бота Telegram не допускает.",
        code: data.token ? "" : 'HKC_TELEGRAM_BOT_TOKEN="123456:ABC…"',
      },
      {
        done: admins.length > 0,
        bad: Boolean(data.adminsIssue),
        title: "Кто может управлять",
        text: admins.length
          ? `Доступ: ID ${admins.join(", ")}.${data.adminsIssue ? ` ${data.adminsIssue}.` : ""}`
          : "Включите управление и напишите боту /start — ваш ID появится ниже. Остальным бот не отвечает.",
        code: admins.length ? "" : 'HKC_TELEGRAM_ADMIN_IDS="ваш ID"',
      },
      {
        done: Boolean(data.webApp),
        bad: Boolean(data.webAppIssue),
        optional: true,
        title: "Мини-приложение",
        text: data.webAppIssue
          ? `${data.webAppIssue}.`
          : data.webApp
            ? `Открывается кнопкой «Панель» в чате: ${data.webApp}`
            : "Нужен HTTPS-адрес панели. Через Tailscale: tailscale serve --bg 8080 — и адрес *.ts.net откроется только на устройствах в вашей сети.",
        code: data.webApp ? "" : 'HKC_TELEGRAM_WEBAPP_URL="https://машина.tailnet.ts.net"',
      },
      {
        done: data.notifications,
        bad: Boolean(data.chatIssue),
        optional: true,
        title: "Уведомления",
        text: data.chatIssue
          ? `${data.chatIssue}.`
          : data.notifications
            ? "Сообщения о падениях, перезапусках и ресурсах приходят в чат."
            : "Куда присылать уведомления: ваш ID или ID группы. Что именно присылать — в разделе «Восстановление».",
        code: data.notifications ? "" : 'HKC_TELEGRAM_CHAT_ID="ваш ID"',
      },
    ];
  }

  function renderSteps(data) {
    const list = $("#tg-steps");
    list.replaceChildren();
    const all = steps(data);
    for (const [index, step] of all.entries()) {
      const item = document.createElement("li");
      item.dataset.state = step.bad ? "bad" : step.done ? "done" : "todo";
      const mark = document.createElement("span");
      mark.className = "tg-step-mark";
      mark.textContent = step.bad ? "!" : step.done ? "✓" : String(index + 1);
      const body = document.createElement("div");
      const title = document.createElement("strong");
      title.textContent = step.title;
      if (step.optional) {
        const tag = document.createElement("small");
        tag.className = "tg-optional";
        tag.textContent = "по желанию";
        title.append(" ", tag);
      }
      const text = document.createElement("p");
      text.textContent = step.text;
      body.append(title, text);
      if (step.code) body.append(snippet(step.code));
      item.append(mark, body);
      list.append(item);
    }
    const required = all.filter((step) => !step.optional);
    $("#tg-progress").textContent = required.every((step) => step.done)
      ? "Обязательное готово"
      : `${required.filter((step) => step.done).length} из ${required.length} обязательных`;
    if (all.some((step) => step.code)) {
      const hint = document.createElement("li");
      hint.className = "tg-hint";
      hint.textContent = "Строки добавляются в /etc/hkc/hkc.env, затем: sudo systemctl restart hkc-web";
      list.append(hint);
    }
  }

  function renderStrangers(unknown) {
    const block = $("#tg-strangers");
    block.hidden = !unknown.length;
    const list = $("#tg-strangers-list");
    list.replaceChildren();
    for (const sender of unknown) {
      const row = document.createElement("div");
      row.className = "tg-stranger";
      const who = document.createElement("span");
      const name = document.createElement("strong");
      name.textContent = sender.name + (sender.username ? ` @${sender.username}` : "");
      const meta = document.createElement("small");
      meta.textContent = `ID ${sender.id} · ${env.formatDate(sender.at)}`;
      who.append(name, meta);
      row.append(who, copyButton(`HKC_TELEGRAM_ADMIN_IDS="${sender.id}"`));
      list.append(row);
    }
  }

  function renderCommands(data) {
    const list = $("#tg-commands");
    list.replaceChildren();
    for (const command of data.commands || []) {
      if (command.name === "app" && !data.webApp) continue;
      const card = document.createElement("article");
      card.className = "tg-command";
      const name = document.createElement("code");
      name.textContent = `/${command.name}`;
      const text = document.createElement("span");
      text.textContent = command.description;
      card.append(name, text);
      if (command.confirm && data.settings?.confirm) {
        const badge = document.createElement("small");
        badge.textContent = "с подтверждением";
        card.append(badge);
      }
      list.append(card);
    }
  }

  const activityLabels = {
    "bot.start": "Запуск",
    "bot.stop": "Остановка",
    "bot.restart": "Перезапуск",
    "update.install": "Обновление панели",
    "telegram.settings": "Настройки Telegram",
    "telegram.mute": "Тишина",
    "telegram.unmute": "Уведомления включены",
    "watchdog.update": "Автовосстановление",
    "watchdog.resume": "Автовосстановление возобновлено",
    "log.share": "Запись журнала в чат",
    "log.rule": "Правило журнала",
  };

  function activityText(event) {
    const flags = Object.fromEntries((event.detail || "").split(" ").map((pair) => pair.split("=")));
    if (event.action === "telegram.settings")
      return `управление ${flags.control === "true" ? "включено" : "выключено"}, подтверждение ${flags.confirm === "true" ? "включено" : "выключено"}`;
    if (event.action === "watchdog.update") return flags.enabled === "true" ? "включено" : "выключено";
    if (event.action === "telegram.mute") return flags.all === "true" ? "обо всём, даже о падении" : "критичные приходят";
    return event.detail;
  }

  function renderActivity(events) {
    const list = $("#tg-activity");
    list.replaceChildren();
    $("#tg-activity-empty").hidden = events.length > 0;
    for (const event of events) {
      const row = document.createElement("div");
      row.className = "tg-event";
      row.dataset.action = event.action;
      const dot = document.createElement("i");
      const main = document.createElement("span");
      const title = document.createElement("strong");
      title.textContent = activityLabels[event.action] || event.action;
      const detail = document.createElement("small");
      const actor = event.actor.startsWith("telegram:") ? event.actor.slice(9) : "администратор панели";
      const text = activityText(event);
      detail.textContent = text ? `${actor} · ${text}` : actor;
      main.append(title, detail);
      const time = document.createElement("time");
      time.textContent = env.formatDate(event.time);
      row.append(dot, main, time);
      list.append(row);
    }
  }

  // Тишина: состояние, кнопки «на час / 3 часа / до утра», «даже о падении» и «включить».
  function renderMute(mute = {}) {
    const card = $("#tg-mute-card");
    const available = mute.available !== false;
    const state = $("#tg-mute-state");
    state.dataset.state = mute.active ? "recovering" : "healthy";
    state.textContent = mute.active ? `до ${mute.untilText}` : "Включены";
    $("#tg-mute-text").textContent = !available
      ? "Тишина работает, когда подключён Telegram-бот."
      : mute.active
        ? mute.all
          ? `Бот молчит обо всём до ${mute.untilText}, даже о падении.`
          : `Бот молчит до ${mute.untilText}. О падении бота сообщит всё равно.`
        : "Уведомления приходят. Тишина приглушит их на время: когда спите или чините бота.";
    $("#tg-unmute").hidden = !mute.active;
    if (!saving) $("#tg-mute-all").checked = Boolean(mute.all);
    card.querySelectorAll("button, input").forEach((control) => (control.disabled = !available));
  }

  // Автовосстановление: включено ли, что делает сейчас и кнопка возобновления после паузы.
  function renderWatchdog(watchdog = {}) {
    const available = watchdog.available !== false;
    const state = $("#tg-watchdog-state");
    const suspended = watchdog.state === "suspended";
    state.dataset.state = !watchdog.enabled ? "" : suspended ? "suspended" : watchdog.state === "healthy" ? "healthy" : "recovering";
    state.textContent = !available ? "Недоступно" : !watchdog.enabled ? "Выключено" : suspended ? "Пауза" : "Включено";
    if (!saving) $("#tg-watchdog").checked = Boolean(watchdog.enabled);
    $("#tg-watchdog").disabled = !available;
    $("#tg-watchdog-text").textContent = !available
      ? "Хранилище настроек недоступно."
      : watchdog.enabled
        ? watchdog.message || "Следит за процессом."
        : "Бот не поднимется сам после падения.";
    $("#tg-watchdog-resume").hidden = !suspended;
  }

  // Обновление панели: установленная версия, новая и кнопка установки.
  function renderUpdate(update = {}) {
    $("#tg-update-installed").textContent = update.installed ? `установлена ${update.installed}` : "";
    const button = $("#tg-update");
    button.hidden = !update.latest || update.running;
    button.textContent = update.latest ? `Обновить до ${update.latest}` : "Обновить";
    $("#tg-update-text").textContent = !update.available
      ? "Служба обновления не настроена на этом сервере."
      : update.running
        ? "Идёт установка: панель перезапустится и вернётся через минуту."
        : update.latest
          ? `Вышла ${update.latest}${update.note ? ` — ${update.note}` : ""}.`
          : "Установлена последняя версия.";
  }

  // Подпись под сводкой: когда пришла последняя и почему может не прийти.
  function renderDigestNote(data) {
    const note = $("#tg-digest-note");
    const base = "Что было с прошлой сводки: остановки и падения бота с причинами, ошибки и самые частые из них, нагрузка сервера, модули и новое. По запросу — фраза «сводка», «что было за ночь» или /digest.";
    let state;
    if (!data.digest?.enabled) state = "Утренняя сводка выключена.";
    else if (!data.settings?.controlEnabled) state = "Не придёт, пока выключено управление из Telegram.";
    else if (data.digest.lastSent) state = `Последняя пришла ${new Date(data.digest.lastSent).toLocaleString("ru-RU", { day: "numeric", month: "long", hour: "2-digit", minute: "2-digit" })}.`;
    else state = `Первая придёт в ${data.digest.at}.`;
    note.textContent = `${base} ${state}`;
  }

  function render(next) {
    if (!next) return;
    data = next;
    const hero = $("#tg-hero");
    // Время опроса меняется постоянно; остальное перерисовываем только при изменении.
    const poll = next.lastPollAt ? Math.max(0, Math.round((Date.now() - Date.parse(next.lastPollAt)) / 1000)) : null;
    const [title, subtitle] = heroes[next.state] || heroes.loading;
    let lead = subtitle;
    if (next.state === "running") lead = `Слушает команды${poll != null ? ` · последний ответ Telegram ${poll} с назад` : ""}.`;
    if (next.state === "error" || next.state === "conflict") lead = next.error;
    if (next.state === "disabled" && !(next.admins || []).length) lead = "Бот подключён. Включите управление и напишите ему /start — так вы узнаете свой ID.";
    if ($("#tg-subtitle").textContent !== lead) $("#tg-subtitle").textContent = lead;
    const changed = JSON.stringify({ ...next, lastPollAt: undefined });
    if (changed === signature) return;
    signature = changed;

    hero.dataset.state = next.state;
    $("#tg-title").textContent = next.state === "running" && next.bot?.username ? `@${next.bot.username} на связи` : title;
    $("#tg-eyebrow").textContent = next.bot?.name ? `Telegram-бот · ${next.bot.name}` : "Telegram-бот";
    const chips = { notify: next.notifications, control: next.state === "running", app: next.state === "running" && Boolean(next.webApp) };
    for (const [key, active] of Object.entries(chips)) $(`[data-chip="${key}"]`).dataset.active = String(Boolean(active));
    const control = $("#tg-control");
    if (!saving) {
      control.checked = Boolean(next.settings?.controlEnabled);
      $("#tg-confirm").checked = Boolean(next.settings?.confirm);
      $("#tg-digest").checked = Boolean(next.digest?.enabled);
      $("#tg-digest-at").value = next.digest?.at || "08:00";
    }
    // Из мини-приложения главный переключатель не меняется: выключив его, вы закрыли бы себе вход.
    const fromTelegram = Boolean(next.viewer?.telegram);
    control.disabled = !next.token || fromTelegram;
    $("#tg-master-note").hidden = !fromTelegram;
    renderDigestNote(next);

    const name = next.bot?.name || "Бот панели";
    $("#tg-bot-name").textContent = name;
    $("#tg-avatar").textContent = name[0]?.toUpperCase() || "H";
    $("#tg-bot-status").textContent = next.bot?.username ? `@${next.bot.username}` : "бот";
    $("#tg-menu-button").hidden = !next.webApp;

    renderSteps(next);
    renderStrangers(next.unknown || []);
    renderCommands(next);
    renderMute(next.mute);
    renderWatchdog(next.watchdog);
    renderUpdate(next.update);
    renderActivity(next.activity || []);
  }

  async function refresh() {
    try {
      render(await env.request("/api/telegram"));
    } catch (error) {
      env.notice(error.message, "error");
    }
  }

  // Запрос, меняющий настройки: ответ — новое состояние страницы.
  async function send(path, method, body, message) {
    saving = true;
    try {
      const next = await env.request(path, { method, body: JSON.stringify(body) });
      saving = false;
      signature = "";
      render(next);
      if (message) env.notice(message);
    } catch (error) {
      saving = false;
      signature = "";
      env.notice(error.message, "error");
      await refresh();
    }
  }

  const saveSettings = (patch, message) =>
    send(
      "/api/telegram",
      "PUT",
      { controlEnabled: Boolean(data?.settings?.controlEnabled), confirm: $("#tg-confirm").checked, ...patch },
      message,
    );

  function mount() {
    env.root.innerHTML = TEMPLATE;
    $("#tg-control").addEventListener("change", async (event) => {
      const enabled = event.target.checked;
      if (
        enabled &&
        !(await env.confirm(
          "Включить управление из Telegram?",
          "Панель начнёт слушать бота. Если этим же ботом пользуется другая программа (например, инлайн-бот Heroku), обе начнут мешать друг другу — используйте отдельного бота. Команды выполняются только для ID из HKC_TELEGRAM_ADMIN_IDS.",
          { accept: "Включить", tone: "primary" },
        ))
      ) {
        event.target.checked = false;
        return;
      }
      await saveSettings({ controlEnabled: enabled }, enabled ? "Управление из Telegram включено" : "Управление из Telegram выключено");
    });
    $("#tg-digest-form").addEventListener("submit", (event) => {
      event.preventDefault();
      const enabled = $("#tg-digest").checked;
      const at = $("#tg-digest-at").value || "08:00";
      saveSettings({ digestOff: !enabled, digestAt: at }, enabled ? `Сводка будет приходить в ${at}` : "Утренняя сводка выключена");
    });
    $("#tg-confirm").addEventListener("change", (event) =>
      saveSettings({ confirm: event.target.checked }, event.target.checked ? "Опасные команды подтверждаются кнопкой" : "Команды выполняются сразу"),
    );
    const muteLabels = { "1h": "на час", "3h": "на 3 часа", morning: "до утра" };
    env.root.querySelectorAll("[data-mute]").forEach((button) =>
      button.addEventListener("click", () =>
        send("/api/telegram/mute", "POST", { for: button.dataset.mute, all: $("#tg-mute-all").checked }, `Тишина ${muteLabels[button.dataset.mute]}`),
      ),
    );
    $("#tg-unmute").addEventListener("click", () => send("/api/telegram/mute", "POST", { off: true }, "Уведомления снова включены"));
    $("#tg-mute-all").addEventListener("change", (event) => {
      // Уже включённую тишину переключатель меняет сразу, до того же срока.
      if (!data?.mute?.active) return;
      const left = Date.parse(data.mute.until) - Date.now();
      if (left <= 0) return;
      const choice = left > 3 * 3600000 ? "morning" : left > 3600000 ? "3h" : "1h";
      send("/api/telegram/mute", "POST", { for: choice, all: event.target.checked }, event.target.checked ? "Тишина обо всём, даже о падении" : "О падении бота сообщу и в тишину");
    });
    $("#tg-watchdog").addEventListener("change", async (event) => {
      const enabled = event.target.checked;
      if (
        !enabled &&
        !(await env.confirm("Выключить автовосстановление?", "Если бот упадёт, сам он не поднимется — запускать его придётся вручную.", { accept: "Выключить" }))
      ) {
        event.target.checked = true;
        return;
      }
      send("/api/telegram/watchdog", "POST", { enabled }, enabled ? "Автовосстановление включено" : "Автовосстановление выключено");
    });
    $("#tg-watchdog-resume").addEventListener("click", () => send("/api/telegram/watchdog", "POST", { resume: true }, "Наблюдение возобновлено"));
    $("#tg-update").addEventListener("click", async () => {
      const version = data?.update?.latest;
      if (!version) return;
      const accepted = await env.confirm(
        `Обновить панель до ${version}?`,
        "Панель перезапустится и примерно минуту будет недоступна. Бот при этом продолжит работать.",
        { accept: "Обновить", tone: "primary" },
      );
      if (!accepted) return;
      try {
        const result = await env.request("/api/telegram/update", { method: "POST", body: JSON.stringify({ version }) });
        env.notice(result.message ? result.message[0].toUpperCase() + result.message.slice(1) : `Обновляю до ${version}`);
        env.onUpdateStarted?.(version);
        await refresh();
      } catch (error) {
        env.notice(error.message, "error");
      }
    });
  }

  return { mount, render, refresh };
}

const TEMPLATE = `
<section class="card tg-hero" id="tg-hero" data-state="loading">
  <div class="tg-emblem" aria-hidden="true">
    <span class="tg-orbit"></span><span class="tg-orbit"></span>
    <span class="tg-emblem-core"><svg viewBox="0 0 24 24"><path d="M21.5 4 2.8 11.3c-.8.3-.8 1.4 0 1.7l4.6 1.6 1.8 5.6c.2.7 1.1.9 1.6.4l2.7-2.6 4.6 3.4c.6.4 1.4.1 1.6-.6L22.8 5.3c.2-.9-.6-1.6-1.3-1.3z"/><path d="m7.4 14.6 10.1-7.1-7.7 8.4"/></svg></span>
  </div>
  <div class="tg-hero-text">
    <span class="eyebrow" id="tg-eyebrow">Telegram-бот</span>
    <h2 id="tg-title">Получаем состояние…</h2>
    <p id="tg-subtitle">Смотрим, что настроено на сервере.</p>
    <div class="tg-chips">
      <span class="tg-chip" data-chip="notify">Уведомления</span
      ><span class="tg-chip" data-chip="control">Команды</span
      ><span class="tg-chip" data-chip="app">Мини-приложение</span>
    </div>
  </div>
  <label class="watchdog-switch tg-master"
    ><input id="tg-control" type="checkbox" /><span
      ><strong>Управление из Telegram</strong
      ><small>Команды бота и мини-приложение</small
      ><small class="tg-readonly" id="tg-master-note" hidden>Включается и выключается в админке панели</small></span
    ></label
  >
</section>

<div class="tg-grid">
  <section class="card admin-section tg-setup">
    <div class="section-heading">
      <div>
        <span class="eyebrow">Настройка на сервере</span>
        <h2>Что нужно сделать</h2>
      </div>
      <span class="muted" id="tg-progress">—</span>
    </div>
    <ol class="tg-steps" id="tg-steps"></ol>
    <div class="tg-strangers" id="tg-strangers" hidden>
      <strong>Писали боту, но доступа нет</strong>
      <small>Если это вы — скопируйте строку в /etc/hkc/hkc.env и перезапустите панель.</small>
      <div id="tg-strangers-list"></div>
    </div>
  </section>

  <section class="card admin-section tg-preview-card">
    <div class="section-heading">
      <div>
        <span class="eyebrow">Так это выглядит</span>
        <h2>Чат с ботом</h2>
      </div>
    </div>
    <div class="tg-phone" id="tg-phone" aria-label="Пример переписки с ботом">
      <div class="tg-phone-bar">
        <span class="tg-avatar" id="tg-avatar">H</span>
        <span><strong id="tg-bot-name">Бот панели</strong><small id="tg-bot-status">бот</small></span>
      </div>
      <div class="tg-chat">
        <p class="tg-msg out"><span>/status</span></p>
        <p class="tg-msg in"><span><b>🟢 Бот работает</b><br />PID 4821 · 3ч 12м<br /><br />CPU 7% · память 41% · диск 63%<br />Автовосстановление: включено</span></p>
        <p class="tg-msg out"><span>/restart</span></p>
        <p class="tg-msg in"><span>Перезапустить бота?</span></p>
        <p class="tg-buttons"><span>🔄 Перезапустить</span><span>Отмена</span></p>
        <p class="tg-msg in"><span>✅ Бот перезапущен<br />PID 4907</span></p>
        <p class="tg-msg in"><span><b>🔵 Доступна hrk-console v2.12.0</b><br />Новая версия вышла</span></p>
        <p class="tg-buttons single"><span>⬆️ Обновить до v2.12.0</span></p>
        <p class="tg-typing" aria-hidden="true"><i></i><i></i><i></i></p>
      </div>
      <div class="tg-phone-input">
        <span class="tg-menu" id="tg-menu-button">Панель</span><span>Сообщение</span>
      </div>
    </div>
  </section>
</div>

<section class="card admin-section">
  <div class="section-heading">
    <div>
      <span class="eyebrow">Что умеет бот</span>
      <h2>Команды</h2>
    </div>
    <label class="watchdog-switch"
      ><input id="tg-confirm" type="checkbox" /><span
        ><strong>Подтверждать опасные</strong
        ><small>Остановка и перезапуск — по кнопке</small></span
      ></label
    >
  </div>
  <div class="tg-commands" id="tg-commands"></div>
  <p class="watchdog-note">
    Бот отвечает только ID из HKC_TELEGRAM_ADMIN_IDS, остальным молчит. Команды старше
    двух минут не выполняются: о том, что пришло, пока панель была выключена, бот сообщит
    и предложит выбор.
    Если мини-приложение недоступно, команды продолжают работать.
  </p>
</section>

<section class="card admin-section" id="tg-mute-card">
  <div class="section-heading">
    <div>
      <span class="eyebrow">Уведомления</span>
      <h2>Тишина</h2>
    </div>
    <span class="watchdog-state" id="tg-mute-state">—</span>
  </div>
  <p class="backup-note" id="tg-mute-text">—</p>
  <div class="tg-control-row">
    <div class="tg-mute-buttons" role="group" aria-label="Тишина на время">
      <button class="compact" type="button" data-mute="1h">На час</button
      ><button class="compact" type="button" data-mute="3h">На 3 часа</button
      ><button class="compact" type="button" data-mute="morning">До утра</button
      ><button class="compact" type="button" id="tg-unmute" hidden>Включить уведомления</button>
    </div>
    <label class="watchdog-switch"
      ><input id="tg-mute-all" type="checkbox" /><span
        ><strong>Даже о падении бота</strong
        ><small>Иначе критичные события приходят и в тишину</small></span
      ></label
    >
  </div>
</section>

<section class="card admin-section" id="tg-watchdog-card">
  <div class="section-heading">
    <div>
      <span class="eyebrow">Наблюдение</span>
      <h2>Автовосстановление</h2>
    </div>
    <span class="watchdog-state" id="tg-watchdog-state">—</span>
  </div>
  <div class="tg-control-row">
    <label class="watchdog-switch"
      ><input id="tg-watchdog" type="checkbox" /><span
        ><strong>Поднимать бота после падения</strong
        ><small id="tg-watchdog-text">—</small></span
      ></label
    ><button class="compact" type="button" id="tg-watchdog-resume" hidden>Возобновить</button>
  </div>
  <p class="watchdog-note">Сколько ждать и сколько раз пробовать — в админке, раздел «Восстановление».</p>
</section>

<section class="card admin-section" id="tg-update-card">
  <div class="section-heading">
    <div>
      <span class="eyebrow">Панель</span>
      <h2>Обновление</h2>
    </div>
    <span class="muted" id="tg-update-installed">—</span>
  </div>
  <div class="tg-control-row">
    <p class="backup-note" id="tg-update-text">—</p>
    <div class="tg-update-buttons"><button class="primary" type="button" id="tg-update" hidden>Обновить</button></div>
  </div>
</section>

<section class="card admin-section">
  <div class="section-heading">
    <div>
      <span class="eyebrow">Каждое утро</span>
      <h2>Сводка</h2>
    </div>
  </div>
  <form id="tg-digest-form" class="watchdog-form">
    <label class="watchdog-switch"
      ><input id="tg-digest" type="checkbox" /><span
        ><strong>Утренняя сводка</strong
        ><small>В личный чат с ботом</small></span
      ></label
    ><label
      >Время сервера<input id="tg-digest-at" type="time" step="60" value="08:00" required /></label
    ><button class="primary" type="submit">Сохранить</button>
  </form>
  <p class="watchdog-note" id="tg-digest-note">
    Что было с прошлой сводки: остановки и падения бота с причинами, ошибки и самые частые из них,
    нагрузка сервера, модули и новое. По запросу — фраза «сводка», «что было за ночь» или /digest.
    Приходит, только когда включено управление из Telegram.
  </p>
</section>

<section class="card admin-section">
  <div class="section-heading">
    <div>
      <span class="eyebrow">Журнал</span>
      <h2>Действия из Telegram</h2>
    </div>
  </div>
  <div class="tg-activity" id="tg-activity"></div>
  <p class="backup-note" id="tg-activity-empty">Из Telegram пока ничего не делали.</p>
</section>`;
