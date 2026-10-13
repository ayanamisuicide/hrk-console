// Спарклайны карточек и мини-приложения Telegram.
import { createPlayback, lowerBound, reducedMotion, tracePath, valueAt } from "./chart-core.js";

// Живой спарклайн для карточек: то же непрерывное время, что у большого графика, — окно
// в две минуты прокручивается каждый кадр, новые точки въезжают справа, шкала меняется
// плавно. Рисует, только пока карточка видна; при уменьшенном движении — статичный кадр.
export function createSparkline(canvas, color, { windowMs = 120000, onValue } = {}) {
  const context = canvas.getContext("2d");
  const state = { times: [], values: [], playback: createPlayback(), low: 0, high: 0, frame: 0, last: 0, resolved: "" };

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
    const end = state.playback.at(state.times, now);
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
    for (let i = from; i < state.times.length && state.times[i] <= end; i++) {
      xs.push(x(state.times[i]));
      ys.push(y(state.values[i]));
    }
    const headValue = valueAt(state.times, state.values, end);
    xs.push(x(end));
    ys.push(y(headValue));
    onValue?.(headValue);
    if (xs.length < 2) return true;
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

  // Принимает точки истории; время показа ведут часы воспроизведения.
  function setData(points, key) {
    state.times = points.map((point) => Date.parse(point.at));
    state.values = points.map((point) => Number(point[key]) || 0);
    state.playback.sync(state.times);
    state.resolved = getComputedStyle(canvas).getPropertyValue(color).trim() || "#888";
    invalidate();
  }

  return { setData, invalidate };
}
