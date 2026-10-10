// Живой график на canvas: непрерывная прокрутка времени, монотонные кривые с заливкой,
// пики CPU, пороги, перезапуски, лента событий, перекрестие с подсказкой и масштаб выделением.
// Все переходы (диапазон, масштаб, ось, скрытие серии) — экспоненциальное следование к цели,
// поэтому любое изменение плавное и прерываемое. Сетевые запросы выполняет модуль system.

const severityColor = {
  critical: "--bad",
  warning: "--warn",
  ok: "--ok",
  info: "--accent",
};
const timeSteps = [
  10, 30, 60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 21600,
].map((seconds) => seconds * 1000);

const reducedMotion = () =>
  window.prefersReducedMotion?.() ??
  window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

// Ближайшее «круглое» значение не меньше заданного: 1, 2, 2.5, 5 × 10ⁿ.
function niceCeil(value) {
  if (!(value > 0)) return 1;
  const power = 10 ** Math.floor(Math.log10(value));
  for (const step of [1, 2, 2.5, 5, 10])
    if (step * power >= value) return step * power;
  return 10 * power;
}

// Индекс первой точки со временем не меньше t.
function lowerBound(times, t) {
  let low = 0;
  let high = times.length;
  while (low < high) {
    const middle = (low + high) >> 1;
    if (times[middle] < t) low = middle + 1;
    else high = middle;
  }
  return low;
}

// Касательные Фрица — Карлсона: кривая проходит через точки и не даёт ложных выбросов.
function monotoneTangents(xs, ys) {
  const n = xs.length;
  const slopes = new Array(Math.max(0, n - 1));
  const tangents = new Array(n).fill(0);
  for (let i = 0; i < n - 1; i++) {
    const dx = xs[i + 1] - xs[i];
    slopes[i] = dx ? (ys[i + 1] - ys[i]) / dx : 0;
  }
  if (n > 1) {
    tangents[0] = slopes[0];
    tangents[n - 1] = slopes[n - 2];
  }
  for (let i = 1; i < n - 1; i++)
    tangents[i] =
      slopes[i - 1] * slopes[i] <= 0 ? 0 : (slopes[i - 1] + slopes[i]) / 2;
  for (let i = 0; i < n - 1; i++) {
    if (!slopes[i]) {
      tangents[i] = tangents[i + 1] = 0;
      continue;
    }
    const a = tangents[i] / slopes[i];
    const b = tangents[i + 1] / slopes[i];
    const length = a * a + b * b;
    if (length > 9) {
      const scale = 3 / Math.sqrt(length);
      tangents[i] = scale * a * slopes[i];
      tangents[i + 1] = scale * b * slopes[i];
    }
  }
  return tangents;
}

function tracePath(context, xs, ys, move = true) {
  const tangents = monotoneTangents(xs, ys);
  if (move) context.moveTo(xs[0], ys[0]);
  else context.lineTo(xs[0], ys[0]);
  for (let i = 0; i < xs.length - 1; i++) {
    const third = (xs[i + 1] - xs[i]) / 3;
    context.bezierCurveTo(
      xs[i] + third,
      ys[i] + tangents[i] * third,
      xs[i + 1] - third,
      ys[i + 1] - tangents[i + 1] * third,
      xs[i + 1],
      ys[i + 1],
    );
  }
}

function formatClock(ms, withSeconds) {
  return new Date(ms).toLocaleTimeString("ru-RU", {
    hour: "2-digit",
    minute: "2-digit",
    ...(withSeconds ? { second: "2-digit" } : {}),
  });
}

export function createLiveChart(root, { onZoomChange } = {}) {
  const canvas = document.createElement("canvas");
  canvas.className = "live-chart-canvas";
  canvas.setAttribute("role", "img");
  const tooltip = document.createElement("div");
  tooltip.className = "live-chart-tooltip";
  tooltip.hidden = true;
  root.replaceChildren(canvas, tooltip);
  const context = canvas.getContext("2d");

  const state = {
    width: 0,
    height: 0,
    view: null,
    viewKey: "",
    times: [],
    columns: {},
    restarts: [],
    events: [],
    thresholds: {},
    rangeMs: 300000,
    serverNow: 0,
    receivedAt: 0,
    interval: 1000,
    // Текущее и целевое окно времени; при масштабе цель фиксирована, иначе следует за «сейчас».
    from: 0,
    to: 0,
    zoom: null,
    axisMax: { left: 1, right: 1 },
    alpha: {},
    hidden: new Set(),
    reveal: 1,
    revealStarted: 0,
    pointer: null,
    brush: null,
    colors: {},
    frame: 0,
    lastFrame: 0,
  };

  function resolveColors() {
    const styles = getComputedStyle(root);
    const read = (name) => styles.getPropertyValue(name).trim() || "#888";
    state.colors = {
      text: read("--text-3"),
      textStrong: read("--text-2"),
      grid: read("--border"),
      surface: read("--surface"),
      font: getComputedStyle(document.body).fontFamily,
    };
    for (const name of [
      "--chart-1",
      "--chart-2",
      "--chart-3",
      "--bad",
      "--warn",
      "--ok",
      "--accent",
    ])
      state.colors[name] = read(name);
  }

  // Переводит любой CSS-цвет в rgba с нужной прозрачностью через нормализацию canvas.
  const alphaCache = new Map();
  function withAlpha(color, alpha) {
    const key = `${color}|${alpha}`;
    if (alphaCache.has(key)) return alphaCache.get(key);
    context.fillStyle = "#000";
    context.fillStyle = color;
    const normalized = context.fillStyle;
    let result = normalized;
    if (normalized.startsWith("#")) {
      const value = parseInt(normalized.slice(1), 16);
      result = `rgba(${value >> 16}, ${(value >> 8) & 255}, ${value & 255}, ${alpha})`;
    } else if (normalized.startsWith("rgba"))
      result = normalized.replace(/[\d.]+\)$/, `${alpha})`);
    if (alphaCache.size > 200) alphaCache.clear();
    alphaCache.set(key, result);
    return result;
  }

  function layout() {
    const hasRight = state.view?.series.some((item) => item.axis === "right");
    const compact = state.width < 520;
    return {
      left: compact ? 38 : 48,
      right: hasRight ? (compact ? 42 : 54) : 14,
      top: 14,
      bottom: state.height - 50,
      lane: state.height - 38,
      axis: state.height - 12,
    };
  }

  // «Сейчас» на стороне сервера, продолженное часами браузера между опросами.
  function liveEnd(now) {
    if (!state.serverNow) return state.times.at(-1) || Date.now();
    if (reducedMotion()) return state.serverNow;
    const projected = state.serverNow + (now - state.receivedAt);
    const latest = state.times.at(-1) || state.serverNow;
    return Math.min(projected, Math.max(latest, state.serverNow) + 2500);
  }

  function targetWindow(now) {
    if (state.zoom) return state.zoom;
    const end = liveEnd(now);
    return { from: end - state.rangeMs, to: end };
  }

  function visibleRange(column, fromIndex, toIndex) {
    let maximum = 0;
    for (let i = fromIndex; i <= toIndex; i++) {
      const value = column[i];
      if (value > maximum) maximum = value;
    }
    return maximum;
  }

  function axisTargets(fromIndex, toIndex) {
    const targets = { left: 0, right: 0 };
    for (const series of state.view.series) {
      if (state.hidden.has(series.key)) continue;
      const column = state.columns[series.key];
      if (!column) continue;
      let maximum = visibleRange(column, fromIndex, toIndex);
      if (series.envelope && state.columns[series.envelope])
        maximum = Math.max(
          maximum,
          visibleRange(state.columns[series.envelope], fromIndex, toIndex),
        );
      targets[series.axis] = Math.max(targets[series.axis], maximum);
    }
    // Пороги не растягивают шкалу: при низкой нагрузке график остаётся подробным,
    // а порог выше шкалы обозначается меткой у верхнего края.
    for (const side of ["left", "right"]) {
      const axis = state.view.axes[side];
      if (!axis) continue;
      // Шкала делится на четыре круглых шага в единицах оси (например, мегабайтах),
      // чтобы подписи сетки были целыми.
      const unit = axis.unit || 1;
      const nice =
        4 *
        unit *
        niceCeil(Math.max(axis.minMax || 1, targets[side] * 1.18) / unit / 4);
      targets[side] = axis.fixedMax || Math.min(axis.cap || Infinity, nice);
    }
    return targets;
  }

  // Экспоненциальное следование к цели: dt-независимо, прерываемо.
  function follow(current, target, dt, time = 140) {
    if (reducedMotion() || !Number.isFinite(current)) return target;
    const mix = 1 - Math.exp(-dt / time);
    const next = current + (target - current) * mix;
    return Math.abs(next - target) < Math.abs(target) * 1e-4 + 1e-6
      ? target
      : next;
  }

  function draw(now) {
    const dt = state.lastFrame ? Math.min(100, now - state.lastFrame) : 16;
    state.lastFrame = now;
    const { width, height } = state;
    if (!width || !height || !state.view) return false;
    const box = layout();
    const target = targetWindow(now);
    const animatingWindow =
      Math.abs(state.from - target.from) > 1 ||
      Math.abs(state.to - target.to) > 1;
    if (!state.from || !state.to) {
      state.from = target.from;
      state.to = target.to;
    } else {
      state.from = follow(state.from, target.from, dt, 180);
      state.to = follow(state.to, target.to, dt, 180);
    }
    const span = Math.max(1000, state.to - state.from);
    const x = (t) =>
      box.left + ((t - state.from) / span) * (width - box.left - box.right);
    const timeAt = (px) =>
      state.from + ((px - box.left) / (width - box.left - box.right)) * span;

    const times = state.times;
    const fromIndex = Math.max(0, lowerBound(times, state.from) - 1);
    const toIndex = Math.min(times.length - 1, lowerBound(times, state.to));
    const targets = axisTargets(fromIndex, Math.max(fromIndex, toIndex));
    let animatingAxis = false;
    for (const side of ["left", "right"]) {
      const next = follow(state.axisMax[side], targets[side], dt, 220);
      animatingAxis ||= Math.abs(next - targets[side]) > 1e-6;
      state.axisMax[side] = next || targets[side] || 1;
    }
    let animatingAlpha = false;
    for (const series of state.view.series) {
      const goal = state.hidden.has(series.key) ? 0 : 1;
      const current = state.alpha[series.key] ?? goal;
      state.alpha[series.key] = follow(current, goal, dt, 120);
      animatingAlpha ||= Math.abs(state.alpha[series.key] - goal) > 0.002;
    }
    const plotHeight = box.bottom - box.top;
    const y = (value, side) =>
      box.bottom -
      (Math.max(0, value) / (state.axisMax[side] || 1)) * plotHeight;

    if (state.revealStarted) {
      state.reveal = Math.min(1, (now - state.revealStarted) / 950);
      if (state.reveal >= 1 || reducedMotion()) {
        state.reveal = 1;
        state.revealStarted = 0;
      }
    }
    const reveal = 1 - Math.pow(1 - state.reveal, 3);

    context.setTransform(devicePixelRatio, 0, 0, devicePixelRatio, 0, 0);
    context.clearRect(0, 0, width, height);
    const { colors } = state;
    const font = `500 11px ${colors.font}`;
    context.font = font;
    context.textBaseline = "middle";

    // Сетка и подписи осей.
    context.lineWidth = 1;
    for (let line = 0; line <= 4; line++) {
      const lineY = Math.round(box.top + (plotHeight * line) / 4) + 0.5;
      context.strokeStyle = withAlpha(colors.grid, line === 4 ? 1 : 0.7);
      context.setLineDash(line === 4 ? [] : [3, 5]);
      context.beginPath();
      context.moveTo(box.left, lineY);
      context.lineTo(width - box.right, lineY);
      context.stroke();
      context.setLineDash([]);
      const ratio = 1 - line / 4;
      context.fillStyle = colors.text;
      const left = state.view.axes.left;
      if (left) {
        context.textAlign = "right";
        context.fillText(
          left.format(state.axisMax.left * ratio),
          box.left - 8,
          lineY,
        );
      }
      const right = state.view.axes.right;
      if (right && state.view.series.some((item) => item.axis === "right")) {
        context.textAlign = "left";
        context.fillText(
          right.format(state.axisMax.right * ratio),
          width - box.right + 8,
          lineY,
        );
      }
    }
    const step =
      timeSteps.find(
        (candidate) =>
          (candidate / span) * (width - box.left - box.right) >= 84,
      ) || timeSteps.at(-1);
    const offset = new Date().getTimezoneOffset() * 60000;
    context.textAlign = "center";
    context.fillStyle = colors.text;
    for (
      let tick = Math.ceil((state.from - offset) / step) * step + offset;
      tick <= state.to;
      tick += step
    ) {
      const tickX = x(tick);
      if (tickX < box.left + 16 || tickX > width - box.right - 16) continue;
      context.fillText(formatClock(tick, step < 60000), tickX, box.axis);
      context.strokeStyle = withAlpha(colors.grid, 0.45);
      context.beginPath();
      context.moveTo(Math.round(tickX) + 0.5, box.top);
      context.lineTo(Math.round(tickX) + 0.5, box.bottom);
      context.stroke();
    }

    // Всё, что относится к данным, обрезаем по области графика и по анимации появления.
    context.save();
    context.beginPath();
    context.rect(
      box.left,
      0,
      (width - box.left - box.right) * reveal,
      box.bottom + 1,
    );
    context.clip();

    // Пороги уведомлений; одинаковые значения разных серий рисуются одной линией.
    const thresholdGroups = [];
    if (state.view.thresholds)
      for (const series of state.view.series) {
        const limit = state.thresholds[series.key];
        const alpha = state.alpha[series.key] ?? 1;
        if (!limit || alpha < 0.3) continue;
        const group = thresholdGroups.find((item) => item.limit === limit);
        if (group) group.series.push(series);
        else thresholdGroups.push({ limit, axis: series.axis, series: [series] });
      }
    for (const group of thresholdGroups) {
      group.above = group.limit > state.axisMax[group.axis] * 1.001;
      if (group.above) continue;
      const color =
        group.series.length > 1
          ? colors.textStrong
          : colors[group.series[0].color];
      const lineY = Math.round(y(group.limit, group.axis)) + 0.5;
      context.strokeStyle = withAlpha(color, 0.55);
      context.setLineDash([6, 6]);
      context.beginPath();
      context.moveTo(box.left, lineY);
      context.lineTo(width - box.right, lineY);
      context.stroke();
      context.setLineDash([]);
    }

    // Перезапуски бота.
    for (const restart of state.restarts) {
      if (restart < state.from || restart > state.to) continue;
      const restartX = Math.round(x(restart)) + 0.5;
      context.strokeStyle = withAlpha(colors["--bad"], 0.6);
      context.setLineDash([4, 4]);
      context.beginPath();
      context.moveTo(restartX, box.top);
      context.lineTo(restartX, box.bottom);
      context.stroke();
      context.setLineDash([]);
    }

    // Серии: разрывы больше нескольких интервалов (панель не работала) не соединяем.
    const gap = Math.max(state.interval * 5, 10000);
    const segments = [];
    let segmentStart = fromIndex;
    for (let i = fromIndex + 1; i <= toIndex; i++)
      if (times[i] - times[i - 1] > gap) {
        segments.push([segmentStart, i - 1]);
        segmentStart = i;
      }
    if (toIndex >= fromIndex) segments.push([segmentStart, toIndex]);

    for (const series of state.view.series) {
      const column = state.columns[series.key];
      const alpha = state.alpha[series.key] ?? 1;
      if (!column || alpha < 0.02) continue;
      const color = colors[series.color];
      // Для серий, где ноль означает «нет измерения» (задержка Telegram), нули — тоже разрывы.
      const seriesSegments = series.skipZero
        ? segments.flatMap(([start, end]) => {
            const parts = [];
            let open = -1;
            for (let i = start; i <= end + 1; i++) {
              const present = i <= end && column[i] > 0;
              if (present && open < 0) open = i;
              if (!present && open >= 0) {
                parts.push([open, i - 1]);
                open = -1;
              }
            }
            return parts;
          })
        : segments;
      for (const [start, end] of seriesSegments) {
        if (end - start < 1) continue;
        const xs = [];
        const ys = [];
        for (let i = start; i <= end; i++) {
          xs.push(x(times[i]));
          ys.push(y(column[i], series.axis));
        }
        // Пик CPU внутри сжатого интервала — полупрозрачная полоса над средним.
        const envelope = series.envelope && state.columns[series.envelope];
        if (envelope) {
          context.beginPath();
          for (let i = start; i <= end; i++) {
            const peak = Math.max(envelope[i] || 0, column[i]);
            const px = x(times[i]);
            if (i === start) context.moveTo(px, y(peak, series.axis));
            else context.lineTo(px, y(peak, series.axis));
          }
          for (let i = end; i >= start; i--)
            context.lineTo(x(times[i]), y(column[i], series.axis));
          context.closePath();
          context.fillStyle = withAlpha(color, 0.13 * alpha);
          context.fill();
        }
        if (series.fill !== false) {
          const gradient = context.createLinearGradient(
            0,
            box.top,
            0,
            box.bottom,
          );
          gradient.addColorStop(0, withAlpha(color, 0.3 * alpha));
          gradient.addColorStop(0.65, withAlpha(color, 0.08 * alpha));
          gradient.addColorStop(1, withAlpha(color, 0));
          context.beginPath();
          tracePath(context, xs, ys);
          context.lineTo(xs.at(-1), box.bottom);
          context.lineTo(xs[0], box.bottom);
          context.closePath();
          context.fillStyle = gradient;
          context.fill();
        }
        context.beginPath();
        tracePath(context, xs, ys);
        context.lineWidth = 2;
        context.lineJoin = "round";
        context.lineCap = "round";
        context.strokeStyle = withAlpha(color, alpha);
        context.shadowColor = withAlpha(color, 0.55 * alpha);
        context.shadowBlur = reducedMotion() ? 0 : 10;
        if (series.dashed) context.setLineDash([5, 4]);
        context.stroke();
        context.setLineDash([]);
        context.shadowBlur = 0;
      }
    }
    context.restore();

    // Живая «голова»: точка с расходящимся кольцом на последнем значении.
    let pulsing = false;
    if (!state.zoom && times.length && reveal >= 1) {
      const last = times.length - 1;
      if (times[last] >= state.from && times[last] <= state.to + 1000)
        for (const series of state.view.series) {
          const column = state.columns[series.key];
          const alpha = state.alpha[series.key] ?? 1;
          if (!column || alpha < 0.05) continue;
          if (series.skipZero && !(column[last] > 0)) continue;
          const color = colors[series.color];
          const headX = x(times[last]);
          const headY = y(column[last], series.axis);
          if (!reducedMotion()) {
            const phase = (now % 1800) / 1800;
            context.beginPath();
            context.arc(headX, headY, 4 + phase * 11, 0, Math.PI * 2);
            context.fillStyle = withAlpha(color, 0.35 * (1 - phase) * alpha);
            context.fill();
            pulsing = true;
          }
          context.beginPath();
          context.arc(headX, headY, 4, 0, Math.PI * 2);
          context.fillStyle = withAlpha(color, alpha);
          context.fill();
          context.lineWidth = 2;
          context.strokeStyle = colors.surface;
          context.stroke();
        }
    }

    // Подписи порогов справа поверх серий; порог выше шкалы — метка со стрелкой у верха.
    if (reveal >= 1)
      thresholdGroups.forEach((group, index) => {
        const names = group.series
          .map((series) => series.short || series.label)
          .join(" · ");
        const label = `${group.above ? "↑ " : ""}порог ${names} ${group.limit}%`;
        const labelY = group.above
          ? box.top + 9 + index * 19
          : y(group.limit, group.axis);
        const color =
          group.series.length > 1
            ? colors.textStrong
            : colors[group.series[0].color];
        context.font = `600 10px ${colors.font}`;
        const labelWidth = context.measureText(label).width + 10;
        const labelX = width - box.right - labelWidth - 4;
        context.fillStyle = withAlpha(colors.surface, 0.92);
        context.beginPath();
        context.roundRect(labelX, labelY - 8, labelWidth, 16, 8);
        context.fill();
        context.fillStyle = withAlpha(color, group.above ? 0.75 : 1);
        context.textAlign = "left";
        context.fillText(label, labelX + 5, labelY + 0.5);
        context.font = font;
      });

    // Лента событий под графиком: близкие маркеры объединяются в кружок с числом.
    const laneMarkers = [];
    const laneItems = [
      ...state.events.map((event) => ({
        t: Date.parse(event.time),
        color: colors[severityColor[event.severity] || "--accent"],
        event,
      })),
      ...state.restarts.map((t) => ({
        t,
        color: colors["--bad"],
        event: { title: "Смена процесса бота", severity: "critical" },
      })),
    ]
      .filter((item) => item.t >= state.from && item.t <= state.to)
      .sort((a, b) => a.t - b.t);
    for (const item of laneItems) {
      const itemX = x(item.t);
      const previous = laneMarkers.at(-1);
      if (previous && itemX - previous.x < 12) {
        previous.items.push(item);
        if (item.event.severity === "critical") previous.color = item.color;
      } else laneMarkers.push({ x: itemX, color: item.color, items: [item] });
    }
    context.strokeStyle = withAlpha(colors.grid, 0.8);
    context.beginPath();
    context.moveTo(box.left, Math.round(box.lane) + 0.5);
    context.lineTo(width - box.right, Math.round(box.lane) + 0.5);
    context.stroke();
    for (const marker of laneMarkers) {
      if (marker.x < box.left - 2 || marker.x > width - box.right + 2) continue;
      const radius = marker.items.length > 1 ? 7 : 4.5;
      context.beginPath();
      context.arc(marker.x, box.lane, radius, 0, Math.PI * 2);
      context.fillStyle = marker.color;
      context.fill();
      context.lineWidth = 2;
      context.strokeStyle = colors.surface;
      context.stroke();
      if (marker.items.length > 1) {
        context.fillStyle = colors.surface;
        context.font = `700 9px ${colors.font}`;
        context.textAlign = "center";
        context.fillText(
          String(Math.min(99, marker.items.length)),
          marker.x,
          box.lane + 0.5,
        );
        context.font = font;
      }
    }
    state.laneMarkers = laneMarkers;

    // Выделение для масштаба.
    if (state.brush?.active) {
      const left = Math.min(state.brush.start, state.brush.current);
      const right = Math.max(state.brush.start, state.brush.current);
      context.fillStyle = withAlpha(colors["--accent"], 0.12);
      context.fillRect(left, box.top, right - left, box.bottom - box.top);
      context.strokeStyle = withAlpha(colors["--accent"], 0.7);
      context.strokeRect(
        left + 0.5,
        box.top + 0.5,
        right - left - 1,
        box.bottom - box.top - 1,
      );
    }

    // Перекрестие и подсказка.
    updateHover(box, x, y, timeAt);

    canvas.setAttribute("aria-label", describe());
    return (
      animatingWindow ||
      animatingAxis ||
      animatingAlpha ||
      state.reveal < 1 ||
      pulsing ||
      (!state.zoom && !reducedMotion())
    );
  }

  function updateHover(box, x, y, timeAt) {
    const pointer = state.pointer;
    if (
      !pointer ||
      state.brush?.active ||
      !state.times.length ||
      pointer.x < box.left ||
      pointer.x > state.width - box.right
    ) {
      tooltip.hidden = true;
      return;
    }
    const nearLane = Math.abs(pointer.y - box.lane) < 12;
    const marker = nearLane
      ? state.laneMarkers?.find((item) => Math.abs(item.x - pointer.x) < 10)
      : null;
    const t = timeAt(pointer.x);
    let index = lowerBound(state.times, t);
    if (
      index > 0 &&
      (index >= state.times.length ||
        t - state.times[index - 1] < state.times[index] - t)
    )
      index--;
    const pointTime = state.times[index];
    const crossX = Math.round(marker ? marker.x : x(pointTime)) + 0.5;
    const { colors } = state;
    context.strokeStyle = withAlpha(colors.textStrong, 0.45);
    context.beginPath();
    context.moveTo(crossX, box.top);
    context.lineTo(crossX, box.bottom);
    context.stroke();
    const rows = [];
    if (!marker)
      for (const series of state.view.series) {
        const column = state.columns[series.key];
        if (!column || state.hidden.has(series.key)) continue;
        const value = column[index];
        const color = colors[series.color];
        context.beginPath();
        context.arc(crossX, y(value, series.axis), 4.5, 0, Math.PI * 2);
        context.fillStyle = color;
        context.fill();
        context.lineWidth = 2;
        context.strokeStyle = colors.surface;
        context.stroke();
        rows.push({ color, label: series.label, value: series.format(value) });
        const envelope =
          series.envelope && state.columns[series.envelope]?.[index];
        if (envelope > value)
          rows.push({
            color,
            label: "пик",
            value: series.format(envelope),
            muted: true,
          });
      }
    const events = marker
      ? marker.items.map((item) => item.event)
      : state.events.filter(
          (event) =>
            Math.abs(Date.parse(event.time) - pointTime) <=
            Math.max(state.interval, (state.to - state.from) / 200),
        );
    renderTooltip(
      marker ? marker.items[0].t : pointTime,
      rows,
      events,
      crossX,
      box,
    );
  }

  function renderTooltip(time, rows, events, crossX, box) {
    const signature = JSON.stringify([time, rows, events.map((e) => e.title)]);
    if (tooltip.dataset.signature !== signature) {
      tooltip.dataset.signature = signature;
      const fragment = document.createDocumentFragment();
      const heading = document.createElement("time");
      heading.textContent = formatClock(time, state.rangeMs <= 3600000);
      fragment.append(heading);
      for (const row of rows) {
        const line = document.createElement("div");
        line.className = `live-chart-row${row.muted ? " muted" : ""}`;
        const swatch = document.createElement("i");
        swatch.style.background = row.color;
        const label = document.createElement("span");
        label.textContent = row.label;
        const value = document.createElement("b");
        value.textContent = row.value;
        line.append(swatch, label, value);
        fragment.append(line);
      }
      for (const event of events.slice(0, 4)) {
        const line = document.createElement("div");
        line.className = "live-chart-event";
        line.dataset.severity = event.severity || "info";
        line.textContent = event.title;
        if (event.message) line.title = event.message;
        fragment.append(line);
      }
      if (events.length > 4) {
        const more = document.createElement("div");
        more.className = "live-chart-event";
        more.textContent = `и ещё ${events.length - 4}`;
        fragment.append(more);
      }
      tooltip.replaceChildren(fragment);
    }
    tooltip.hidden = false;
    const tipWidth = tooltip.offsetWidth;
    const flip = crossX + 14 + tipWidth > state.width - 4;
    tooltip.style.transform = `translate(${Math.round(flip ? crossX - 14 - tipWidth : crossX + 14)}px, ${Math.round(box.top + 4)}px)`;
  }

  function describe() {
    if (!state.view || !state.times.length) return "Данных пока нет";
    const last = state.times.length - 1;
    return `${state.view.title}: ${state.view.series
      .filter((series) => !state.hidden.has(series.key))
      .map(
        (series) =>
          `${series.label} ${series.format(state.columns[series.key]?.[last] || 0)}`,
      )
      .join(", ")}`;
  }

  function loop(now) {
    state.frame = 0;
    if (!root.isConnected || root.offsetParent === null || document.hidden)
      return;
    if (draw(now)) state.frame = requestAnimationFrame(loop);
  }

  function invalidate() {
    if (!state.frame) {
      state.lastFrame = 0;
      state.frame = requestAnimationFrame(loop);
    }
  }

  function resize() {
    const rect = root.getBoundingClientRect();
    if (!rect.width || !rect.height) return;
    state.width = rect.width;
    state.height = rect.height;
    canvas.width = Math.round(rect.width * devicePixelRatio);
    // Размер на экране задаёт CSS (100% контейнера): фиксированная ширина в пикселях
    // не дала бы контейнеру сжиматься при сужении окна.
    canvas.height = Math.round(rect.height * devicePixelRatio);
    invalidate();
  }
  const observer = new ResizeObserver(resize);
  observer.observe(root);
  document.addEventListener("visibilitychange", invalidate);

  function localPoint(event) {
    const rect = canvas.getBoundingClientRect();
    return { x: event.clientX - rect.left, y: event.clientY - rect.top };
  }
  canvas.addEventListener("pointermove", (event) => {
    state.pointer = localPoint(event);
    if (state.brush) {
      state.brush.current = Math.max(
        layout().left,
        Math.min(state.width - layout().right, state.pointer.x),
      );
      if (Math.abs(state.brush.current - state.brush.start) > 6)
        state.brush.active = true;
    }
    invalidate();
  });
  canvas.addEventListener("pointerleave", () => {
    state.pointer = null;
    if (!state.brush) tooltip.hidden = true;
    invalidate();
  });
  canvas.addEventListener("pointerdown", (event) => {
    if (event.pointerType === "touch" || event.button !== 0) return;
    const point = localPoint(event);
    const box = layout();
    if (point.y > box.bottom || point.x < box.left) return;
    canvas.setPointerCapture(event.pointerId);
    state.brush = { start: point.x, current: point.x, active: false };
  });
  canvas.addEventListener("pointerup", () => {
    const brush = state.brush;
    state.brush = null;
    if (brush?.active) {
      const box = layout();
      const span = state.to - state.from;
      const toTime = (px) =>
        state.from +
        ((px - box.left) / (state.width - box.left - box.right)) * span;
      const from = toTime(Math.min(brush.start, brush.current));
      const to = toTime(Math.max(brush.start, brush.current));
      if (to - from >= 10000) {
        state.zoom = { from, to };
        onZoomChange?.(true);
      }
    }
    invalidate();
  });
  canvas.addEventListener("dblclick", () => resetZoom());

  function resetZoom() {
    if (!state.zoom) return;
    state.zoom = null;
    onZoomChange?.(false);
    invalidate();
  }

  // Принимает ответ /api/system/history и описание вида. Новый вид или диапазон запускают
  // появление слева направо; обычный опрос только продолжает текущую анимацию.
  function setData(payload, view) {
    resolveColors();
    const points = payload.points || [];
    const key = `${view.key}|${payload.rangeSeconds}`;
    if (key !== state.viewKey) {
      const switchingRange =
        state.viewKey && state.viewKey.split("|")[0] === view.key;
      state.viewKey = key;
      state.view = view;
      state.zoom = null;
      onZoomChange?.(false);
      state.alpha = {};
      if (!switchingRange) {
        state.axisMax = { left: 0, right: 0 };
        state.reveal = 0;
        state.revealStarted = performance.now();
      }
    }
    state.view = view;
    state.times = points.map((point) => Date.parse(point.at));
    state.columns = {};
    for (const series of view.series) {
      state.columns[series.key] = points.map(
        (point) => Number(point[series.key]) || 0,
      );
      if (series.envelope)
        state.columns[series.envelope] = points.map(
          (point) => Number(point[series.envelope]) || 0,
        );
    }
    state.restarts = (payload.restarts || []).map((time) => Date.parse(time));
    state.events = payload.events || [];
    state.thresholds = payload.thresholds || {};
    state.rangeMs = (payload.rangeSeconds || 300) * 1000;
    state.interval = (payload.intervalSeconds || 1) * 1000;
    state.serverNow = payload.now
      ? Date.parse(payload.now)
      : state.times.at(-1) || Date.now();
    state.receivedAt = performance.now();
    invalidate();
  }

  function setHidden(key, hidden) {
    if (hidden) state.hidden.add(key);
    else state.hidden.delete(key);
    invalidate();
  }

  function destroy() {
    observer.disconnect();
    document.removeEventListener("visibilitychange", invalidate);
    if (state.frame) cancelAnimationFrame(state.frame);
  }

  return {
    canvas,
    setData,
    setHidden,
    resetZoom,
    isZoomed: () => Boolean(state.zoom),
    invalidate,
    destroy,
  };
}

// Живой спарклайн для карточек: то же непрерывное время, что у большого графика, — окно
// в две минуты прокручивается каждый кадр, новые точки въезжают справа, шкала меняется
// плавно. Рисует, только пока карточка видна; при уменьшенном движении — статичный кадр.
export function createSparkline(canvas, color, windowMs = 120000) {
  const context = canvas.getContext("2d");
  const state = { times: [], values: [], serverNow: 0, receivedAt: 0, low: 0, high: 0, frame: 0, last: 0, resolved: "" };

  function follow(current, target, dt) {
    if (reducedMotion() || !current) return target;
    return current + (target - current) * (1 - Math.exp(-dt / 260));
  }

  function draw(now) {
    const rect = canvas.getBoundingClientRect();
    if (!rect.width || !rect.height || state.times.length < 2) return false;
    const ratio = devicePixelRatio || 1;
    if (canvas.width !== Math.round(rect.width * ratio) || canvas.height !== Math.round(rect.height * ratio)) {
      canvas.width = Math.round(rect.width * ratio);
      canvas.height = Math.round(rect.height * ratio);
    }
    const dt = state.last ? Math.min(100, now - state.last) : 16;
    state.last = now;
    const latest = state.times.at(-1);
    const end = reducedMotion()
      ? latest
      : Math.min(state.serverNow + (now - state.receivedAt), Math.max(latest, state.serverNow) + 2500);
    const start = end - windowMs;
    const from = Math.max(0, lowerBound(state.times, start) - 1);
    let maximum = -Infinity;
    let minimum = Infinity;
    for (let i = from; i < state.times.length; i++) {
      maximum = Math.max(maximum, state.values[i]);
      minimum = Math.min(minimum, state.values[i]);
    }
    if (!Number.isFinite(maximum)) return false;
    // Почти ровная серия (диск, память) растягивается вокруг своего уровня, иначе — от нуля.
    const flat = minimum > maximum * 0.6;
    state.high = follow(state.high, (maximum || 1) * (flat ? 1.02 : 1.15), dt);
    state.low = follow(state.low, flat ? minimum * 0.97 : 0, dt);
    const width = rect.width;
    const height = rect.height;
    const x = (t) => ((t - start) / windowMs) * (width - 6) + 1;
    const y = (value) => height - 3 - ((value - state.low) / (state.high - state.low || 1)) * (height - 8);
    const xs = [];
    const ys = [];
    for (let i = from; i < state.times.length; i++) {
      xs.push(x(state.times[i]));
      ys.push(y(state.values[i]));
    }
    context.setTransform(ratio, 0, 0, ratio, 0, 0);
    context.clearRect(0, 0, width, height);
    context.save();
    context.beginPath();
    context.rect(0, 0, width, height);
    context.clip();
    const gradient = context.createLinearGradient(0, 0, 0, height);
    gradient.addColorStop(0, state.resolved);
    gradient.addColorStop(1, "transparent");
    context.beginPath();
    tracePath(context, xs, ys);
    context.lineTo(xs.at(-1), height);
    context.lineTo(xs[0], height);
    context.closePath();
    context.globalAlpha = 0.22;
    context.fillStyle = gradient;
    context.fill();
    context.globalAlpha = 1;
    context.beginPath();
    tracePath(context, xs, ys);
    context.lineWidth = 1.6;
    context.lineJoin = "round";
    context.strokeStyle = state.resolved;
    context.shadowColor = state.resolved;
    context.shadowBlur = reducedMotion() ? 0 : 6;
    context.stroke();
    context.shadowBlur = 0;
    context.restore();
    const headX = xs.at(-1);
    const headY = ys.at(-1);
    if (!reducedMotion()) {
      const phase = (now % 1800) / 1800;
      context.beginPath();
      context.arc(headX, headY, 2.4 + phase * 6, 0, Math.PI * 2);
      context.globalAlpha = 0.35 * (1 - phase);
      context.fillStyle = state.resolved;
      context.fill();
      context.globalAlpha = 1;
    }
    context.beginPath();
    context.arc(headX, headY, 2.4, 0, Math.PI * 2);
    context.fillStyle = state.resolved;
    context.fill();
    return !reducedMotion();
  }

  function loop(now) {
    state.frame = 0;
    if (!canvas.isConnected || canvas.offsetParent === null || document.hidden) return;
    if (draw(now)) state.frame = requestAnimationFrame(loop);
  }

  function invalidate() {
    if (!state.frame) {
      state.last = 0;
      state.frame = requestAnimationFrame(loop);
    }
  }
  document.addEventListener("visibilitychange", invalidate);

  // Принимает точки истории и серверное «сейчас» из того же ответа.
  function setData(points, key, serverNow) {
    state.times = points.map((point) => Date.parse(point.at));
    state.values = points.map((point) => Number(point[key]) || 0);
    state.serverNow = serverNow ? Date.parse(serverNow) : state.times.at(-1) || Date.now();
    state.receivedAt = performance.now();
    state.resolved = getComputedStyle(canvas).getPropertyValue(color).trim() || "#888";
    invalidate();
  }

  return { setData, invalidate };
}
