// Подмена серверного API журнала для UI-тестов: тот же отбор, курсор и сводка, что в logfeed,
// но на фиксированных данных. Английский текст записей — входные данные чужого бота.

const PREFIX = /^(\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}) \[([A-Z]+)\] ([^:]+):\s?(.*)$/;

// Собирает записи из сырых строк: продолжения уходят в traceback предыдущей записи.
function parseEntries(lines) {
  const out = [];
  for (const raw of lines) {
    const m = raw.match(PREFIX);
    if (m) {
      const ts = new Date(m[1].replace(" ", "T")).getTime();
      out.push({ id: `${ts.toString(36)}-${out.length}`, ts, time: m[1], level: m[2], module: m[3], message: m[4] });
    } else if (out.length) (out.at(-1).trace ||= []).push(raw);
  }
  return out;
}

const defaultLines = [
  "2026-10-03 10:00:00 [INFO] Core: started",
  "2026-10-03 10:00:01 [ERROR] Core: failed request",
  "Traceback (most recent call last):",
  '  File "core.py", line 1, in run',
  "2026-10-03 10:00:02 [ERROR] Core: retry failed",
];

// Возвращает обработчик для маршрутов /api/logs/*: (url) => объект ответа или null.
function createJournalApi(lines = defaultLines, { now = Date.now() } = {}) {
  const entries = parseEntries(lines);
  const calls = [];

  function match(e, p) {
    const levels = (p.get("level") || "").split(",").filter(Boolean);
    if (levels.length && !levels.some((l) => l === e.level || (l === "ERROR" && e.level === "CRITICAL"))) return false;
    if (p.get("module") && e.module !== p.get("module")) return false;
    const q = (p.get("q") || "").toLowerCase();
    if (q && !`${e.message}\n${e.module}\n${(e.trace || []).join("\n")}`.toLowerCase().includes(q)) return false;
    return true;
  }

  function handle(url) {
    const { pathname, searchParams: p } = new URL(url);
    calls.push(`${pathname}?${p}`);
    if (pathname === "/api/logs/entries") {
      const limit = Number(p.get("limit")) || 200;
      const from = Number(p.get("from")) || 0;
      const to = Number(p.get("to")) || 0;
      const before = Number(p.get("before")) || 0;
      let skip = Number(p.get("skip")) || 0;
      const picked = [];
      for (let i = entries.length - 1; i >= 0 && picked.length <= limit; i--) {
        const e = entries[i];
        if (before) {
          if (e.ts > before) continue;
          if (e.ts === before && skip > 0 && match(e, p)) {
            skip--;
            continue;
          }
        }
        if ((from && e.ts < from) || (to && e.ts > to) || !match(e, p)) continue;
        picked.push(e);
      }
      const hasMore = picked.length > limit;
      return { entries: picked.slice(0, limit).reverse(), hasMore };
    }
    if (pathname === "/api/logs/stats") {
      const bucket = Number(p.get("bucket")) || 300000;
      const end = Math.ceil(now / bucket) * bucket;
      const start = Math.floor((now - 6 * 3600 * 1000) / bucket) * bucket;
      const buckets = [];
      for (let t = start; t < end; t += bucket) buckets.push({ t, info: 0, warn: 0, err: 0 });
      const modules = {};
      for (const e of entries) {
        modules[e.module] = (modules[e.module] || 0) + 1;
        if (!match(e, p)) continue;
        const b = buckets[Math.floor((e.ts - start) / bucket)];
        if (!b) continue;
        if (e.level === "ERROR" || e.level === "CRITICAL") b.err++;
        else if (e.level === "WARNING") b.warn++;
        else b.info++;
      }
      return { buckets, modules, total: 0, bucketMs: bucket, now };
    }
    if (pathname === "/api/logs/context") {
      const ts = Number(p.get("ts"));
      const at = entries.findIndex((e) => e.ts === ts && e.id === p.get("id"));
      if (at < 0) return null;
      const n = Number(p.get("n")) || 20;
      const count = (name) => (p.has(name) ? Number(p.get(name)) : n);
      const lo = Math.max(0, at - count("before"));
      return { entries: entries.slice(lo, at + count("after") + 1), index: at - lo };
    }
    if (pathname === "/api/logs/problems") {
      // Упрощённая группировка сервера: модуль, сообщение без чисел и последняя строка traceback.
      const levels = (p.get("level") || "ERROR").split(",");
      const from = Number(p.get("from")) || 0;
      const to = Number(p.get("to")) || 0;
      const groups = new Map();
      for (let i = entries.length - 1; i >= 0; i--) {
        const e = entries[i];
        if (e.ts < from || (to && e.ts > to)) continue;
        if (!levels.some((l) => l === e.level || (l === "ERROR" && e.level === "CRITICAL"))) continue;
        const key = `${e.module}|${e.message.replace(/\d+([.,:]\d+)*/g, "#")}|${(e.trace || []).at(-1) || ""}`;
        const g = groups.get(key);
        if (g) {
          g.count++;
          g.first = e.ts;
        } else groups.set(key, { count: 1, first: e.ts, last: e, query: e.message.split(/\d/)[0].trim() });
      }
      const problems = [...groups.values()].sort((a, b) => b.count - a.count || b.last.ts - a.last.ts);
      return { problems, from, to };
    }
    return undefined;
  }

  return { entries, calls, handle };
}

module.exports = { createJournalApi, parseEntries, defaultLines };
