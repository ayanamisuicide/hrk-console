// Журнал: чистые функции для записей и карточек — ключи, группировка, даты, иконки уровней.
// Состояния раздела не касаются, поэтому общие для всех частей журнала.

// Окно шкалы и плиток по умолчанию.
export const WINDOW_MS = 6 * 3600 * 1000;

export const el = (tag, cls, text) => {
  const node = document.createElement(tag);
  if (cls) node.className = cls;
  if (text != null) node.textContent = text;
  return node;
};

export const entryKey = (e) => `${e.ts}|${e.id}`;

// Подряд идущие одинаковые записи без traceback показываются одной карточкой с счётчиком.
export const groupKey = (e) => (e.trace?.length ? null : `${e.level}\u0000${e.module}\u0000${e.message}`);

export function buildGroups(entries) {
  const groups = [];
  for (const e of entries) {
    const last = groups.at(-1);
    const key = groupKey(e);
    if (last && key && last.key === key) last.entries.push(e);
    else groups.push({ key, entries: [e], node: null });
  }
  return groups;
}

export const dayOf = (e) => (e.time || "").slice(0, 10);
export const hms = (e) => (e.time || "").slice(11, 19);

export function dayLabel(day) {
  const date = new Date(`${day}T00:00:00`);
  if (Number.isNaN(date.getTime())) return day || "Без даты";
  return date.toLocaleDateString("ru-RU", { day: "numeric", month: "long" });
}

export function rawText(e) {
  const head = e.time ? `${e.time} [${e.level}] ${e.module}: ${e.message}` : e.message;
  return [head, ...(e.trace || [])].join("\n");
}

const SVG = "http://www.w3.org/2000/svg";
const ICON_PATHS = {
  err: ["M12 3.5 2.8 19.5h18.4z", "M12 10v4.2", "M12 17.2v.01"],
  warn: ["M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z", "M12 7.5V12l3 2"],
  info: ["M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z", "M12 11v5", "M12 8v.01"],
};

// Плитка слева в карточке: форма и цвет сразу говорят об уровне, не читая текста.
export function levelIcon(level) {
  const kind = level === "ERROR" || level === "CRITICAL" ? "err" : level === "WARNING" ? "warn" : "info";
  const tile = el("span", `jr-ico ${kind}`);
  tile.title = level;
  tile.setAttribute("aria-hidden", "true");
  const svg = document.createElementNS(SVG, "svg");
  svg.setAttribute("viewBox", "0 0 24 24");
  for (const d of ICON_PATHS[kind]) {
    const path = document.createElementNS(SVG, "path");
    path.setAttribute("d", d);
    svg.append(path);
  }
  tile.append(svg);
  return tile;
}

// Цвет точки у имени модуля: один модуль всегда одного оттенка, соседние различимы с первого взгляда.
export function moduleHue(name) {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 360;
  return h;
}

// «3 мин назад» для подсказки у времени записи; now — часы журнала (шкала может их подменять).
export function timeAgo(ts, now) {
  const s = Math.max(0, Math.round((now - ts) / 1000));
  if (s < 60) return `${s} с назад`;
  if (s < 3600) return `${Math.floor(s / 60)} мин назад`;
  if (s < 86400) return `${Math.floor(s / 3600)} ч назад`;
  return `${Math.floor(s / 86400)} дн назад`;
}

export function plural(n, one, few, many) {
  const m10 = n % 10;
  const m100 = n % 100;
  if (m10 === 1 && m100 !== 11) return one;
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return few;
  return many;
}
