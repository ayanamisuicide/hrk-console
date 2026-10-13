// Журнал: правила для групп записей (скрыть, уведомлять о повторах), уровни журнала модулей
// и подпись об архиве.
import { el, plural } from "./journal-format.js";

const LEVEL_LABELS = { DEBUG: "подробно", INFO: "обычно", WARNING: "только предупреждения", ERROR: "только ошибки" };

export function createJournalRules(J) {
  const { ctx, S, $ } = J;
  // Части журнала вызывают друг друга через J: функции других частей появляются в нём
  // при сборке журнала, а вызываются уже после неё.

  /* ---------- Правила, уровни модулей, архив ---------- */

  const canControl = () => ctx.role !== "viewer";
  const ruleFor = (key) => S.rules.find((rule) => rule.key === key);
  const hiddenRules = () => S.rules.filter((rule) => rule.hide);

  async function postJSON(path, body) {
    return ctx.request(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
  }

  async function loadRules() {
    try {
      const data = await ctx.request("/api/logs/rules");
      S.rules = data.rules || [];
    } catch (_) {
      /* Без правил лента показывается целиком. */
    }
    renderControls();
  }

  async function loadLevels() {
    try {
      S.levels = await ctx.request("/api/logs/levels");
    } catch (_) {
      S.levels = { available: false, reason: "Уровни недоступны", levels: [] };
    }
    renderControls();
  }

  async function loadArchive() {
    try {
      const data = await ctx.request("/api/logs/archive");
      const span = $("#archive-span");
      if (data.enabled && data.firstTs) {
        const since = new Date(data.firstTs).toLocaleDateString("ru-RU", { day: "numeric", month: "long" });
        span.textContent = `· история с ${since}`;
        span.title = `Сжатый архив журнала: ${data.files} ${plural(data.files, "копия", "копии", "копий")}, ${(data.bytes / 1048576).toFixed(1)} МБ из ${Math.round(data.limitBytes / 1048576)} МБ, не старше ${data.limitDays} дн.`;
        span.hidden = false;
      } else span.hidden = true;
    } catch (_) {
      /* Подпись об архиве необязательна. */
    }
  }

  // Сохраняет правило группы и перечитывает ленту: скрытое пропадает из записей и счётчиков сразу.
  async function saveRule(body, notice) {
    try {
      const data = await postJSON("/api/logs/rules", body);
      S.rules = data.rules || [];
      renderControls();
      if (notice) ctx.showNotice(notice);
      J.queryChanged();
      return data.rule;
    } catch (error) {
      ctx.showNotice(error.message, "error");
      return null;
    }
  }

  async function removeRule(rule) {
    try {
      const data = await ctx.request(`/api/logs/rules/${encodeURIComponent(rule.id)}`, { method: "DELETE" });
      S.rules = data.rules || [];
      renderControls();
      J.queryChanged();
    } catch (error) {
      ctx.showNotice(error.message, "error");
    }
  }

  async function setLevel(logger, level, minutes) {
    try {
      const result = await postJSON("/api/logs/levels", { logger, level, minutes });
      ctx.showNotice(result.message);
    } catch (error) {
      ctx.showNotice(error.message, "error");
    }
    await loadLevels();
    // Мост в процессе бота применяет уровень раз в секунду: через пару секунд видно «применён».
    setTimeout(loadLevels, 1600);
  }

  const untilText = (until) => (until ? `до ${new Date(until * 1000).toTimeString().slice(0, 5)}` : "до отмены");

  // Под фильтрами: переключатель «Скрытые» и строка переопределённых уровней модулей.
  function renderControls() {
    const toggle = $("#hidden-toggle");
    const hidden = hiddenRules();
    toggle.hidden = hidden.length === 0 && !S.showHidden;
    toggle.textContent = `${S.showHidden ? "Скрытые показаны" : "Скрытые"} · ${hidden.length}`;
    toggle.setAttribute("aria-pressed", String(S.showHidden));
    toggle.title = hidden.length
      ? `Правила скрывают: ${hidden.map((rule) => `${rule.module || "—"}: ${rule.label}`).join("; ")}`
      : "Скрывающих правил нет";
    const row = $("#jr-levels");
    row.replaceChildren();
    const levels = S.levels?.levels || [];
    if (levels.length) {
      row.append(el("span", "jr-levels-title", "Уровни модулей"));
      for (const item of levels) {
        const chip = el("span", `jr-level-chip${item.applied ? "" : " pending"}`);
        chip.title = item.applied ? "Применён в процессе бота" : S.levels.reason || "Ждёт применения в процессе бота";
        chip.append(el("b", "", item.logger), document.createTextNode(` ${LEVEL_LABELS[item.level] || item.level} · ${untilText(item.until)}`));
        if (canControl()) {
          const x = el("button", "", "×");
          x.type = "button";
          x.setAttribute("aria-label", `Вернуть уровень ${item.logger} по умолчанию`);
          x.addEventListener("click", () => setLevel(item.logger, "", 0));
          chip.append(x);
        }
        row.append(chip);
      }
      if (!S.levels.available && S.levels.reason) row.append(el("span", "jr-levels-note", S.levels.reason));
    }
    row.hidden = !levels.length;
  }

  // Блок «Уровень журнала модуля» в деталях записи: подробнее на время или тише до отмены.
  function levelSection(module) {
    const box = el("div", "jr-lvl");
    box.append(el("div", "jr-ctx-title", "Уровень журнала модуля"));
    const current = (S.levels?.levels || []).find((item) => item.logger === module);
    box.append(el("p", "jr-lvl-note", current
      ? `${module}: ${LEVEL_LABELS[current.level] || current.level} · ${untilText(current.until)}${current.applied ? "" : " · ждёт применения"}`
      : `${module} пишет в журнал как настроено в боте. Можно включить подробный журнал на время или приглушить шум.`));
    if (!canControl()) return box;
    const level = el("select");
    level.setAttribute("aria-label", "Уровень");
    for (const [value, label] of [["DEBUG", "Подробно (DEBUG)"], ["INFO", "Обычно (INFO)"], ["WARNING", "Только предупреждения"], ["ERROR", "Только ошибки"]]) level.add(new Option(label, value));
    level.value = current?.level || "DEBUG";
    const span = el("select");
    span.setAttribute("aria-label", "Срок");
    for (const [value, label] of [["15", "на 15 минут"], ["60", "на час"], ["1440", "на сутки"], ["0", "до отмены"]]) span.add(new Option(label, value));
    const apply = el("button", "compact", "Применить");
    apply.type = "button";
    apply.addEventListener("click", () => setLevel(module, level.value, Number(span.value)).then(() => S.selected && J.reopenSelected()));
    const row = el("div", "jr-lvl-row");
    row.append(level, span, apply);
    if (current) {
      const reset = el("button", "compact", "По умолчанию");
      reset.type = "button";
      reset.addEventListener("click", () => setLevel(module, "", 0).then(() => S.selected && J.reopenSelected()));
      row.append(reset);
    }
    box.append(row);
    if (S.levels && !S.levels.available && S.levels.reason) box.append(el("p", "jr-lvl-note warn", S.levels.reason));
    return box;
  }

  // Строка правил в «Частых ошибках»: скрыть или показать группу и уведомлять о повторах.
  function ruleControls(problem) {
    const box = el("div", "jr-rule");
    const rule = ruleFor(problem.key);
    if (rule?.hide) box.append(el("span", "jr-rule-badge", "скрыто"));
    if (rule?.notify) box.append(el("span", "jr-rule-badge bell", `уведомлять: ${rule.notify.count} за ${rule.notify.minutes} мин`));
    if (!canControl()) return box;
    const hide = el("button", "jr-link", rule?.hide ? "Показать" : "Скрыть");
    hide.type = "button";
    hide.title = rule?.hide ? "Вернуть такие записи в ленту и счётчики" : "Убрать такие записи из ленты, счётчиков и Telegram";
    hide.addEventListener("click", (event) => {
      event.stopPropagation();
      saveRule({ key: problem.key, hide: !rule?.hide, notify: rule?.notify || null }, rule?.hide ? "Записи снова видны" : "Такие записи скрыты. Показать их — кнопка «Скрытые» под поиском").then(() => S.drawerMode === "problems" && J.openProblems());
    });
    const bell = el("button", "jr-link", rule?.notify ? "Уведомления…" : "Уведомлять…");
    bell.type = "button";
    bell.addEventListener("click", (event) => {
      event.stopPropagation();
      const open = box.querySelector(".jr-rule-form");
      if (open) open.remove();
      else box.append(notifyForm(problem, rule));
    });
    box.append(hide, bell);
    return box;
  }

  // Все правила журнала — в том числе для групп, которых нет в текущем окне.
  function rulesList() {
    const box = el("details", "jr-rules");
    box.append(el("summary", "", `Правила журнала · ${S.rules.length}`));
    for (const rule of S.rules) {
      const row = el("div", "jr-rules-row");
      const text = el("span", "jr-rules-text");
      text.append(el("b", "", rule.module || "—"), document.createTextNode(` ${rule.label}`));
      const badges = [rule.hide ? "скрыто" : "", rule.notify ? `уведомлять: ${rule.notify.count} за ${rule.notify.minutes} мин` : ""].filter(Boolean).join(" · ");
      row.append(text, el("small", "", badges));
      if (canControl()) {
        const x = el("button", "jr-x", "×");
        x.type = "button";
        x.title = "Удалить правило";
        x.setAttribute("aria-label", "Удалить правило");
        x.addEventListener("click", () => removeRule(rule).then(() => J.openProblems()));
        row.append(x);
      }
      box.append(row);
    }
    return box;
  }

  function notifyForm(problem, rule) {
    const form = el("form", "jr-rule-form");
    form.addEventListener("click", (event) => event.stopPropagation());
    const count = el("input");
    count.type = "number";
    count.min = "1";
    count.max = "1000";
    count.value = String(rule?.notify?.count || 5);
    count.setAttribute("aria-label", "Сколько раз");
    const minutes = el("input");
    minutes.type = "number";
    minutes.min = "1";
    minutes.max = "1440";
    minutes.value = String(rule?.notify?.minutes || 10);
    minutes.setAttribute("aria-label", "За сколько минут");
    const save = el("button", "compact primary", "Сохранить");
    save.type = "submit";
    form.append("Если повторится", count, "раз за", minutes, "мин", save);
    if (rule?.notify) {
      const off = el("button", "compact", "Выключить");
      off.type = "button";
      off.addEventListener("click", () => saveRule({ key: problem.key, hide: !!rule.hide, notify: null }, "Уведомление выключено").then(() => J.openProblems()));
      form.append(off);
    }
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      saveRule({ key: problem.key, hide: !!rule?.hide, notify: { count: Number(count.value), minutes: Number(minutes.value) } }, "Уведомление включено: придёт туда же, куда остальные уведомления панели").then(() => J.openProblems());
    });
    return form;
  }

  return { canControl, loadRules, loadLevels, loadArchive, saveRule, renderControls, levelSection, ruleControls, rulesList };
}
