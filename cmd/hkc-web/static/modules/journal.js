// История и поток журнала, фильтры, закладки и экспорт. Исходный файл бота не изменяется.
// Фабрика возвращает функции раздела; состояние и зависимости берёт из ctx.
// Подписки вызываются точкой входа после заполнения состояния страницы.
export function createJournal(ctx) {
  // Вычисляет короткий стабильный отпечаток строки для закладок. Это не криптографический хеш; совпадающие
  // строки имеют один ключ.
  function lineKey(raw) {
    let hash = 2166136261;
    for (let i = 0; i < raw.length; i++)
      hash = Math.imul(hash ^ raw.charCodeAt(i), 16777619);
    return (hash >>> 0).toString(36);
  }

  // Извлекает модуль из стандартного префикса журнала; продолжение сообщения даёт пустую строку.
  function lineModule(raw) {
    return (
      raw.match(
        /^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \[[A-Z]+\] ([^:]+):/,
      )?.[1] || ""
    );
  }

  // Проверяет модуль и локальные границы времени; строки без даты не проходят временной фильтр.
  function matchesAdvanced(raw) {
    const module = ctx.$("#module-filter").value;
    if (module && ctx.lineModule(raw) !== module) return false;
    const match = raw.match(/^(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2})/);
    const from = ctx.$("#time-from").value;
    const to = ctx.$("#time-to").value;
    if (from || to) {
      if (!match) return false;
      const stamp = `${match[1]}T${match[2]}`;
      if (from && stamp < from) return false;
      if (to && stamp > `${to}:59`) return false;
    }
    return true;
  }

  // Перестраивает список модулей из загруженных строк, сохраняя допустимый выбор.
  function refreshModuleOptions() {
    const select = ctx.$("#module-filter");
    const selected = select.value;
    const modules = [
      ...new Set(ctx.allLines.map(ctx.lineModule).filter(Boolean)),
    ].sort();
    select.replaceChildren(new Option("Все модули", ""));
    for (const module of modules) select.add(new Option(module, module));
    if (modules.includes(selected)) select.value = selected;
  }

  // Перестраивает сохранённые фильтры и доступность удаления выбранного набора.
  function refreshPresetOptions() {
    const select = ctx.$("#preset-select");
    const selected = select.value;
    select.replaceChildren(new Option("Выберите", ""));
    for (const name of Object.keys(ctx.savedFilters).sort())
      select.add(new Option(name, name));
    if (ctx.savedFilters[selected]) select.value = selected;
    ctx.$("#preset-delete").disabled = !select.value;
  }

  // Извлекает уровень журнала; строки без уровня относятся к OTHER.
  function lineLevel(raw) {
    return raw.match(/\[([A-Z]+)\]/)?.[1] || "OTHER";
  }

  // Проверяет выбранный уровень; фильтр ERROR включает также CRITICAL.
  function matchesLevel(raw) {
    if (ctx.activeLevel === "ALL") return true;
    const level = ctx.lineLevel(raw);
    return ctx.activeLevel === "ERROR"
      ? level === "ERROR" || level === "CRITICAL"
      : level === ctx.activeLevel;
  }

  // Применяет все фильтры к истории и заменяет видимую ленту одним фрагментом DOM.
  function renderLines() {
    const query = ctx.filterInput.value.trim().toLowerCase();
    const visible = ctx.allLines.filter(
      (line) =>
        ctx.matchesLevel(line) &&
        ctx.matchesAdvanced(line) &&
        (!query || line.toLowerCase().includes(query)) &&
        (!ctx.bookmarksOnly || ctx.bookmarks.has(ctx.lineKey(line))),
    );
    const fragment = document.createDocumentFragment();
    for (const raw of visible) fragment.append(ctx.createLine(raw));
    if (!visible.length) fragment.append(ctx.createLogEmpty());
    ctx.logEl.replaceChildren(fragment);
    ctx.animateValue(ctx.$("#line-count"), visible.length);
    ctx.$("#export-logs").disabled = visible.length === 0;
    ctx.$("#clear").disabled =
      ctx.allLines.length === 0 && ctx.pausedLines.length === 0;
    if (ctx.autoscroll.checked) ctx.logEl.scrollTop = ctx.logEl.scrollHeight;
  }

  // Создаёт разное пояснение для пустого журнала, пустого поиска и ручной очистки экрана.
  function createLogEmpty() {
    const state = document.createElement("div");
    state.className = "log-empty";
    const icon = document.createElement("span");
    icon.textContent = ctx.logClearedByUser ? "✓" : "⌁";
    const title = document.createElement("strong");
    const detail = document.createElement("small");
    if (ctx.logClearedByUser) {
      title.textContent = "Журнал очищен";
      detail.textContent = "Новые события появятся здесь автоматически.";
    } else if (ctx.allLines.length) {
      title.textContent = "Ничего не найдено";
      detail.textContent = "Измените поиск или выбранные фильтры.";
    } else {
      title.textContent = "Событий пока нет";
      detail.textContent =
        "Поток подключён — новые записи появятся автоматически.";
    }
    state.append(icon, title, detail);
    return state;
  }
  // Разбирает стандартную строку в безопасные текстовые элементы, добавляет закладку и анимацию только нового
  // события.
  function createLine(raw, live = false) {
    const row = document.createElement("div");
    const motionReduced =
      window.prefersReducedMotion?.() ??
      window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
    const animateArrival =
      live && !motionReduced && document.visibilityState === "visible";
    row.className = `line${animateArrival ? " live" : ""}`;
    if (animateArrival)
      row.addEventListener("animationend", (event) => {
        if (event.target === row && !event.pseudoElement)
          row.classList.remove("live");
      });
    const match = raw.match(
      /^(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2}) \[([A-Z]+)\] ([^:]+):\s?(.*)$/,
    );
    if (!match) {
      row.classList.add("continuation");
      row.textContent = raw;
      return row;
    }
    const [, date, time, level, module, message] = match;
    row.dataset.level = level;
    if (ctx.timestamps.checked) {
      const timeEl = document.createElement("time");
      timeEl.dateTime = `${date}T${time}`;
      timeEl.textContent = time;
      row.append(timeEl);
    }
    const levelEl = document.createElement("span");
    levelEl.className = "level";
    levelEl.textContent = level;
    const moduleEl = document.createElement("span");
    moduleEl.className = "module";
    moduleEl.textContent = module;
    const messageEl = document.createElement("span");
    messageEl.className = "message";
    messageEl.textContent = message;
    const mark = document.createElement("button");
    mark.type = "button";
    mark.className = "line-bookmark";
    mark.textContent = ctx.bookmarks.has(ctx.lineKey(raw)) ? "★" : "☆";
    mark.title = "Отметить строку";
    mark.setAttribute("aria-label", "Отметить строку");
    mark.setAttribute(
      "aria-pressed",
      String(ctx.bookmarks.has(ctx.lineKey(raw))),
    );
    mark.addEventListener("click", () => {
      const key = ctx.lineKey(raw);
      if (ctx.bookmarks.has(key)) ctx.bookmarks.delete(key);
      else ctx.bookmarks.add(key);
      localStorage.setItem(
        "hkc-log-bookmarks",
        JSON.stringify([...ctx.bookmarks].slice(-500)),
      );
      mark.textContent = ctx.bookmarks.has(key) ? "★" : "☆";
      mark.setAttribute("aria-pressed", String(ctx.bookmarks.has(key)));
      if (ctx.bookmarksOnly) ctx.renderLines();
    });
    row.append(levelEl, moduleEl, messageEl, mark);
    return row;
  }
  // При паузе накапливает строки отдельно; иначе добавляет событие, ограничивает историю и учитывает текущие
  // фильтры.
  function appendLiveLine(raw) {
    if (ctx.streamPaused) {
      ctx.pausedLines.push(raw);
      ctx.$("#pause-stream").innerHTML =
        `<span>▶</span> Продолжить · ${ctx.pausedLines.length}`;
      ctx.$("#clear").disabled = false;
      return;
    }
    ctx.logClearedByUser = false;
    ctx.allLines.push(raw);
    ctx.$("#clear").disabled = false;
    const module = ctx.lineModule(raw);
    if (
      module &&
      ![...ctx.$("#module-filter").options].some(
        (option) => option.value === module,
      )
    )
      ctx.$("#module-filter").add(new Option(module, module));
    let trimmed = false;
    if (ctx.allLines.length > ctx.maxLines) {
      ctx.allLines.splice(0, ctx.allLines.length - ctx.maxLines);
      trimmed = true;
    }
    const query = ctx.filterInput.value.trim().toLowerCase();
    if (trimmed && query) {
      ctx.renderLines();
      return;
    }
    if (trimmed && ctx.logEl.firstChild) ctx.logEl.firstChild.remove();
    if (
      !ctx.matchesLevel(raw) ||
      !ctx.matchesAdvanced(raw) ||
      (query && !raw.toLowerCase().includes(query)) ||
      (ctx.bookmarksOnly && !ctx.bookmarks.has(ctx.lineKey(raw)))
    ) {
      if (ctx.logEl.querySelector(".log-empty")) ctx.renderLines();
      return;
    }
    ctx.logEl.querySelector(".log-empty")?.remove();
    ctx.logEl.append(ctx.createLine(raw, true));
    const visibleCount = ctx.logEl.querySelectorAll(".line").length;
    ctx.animateValue(ctx.$("#line-count"), visibleCount);
    ctx.$("#export-logs").disabled = false;
    ctx.$("#clear").disabled = false;
    if (ctx.autoscroll.checked) ctx.logEl.scrollTop = ctx.logEl.scrollHeight;
  }

  // Получает начальный хвост журнала, обновляет модули и рисует ленту.
  async function loadHistory() {
    const data = await ctx.request("/api/logs?limit=800");
    ctx.logClearedByUser = false;
    ctx.allLines = data.lines || [];
    ctx.refreshModuleOptions();
    ctx.renderLines();
  }

  // Закрывает прежний EventSource и подключает SSE; автоматическое переподключение выполняет браузер.
  function connectStream() {
    if (ctx.stream) ctx.stream.close();
    ctx.stream = new EventSource("/api/events");
    ctx.stream.onopen = () => {
      ctx.$("#stream-state").innerHTML = "<i></i> поток подключён";
      ctx.$("#stream-state").classList.add("online");
    };
    ctx.stream.onmessage = (event) => {
      ctx.appendLiveLine(JSON.parse(event.data));
    };
    ctx.stream.onerror = () => {
      ctx.$("#stream-state").innerHTML = "<i></i> переподключение…";
      ctx.$("#stream-state").classList.remove("online");
    };
  }
  // Меняет фильтр уровня и активную кнопку, затем заново рисует строки.
  function bindFilterChip() {
    document.querySelectorAll(".filter-chip").forEach((button) => {
      button.addEventListener("click", () => {
        ctx.activeLevel = button.dataset.level;
        document
          .querySelectorAll(".filter-chip")
          .forEach((item) => item.classList.toggle("active", item === button));
        ctx.renderLines();
      });
    });
  }

  // Приостанавливает только показ, а не соединение; при продолжении переносит накопленные строки в ленту.
  function bindPauseStream() {
    ctx.$("#pause-stream").addEventListener("click", () => {
      ctx.streamPaused = !ctx.streamPaused;
      ctx.$("#pause-stream").classList.toggle("active", ctx.streamPaused);
      if (!ctx.streamPaused) {
        const queued = ctx.pausedLines.splice(0);
        ctx.$("#pause-stream").innerHTML = "<span>Ⅱ</span> Пауза";
        queued.forEach(ctx.appendLiveLine);
      } else {
        ctx.$("#pause-stream").innerHTML = "<span>▶</span> Продолжить";
      }
    });
  }

  // Переключает показ только отмеченных строк и синхронизирует aria-pressed.
  function bindBookmarksOnly() {
    ctx.$("#bookmarks-only").addEventListener("click", (event) => {
      ctx.bookmarksOnly = !ctx.bookmarksOnly;
      event.currentTarget.setAttribute(
        "aria-pressed",
        String(ctx.bookmarksOnly),
      );
      ctx.renderLines();
    });
  }
  // Скачивает видимые по текущим фильтрам строки как текст; временный URL освобождается после запуска
  // скачивания.
  function bindExportLogs() {
    ctx.$("#export-logs").addEventListener("click", () => {
      const query = ctx.filterInput.value.trim().toLowerCase();
      const lines = ctx.allLines.filter(
        (line) =>
          ctx.matchesLevel(line) &&
          ctx.matchesAdvanced(line) &&
          (!query || line.toLowerCase().includes(query)) &&
          (!ctx.bookmarksOnly || ctx.bookmarks.has(ctx.lineKey(line))),
      );
      const blob = new Blob([lines.join("\n") + "\n"], {
        type: "text/plain;charset=utf-8",
      });
      const link = document.createElement("a");
      link.href = URL.createObjectURL(blob);
      link.download = `heroku-log-${new Date().toISOString().slice(0, 10)}.txt`;
      link.click();
      setTimeout(() => URL.revokeObjectURL(link.href), 1000);
    });
  }

  // Сохраняет поиск, уровень, модуль и время как именованный набор в localStorage.
  function bindPresetSave() {
    ctx.$("#preset-save").addEventListener("click", () => {
      const name = ctx.$("#preset-name").value.trim();
      if (!name) {
        ctx.showNotice("Введите имя фильтра", "error");
        return;
      }
      ctx.savedFilters[name] = {
        query: ctx.filterInput.value,
        level: ctx.activeLevel,
        module: ctx.$("#module-filter").value,
        from: ctx.$("#time-from").value,
        to: ctx.$("#time-to").value,
      };
      localStorage.setItem("hkc-log-presets", JSON.stringify(ctx.savedFilters));
      ctx.refreshPresetOptions();
      ctx.$("#preset-select").value = name;
      ctx.$("#preset-delete").disabled = false;
      ctx.$("#preset-name").value = "";
      ctx.showNotice(`Фильтр «${name}» сохранён`);
    });
  }

  // Восстанавливает выбранный набор фильтров и обновляет ленту.
  function bindPresetSelect() {
    ctx.$("#preset-select").addEventListener("change", () => {
      ctx.$("#preset-delete").disabled = !ctx.$("#preset-select").value;
      const preset = ctx.savedFilters[ctx.$("#preset-select").value];
      if (!preset) return;
      ctx.filterInput.value = preset.query || "";
      ctx.activeLevel = preset.level || "ALL";
      ctx.$("#module-filter").value = preset.module || "";
      ctx.$("#time-from").value = preset.from || "";
      ctx.$("#time-to").value = preset.to || "";
      document
        .querySelectorAll(".filter-chip")
        .forEach((item) =>
          item.classList.toggle(
            "active",
            item.dataset.level === ctx.activeLevel,
          ),
        );
      ctx.renderLines();
    });
  }

  // Удаляет выбранный набор из локального хранилища и списка.
  function bindPresetDelete() {
    ctx.$("#preset-delete").addEventListener("click", () => {
      const name = ctx.$("#preset-select").value;
      if (!name) return;
      delete ctx.savedFilters[name];
      localStorage.setItem("hkc-log-presets", JSON.stringify(ctx.savedFilters));
      ctx.refreshPresetOptions();
      ctx.showNotice(`Фильтр «${name}» удалён`);
    });
  }

  // Очищает только историю на экране и буфер паузы; серверный файл журнала не удаляется.
  function bindClear() {
    ctx.$("#clear").addEventListener("click", () => {
      ctx.allLines = [];
      ctx.pausedLines = [];
      ctx.logClearedByUser = true;
      if (ctx.streamPaused)
        ctx.$("#pause-stream").innerHTML = "<span>▶</span> Продолжить";
      ctx.renderLines();
      ctx.showNotice("Экран журнала очищен");
    });
  }

  // Подключает перерисовку журнала к вводу поисковой строки.
  function bindSearchKeyboard() {
    ctx.filterInput.addEventListener("input", ctx.renderLines);
  }

  // Перерисовывает строки при смене видимости времени.
  function bindTimestamps() {
    ctx.timestamps.addEventListener("change", ctx.renderLines);
  }

  return {
    lineKey,
    lineModule,
    matchesAdvanced,
    refreshModuleOptions,
    refreshPresetOptions,
    lineLevel,
    matchesLevel,
    renderLines,
    createLogEmpty,
    createLine,
    appendLiveLine,
    loadHistory,
    connectStream,
    bindFilterChip,
    bindPauseStream,
    bindBookmarksOnly,
    bindExportLogs,
    bindPresetSave,
    bindPresetSelect,
    bindPresetDelete,
    bindClear,
    bindSearchKeyboard,
    bindTimestamps,
  };
}
