// Шкала событий журнала на canvas: столбики по интервалам, окрашенные по уровню, отметки
// перезапусков и выделение диапазона мышью. Как и в live-chart.js, любое изменение —
// окно времени, высота столбика, масштаб, выделение — идёт плавным следованием к цели,
// поэтому новая запись выглядит как рост столбика, а не как перерисовка. Сетевые запросы
// и состояние запроса принадлежат модулю journal.

const LABEL_H = 18;
const GAP = 1.5;

// Время ведёт собственные часы: правая граница окна — «сейчас» по серверу, и она движется
// с реальной скоростью каждый кадр, а не прыгает на каждом ответе.
export function createLogTimeline(root, { windowMs, bucketMs, onSelect }) {
  const canvas = document.createElement("canvas");
  canvas.setAttribute("role", "img");
  canvas.setAttribute("aria-label", "Шкала событий журнала");
  const tip = document.createElement("div");
  tip.className = "tl-tip";
  tip.hidden = true;
  root.append(canvas, tip);
  const g = canvas.getContext("2d");

  const reduced = () =>
    window.prefersReducedMotion?.() ??
    window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

  // cells: начало интервала → текущие (рисуемые) и целевые значения по уровням.
  const cells = new Map();
  const view = { yMax: 4, yTarget: 4, selFrom: 0, selTo: 0, selAlpha: 0 };
  const sel = { from: 0, to: 0, on: false };
  let serverOffset = 0;
  let markers = [];
  let hover = null;
  let drag = null;
  let width = 0;
  let height = 0;
  let dpr = 1;
  let colors = null;
  let colorsAt = 0;
  let frame = 0;
  let timer = 0;
  let last = 0;
  let drawnEnd = 0;
  let destroyed = false;

  const clock = () => Date.now() + serverOffset;
  const plotH = () => Math.max(10, height - LABEL_H);

  function readColors() {
    const style = getComputedStyle(root);
    const get = (name, fallback) => style.getPropertyValue(name).trim() || fallback;
    colors = {
      info: get("--border-strong", "#2e333d"),
      infoHover: get("--text-3", "#6c7381"),
      warn: get("--warn", "#f5a524"),
      bad: get("--bad", "#f2555a"),
      accent: get("--accent", "#7c8cff"),
      text: get("--text-3", "#6c7381"),
      grid: get("--border", "#22262e"),
      marker: get("--violet", "#b583ff"),
      surface: get("--surface", "#121419"),
    };
    colorsAt = performance.now();
  }

  // Экспоненциальное следование: прерываемое, не зависит от частоты кадров.
  function follow(current, target, dt, time = 160) {
    if (reduced()) return target;
    const next = current + (target - current) * (1 - Math.exp(-dt / time));
    return Math.abs(target - next) < 0.01 ? target : next;
  }

  const xOf = (t, end) => ((t - (end - windowMs)) / windowMs) * width;
  const tOf = (x, end) => end - windowMs + (x / width) * windowMs;

  // Принимает свежую сводку сервера. Значения становятся целями: столбики подтягиваются
  // к ним плавно, а не подменяются.
  function setStats(stats) {
    if (typeof stats.now === "number") {
      const next = stats.now - Date.now();
      // Часы сервера и вкладки расходятся на доли секунды; крупные скачки не сглаживаем.
      serverOffset = Math.abs(next - serverOffset) < 1500 ? serverOffset + (next - serverOffset) * 0.2 : next;
    }
    const seen = new Set();
    for (const b of stats.buckets || []) {
      seen.add(b.t);
      const cell = cells.get(b.t) || { cur: { i: 0, w: 0, e: 0 }, tgt: { i: 0, w: 0, e: 0 } };
      cell.tgt = { i: b.info, w: b.warn, e: b.err };
      cells.set(b.t, cell);
    }
    // Интервалы, которых в ответе нет, вышли за окно или опустели из-за фильтра.
    for (const [t, cell] of cells) {
      if (seen.has(t)) continue;
      if (t + bucketMs < clock() - windowMs - bucketMs) cells.delete(t);
      else cell.tgt = { i: 0, w: 0, e: 0 };
    }
    invalidate();
  }

  // Новая запись потока увеличивает свой интервал сразу, не дожидаясь опроса.
  function bump(ts, level) {
    const t = Math.floor(ts / bucketMs) * bucketMs;
    const cell = cells.get(t) || { cur: { i: 0, w: 0, e: 0 }, tgt: { i: 0, w: 0, e: 0 } };
    if (level === "ERROR" || level === "CRITICAL") cell.tgt.e++;
    else if (level === "WARNING") cell.tgt.w++;
    else cell.tgt.i++;
    cells.set(t, cell);
    invalidate();
  }

  function setMarkers(times) {
    markers = times;
    invalidate();
  }

  // Выделение задаёт журнал (чипы, быстрые диапазоны); холст лишь рисует его.
  function setSelection(range) {
    if (range) {
      sel.from = range[0];
      sel.to = range[1];
      sel.on = true;
    } else sel.on = false;
    invalidate();
  }

  function layout() {
    const rect = root.getBoundingClientRect();
    dpr = window.devicePixelRatio || 1;
    width = Math.max(0, rect.width);
    height = Math.max(0, rect.height);
    canvas.width = Math.round(width * dpr);
    canvas.height = Math.round(height * dpr);
    canvas.style.width = `${width}px`;
    canvas.style.height = `${height}px`;
    invalidate();
  }

  // Один шаг анимации; true — что-то ещё движется.
  function step(now) {
    const dt = last ? Math.min(100, now - last) : 16;
    last = now;
    let moving = false;
    // Масштаб строится по целям видимых интервалов, чтобы он не метался вместе с анимацией.
    const end = clock();
    let top = 4;
    for (const [t, cell] of cells) {
      for (const key of ["i", "w", "e"]) {
        const next = follow(cell.cur[key], cell.tgt[key], dt);
        if (next !== cell.cur[key]) moving = true;
        cell.cur[key] = next;
      }
      if (t + bucketMs < end - windowMs || t > end) continue;
      top = Math.max(top, cell.tgt.i + cell.tgt.w + cell.tgt.e);
    }
    view.yTarget = top;
    const y = follow(view.yMax, view.yTarget, dt, 240);
    if (y !== view.yMax) moving = true;
    view.yMax = y;
    const alpha = follow(view.selAlpha, sel.on || drag?.moved ? 1 : 0, dt, 120);
    if (alpha !== view.selAlpha) moving = true;
    view.selAlpha = alpha;
    if (sel.on || drag?.moved) {
      // Появляющееся выделение и перетаскивание идут за курсором без задержки; плавно
      // двигается только уже показанное выделение, когда его меняют снаружи.
      const snap = drag?.moved || view.selAlpha < 0.05;
      const from = snap ? sel.from : follow(view.selFrom, sel.from, dt, 120);
      const to = snap ? sel.to : follow(view.selTo, sel.to, dt, 120);
      if (from !== view.selFrom || to !== view.selTo) moving = true;
      view.selFrom = from;
      view.selTo = to;
    }
    return moving || drag != null;
  }

  function draw() {
    if (!width || !height) return;
    if (!colors || performance.now() - colorsAt > 1000) readColors();
    const end = clock();
    drawnEnd = end;
    g.setTransform(dpr, 0, 0, dpr, 0, 0);
    g.clearRect(0, 0, width, height);
    const ph = plotH();
    const barW = Math.max(1, (bucketMs / windowMs) * width - GAP);

    g.fillStyle = colors.grid;
    g.fillRect(0, ph, width, 1);

    // Метки времени по целым часам: плывут вместе с окном.
    g.font = '11px "JetBrains Mono", "Cascadia Mono", Consolas, monospace';
    g.textBaseline = "alphabetic";
    g.fillStyle = colors.text;
    const hour = 3600000;
    for (let t = Math.ceil((end - windowMs) / hour) * hour; t <= end; t += hour) {
      const x = xOf(t, end);
      if (x < 20 || x > width - 26) continue;
      g.fillRect(x, ph, 1, 3);
      const label = new Date(t).toTimeString().slice(0, 5);
      g.fillText(label, x - g.measureText(label).width / 2, height - 3);
    }

    // Выделение рисуется под столбиками, чтобы не гасить их цвет.
    if (view.selAlpha > 0.01) {
      const x1 = xOf(view.selFrom, end);
      const x2 = xOf(view.selTo, end);
      const left = Math.max(0, Math.min(x1, x2));
      const right = Math.min(width, Math.max(x1, x2, left + 2));
      g.globalAlpha = view.selAlpha;
      g.fillStyle = withAlpha(colors.accent, 0.14);
      g.fillRect(left, 0, right - left, ph);
      g.fillStyle = colors.accent;
      if (x1 >= 0 && x1 <= width) g.fillRect(Math.min(x1, x2), 0, 2, ph);
      if (x2 >= 0 && x2 <= width) g.fillRect(Math.max(x1, x2) - 2, 0, 2, ph);
      g.globalAlpha = 1;
    }

    for (const [t, cell] of cells) {
      const x0 = xOf(t, end);
      if (x0 + barW < 0 || x0 > width) continue;
      // Текущий интервал обрезается правой границей «сейчас»: он растёт вместе со временем.
      const w = Math.min(barW, width - x0);
      if (w <= 0) continue;
      const dim = view.selAlpha > 0.5 && sel.on && !overlaps(t, sel.from, sel.to) ? 0.32 : 1;
      let y = ph;
      const scale = ph / Math.max(1, view.yMax);
      for (const [key, color] of [["e", colors.bad], ["w", colors.warn], ["i", hover === t ? colors.infoHover : colors.info]]) {
        const value = cell.cur[key];
        if (value < 0.02) continue;
        const h = Math.max(2, value * scale);
        g.globalAlpha = dim;
        g.fillStyle = color;
        roundTop(g, x0, y - h, w, h, 2);
        y -= h;
      }
      g.globalAlpha = 1;
    }

    // Перезапуски: пунктир с подписью, не перекрывающей соседние.
    g.setLineDash([3, 3]);
    g.lineWidth = 1;
    g.strokeStyle = colors.marker;
    g.fillStyle = colors.marker;
    let lastLabel = -Infinity;
    for (const t of markers) {
      const x = Math.round(xOf(t, end)) + 0.5;
      if (x < 0 || x > width) continue;
      g.beginPath();
      g.moveTo(x, 0);
      g.lineTo(x, ph);
      g.stroke();
      if (x - lastLabel > 76) {
        g.setLineDash([]);
        g.fillText("перезапуск", Math.min(x + 4, width - 64), 10);
        g.setLineDash([3, 3]);
        lastLabel = x;
      }
    }
    g.setLineDash([]);
  }

  const overlaps = (t, from, to) => t + bucketMs > from && t < to;

  function roundTop(ctx2, x, y, w, h, r) {
    r = Math.min(r, w / 2, h);
    ctx2.beginPath();
    ctx2.moveTo(x, y + h);
    ctx2.lineTo(x, y + r);
    ctx2.quadraticCurveTo(x, y, x + r, y);
    ctx2.lineTo(x + w - r, y);
    ctx2.quadraticCurveTo(x + w, y, x + w, y + r);
    ctx2.lineTo(x + w, y + h);
    ctx2.closePath();
    ctx2.fill();
  }

  function withAlpha(color, alpha) {
    const m = color.match(/^#([0-9a-f]{6})$/i);
    if (m) {
      const n = parseInt(m[1], 16);
      return `rgba(${n >> 16}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
    }
    return color;
  }

  function loop(now) {
    frame = 0;
    if (destroyed) return;
    if (!root.offsetParent || document.hidden) {
      last = 0;
      return;
    }
    const moving = step(now);
    draw();
    if (moving) frame = requestAnimationFrame(loop);
    else {
      last = 0;
      // Окно едет по часам: в покое хватает редкой перерисовки, сдвиг за секунду меньше пикселя.
      timer = setTimeout(invalidate, 500);
    }
  }

  function invalidate() {
    if (destroyed || frame) return;
    clearTimeout(timer);
    frame = requestAnimationFrame(loop);
  }

  function bucketAt(x) {
    const t = Math.floor(tOf(x, drawnEnd || clock()) / bucketMs) * bucketMs;
    return t;
  }

  function showTip(x, t) {
    const cell = cells.get(t);
    if (!cell) {
      tip.hidden = true;
      return;
    }
    const total = cell.tgt.i + cell.tgt.w + cell.tgt.e;
    const hm = (ms) => new Date(ms).toTimeString().slice(0, 5);
    tip.textContent = `${hm(t)}–${hm(t + bucketMs)} · записей ${total}` +
      (cell.tgt.e ? ` · ошибок ${cell.tgt.e}` : "") + (cell.tgt.w ? ` · предупреждений ${cell.tgt.w}` : "");
    tip.hidden = false;
    const half = tip.offsetWidth / 2;
    tip.style.left = `${Math.max(half, Math.min(width - half, x))}px`;
  }

  const localX = (event) => event.clientX - canvas.getBoundingClientRect().left;

  canvas.addEventListener("pointerdown", (event) => {
    if (event.button !== 0) return;
    drag = { x: localX(event), moved: false, end: drawnEnd || clock() };
    canvas.setPointerCapture(event.pointerId);
    tip.hidden = true;
    invalidate();
  });
  canvas.addEventListener("pointermove", (event) => {
    const x = localX(event);
    if (drag) {
      if (!drag.moved && Math.abs(x - drag.x) > 4) drag.moved = true;
      if (drag.moved) {
        const a = tOf(Math.max(0, Math.min(width, drag.x)), drag.end);
        const b = tOf(Math.max(0, Math.min(width, x)), drag.end);
        sel.from = Math.min(a, b);
        sel.to = Math.max(a, b);
        sel.on = true;
        invalidate();
      }
      return;
    }
    const t = bucketAt(x);
    if (hover !== t) {
      hover = t;
      invalidate();
    }
    showTip(x, t);
  });
  canvas.addEventListener("pointerup", (event) => {
    if (!drag) return;
    const moved = drag.moved;
    const x = localX(event);
    drag = null;
    if (moved) onSelect([sel.from, sel.to]);
    else {
      const t = bucketAt(x);
      const cell = cells.get(t);
      if (cell && cell.tgt.i + cell.tgt.w + cell.tgt.e > 0) onSelect([t, t + bucketMs - 1]);
    }
    invalidate();
  });
  canvas.addEventListener("pointercancel", () => {
    drag = null;
    invalidate();
  });
  canvas.addEventListener("pointerleave", () => {
    hover = null;
    tip.hidden = true;
    invalidate();
  });

  const observer = new ResizeObserver(layout);
  observer.observe(root);
  document.addEventListener("visibilitychange", invalidate);

  function destroy() {
    destroyed = true;
    cancelAnimationFrame(frame);
    clearTimeout(timer);
    observer.disconnect();
    document.removeEventListener("visibilitychange", invalidate);
  }

  layout();
  return { setStats, bump, setMarkers, setSelection, invalidate, now: clock, destroy };
}
