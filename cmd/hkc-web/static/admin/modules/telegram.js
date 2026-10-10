// Раздел «Telegram»: состояние бота, пошаговая настройка, команды и действия из Telegram.
// Секреты сюда не приходят: сервер сообщает только, задано ли значение.
export function createTelegram(ctx) {
  let signature = "";
  let saving = false;

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
        ctx.showNotice("Не удалось скопировать — выделите строку вручную", "error");
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
    const list = ctx.$("#tg-steps");
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
    ctx.$("#tg-progress").textContent = required.every((step) => step.done)
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
    const block = ctx.$("#tg-strangers");
    block.hidden = !unknown.length;
    const list = ctx.$("#tg-strangers-list");
    list.replaceChildren();
    for (const sender of unknown) {
      const row = document.createElement("div");
      row.className = "tg-stranger";
      const who = document.createElement("span");
      const name = document.createElement("strong");
      name.textContent = sender.name + (sender.username ? ` @${sender.username}` : "");
      const meta = document.createElement("small");
      meta.textContent = `ID ${sender.id} · ${ctx.formatDate(sender.at)}`;
      who.append(name, meta);
      row.append(who, copyButton(`HKC_TELEGRAM_ADMIN_IDS="${sender.id}"`));
      list.append(row);
    }
  }

  function renderCommands(data) {
    const list = ctx.$("#tg-commands");
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

  function renderActivity(events) {
    const list = ctx.$("#tg-activity");
    list.replaceChildren();
    ctx.$("#tg-activity-empty").hidden = events.length > 0;
    const labels = { "bot.start": "Запуск", "bot.stop": "Остановка", "bot.restart": "Перезапуск", "update.install": "Обновление панели", "telegram.settings": "Настройки Telegram" };
    for (const event of events) {
      const row = document.createElement("div");
      row.className = "tg-event";
      row.dataset.action = event.action;
      const dot = document.createElement("i");
      const main = document.createElement("span");
      const title = document.createElement("strong");
      title.textContent = labels[event.action] || event.action;
      const detail = document.createElement("small");
      const actor = event.actor.startsWith("telegram:") ? event.actor.slice(9) : "администратор панели";
      let text = event.detail;
      if (event.action === "telegram.settings") {
        const flags = Object.fromEntries(event.detail.split(" ").map((pair) => pair.split("=")));
        text = `управление ${flags.control === "true" ? "включено" : "выключено"}, подтверждение ${flags.confirm === "true" ? "включено" : "выключено"}`;
      }
      detail.textContent = `${actor} · ${text}`;
      main.append(title, detail);
      const time = document.createElement("time");
      time.textContent = ctx.formatDate(event.time);
      row.append(dot, main, time);
      list.append(row);
    }
  }

  function renderTelegram(data) {
    if (!data) return;
    const next = JSON.stringify({ ...data, lastPollAt: undefined });
    const hero = ctx.$("#tg-hero");
    // Время опроса меняется постоянно; остальное перерисовываем только при изменении.
    const poll = data.lastPollAt ? Math.max(0, Math.round((Date.now() - Date.parse(data.lastPollAt)) / 1000)) : null;
    const [title, subtitle] = heroes[data.state] || heroes.loading;
    let lead = subtitle;
    if (data.state === "running") lead = `Слушает команды${poll != null ? ` · последний ответ Telegram ${poll} с назад` : ""}.`;
    if (data.state === "error" || data.state === "conflict") lead = data.error;
    if (data.state === "disabled" && !(data.admins || []).length) lead = "Бот подключён. Включите управление и напишите ему /start — так вы узнаете свой ID.";
    if (ctx.$("#tg-subtitle").textContent !== lead) ctx.$("#tg-subtitle").textContent = lead;
    if (next === signature) return;
    signature = next;

    hero.dataset.state = data.state;
    ctx.$("#tg-title").textContent = data.state === "running" && data.bot?.username ? `@${data.bot.username} на связи` : title;
    ctx.$("#tg-eyebrow").textContent = data.bot?.name ? `Telegram-бот · ${data.bot.name}` : "Telegram-бот";
    const chips = { notify: data.notifications, control: data.state === "running", app: data.state === "running" && Boolean(data.webApp) };
    for (const [key, active] of Object.entries(chips)) ctx.$(`[data-chip="${key}"]`).dataset.active = String(Boolean(active));
    const control = ctx.$("#tg-control");
    if (!saving) {
      control.checked = Boolean(data.settings?.controlEnabled);
      ctx.$("#tg-confirm").checked = Boolean(data.settings?.confirm);
    }
    control.disabled = !data.token;

    const name = data.bot?.name || "Бот панели";
    ctx.$("#tg-bot-name").textContent = name;
    ctx.$("#tg-avatar").textContent = name[0]?.toUpperCase() || "H";
    ctx.$("#tg-bot-status").textContent = data.bot?.username ? `@${data.bot.username}` : "бот";
    ctx.$("#tg-menu-button").hidden = !data.webApp;

    renderSteps(data);
    renderStrangers(data.unknown || []);
    renderCommands(data);
    renderActivity(data.activity || []);
  }

  async function saveTelegram(patch, message) {
    saving = true;
    try {
      const data = await ctx.adminRequest("/api/admin/telegram", {
        method: "PUT",
        body: JSON.stringify({
          controlEnabled: ctx.$("#tg-control").checked,
          confirm: ctx.$("#tg-confirm").checked,
          ...patch,
        }),
      });
      saving = false;
      signature = "";
      renderTelegram(data);
      ctx.showNotice(message);
    } catch (error) {
      saving = false;
      signature = "";
      ctx.showNotice(error.message, "error");
      await ctx.refresh();
    }
  }

  function bindTelegram() {
    ctx.$("#tg-control").addEventListener("change", async (event) => {
      const enabled = event.target.checked;
      if (
        enabled &&
        !(await ctx.confirmAction(
          "Включить управление из Telegram?",
          "Панель начнёт слушать бота. Если этим же ботом пользуется другая программа (например, инлайн-бот Heroku), обе начнут мешать друг другу — используйте отдельного бота. Команды выполняются только для ID из HKC_TELEGRAM_ADMIN_IDS.",
          { accept: "Включить", tone: "primary" },
        ))
      ) {
        event.target.checked = false;
        return;
      }
      await saveTelegram({ controlEnabled: enabled }, enabled ? "Управление из Telegram включено" : "Управление из Telegram выключено");
    });
    ctx.$("#tg-confirm").addEventListener("change", (event) =>
      saveTelegram({ confirm: event.target.checked }, event.target.checked ? "Опасные команды подтверждаются кнопкой" : "Команды выполняются сразу"),
    );
  }

  return { renderTelegram, bindTelegram };
}
