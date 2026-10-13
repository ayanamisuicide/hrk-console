// Общее для графиков на canvas: часы воспроизведения, значение серии в момент времени,
// монотонные кривые и «круглые» шкалы. Используют большой график и спарклайны карточек.

// Задержка воспроизведения от самого свежего полученного замера. Замеры идут раз в секунду,
// опрос тоже раз в секунду, но фазы у них не совпадают, а ответ иногда опаздывает. С тремя
// секундами следующий замер к моменту показа уже получен, и линии не упираются в конец данных.
export const PLAYBACK_DELAY = 3000;
// Расхождение, после которого часы не догоняют, а переставляются: первый кадр, вкладка
// была скрыта, сменился диапазон.
const PLAYBACK_SNAP = 4000;

// Часы воспроизведения. Идут с реальной скоростью кадр за кадром и лишь слегка ускоряются
// или замедляются, подстраиваясь под приход данных. Поэтому время на графике не стоит
// в ожидании опроса и не прыгает при каждом ответе сервера. Показывается только то, что
// уже измерено: часы не уходят дальше последнего замера.
export function createPlayback() {
  const clock = { t: 0, last: 0, offsets: [] };
  return {
    // Новые данные. Сдвиг «время замера − время получения» колеблется: ответ мог задержаться,
    // а новый замер — не успеть к опросу. Опорой служит самый свежий сдвиг за последние
    // опросы, поэтому редкие опоздания не тормозят часы.
    sync(times) {
      const latest = times.at(-1);
      if (latest === undefined) return;
      const now = performance.now();
      clock.offsets.push([now, latest - now]);
      while (clock.offsets.length > 1 && now - clock.offsets[0][0] > 10000) clock.offsets.shift();
      // Данные отстали сильнее, чем часы могут догнать (сменился диапазон, сервер долго молчал) —
      // старые сдвиги больше не годятся.
      const freshest = Math.max(...clock.offsets.map(([, offset]) => offset));
      if (freshest - (latest - now) > PLAYBACK_SNAP) clock.offsets = [[now, latest - now]];
    },
    at(times, now) {
      const latest = times.at(-1);
      if (latest === undefined) return Date.now();
      if (reducedMotion() || !clock.offsets.length) return latest;
      const target = Math.max(...clock.offsets.map(([, offset]) => offset)) + now - PLAYBACK_DELAY;
      const dt = now - clock.last;
      if (!clock.last || dt > 1000 || Math.abs(target - clock.t) > PLAYBACK_SNAP) clock.t = target;
      else if (dt > 0) {
        // Мягкая подстройка: не больше ±5% скорости, на глаз незаметно.
        const drift = Math.max(-0.05, Math.min(0.05, (target - clock.t) / 8000));
        clock.t += dt * (1 + drift);
      }
      clock.last = now;
      clock.t = Math.max(times[0], Math.min(clock.t, latest));
      return clock.t;
    },
  };
}

// Значение серии в момент t: линейно между соседними замерами.
export function valueAt(times, column, t) {
  const index = lowerBound(times, t);
  if (index <= 0) return column[0];
  if (index >= times.length) return column[times.length - 1];
  const left = times[index - 1];
  const right = times[index];
  const mix = right > left ? (t - left) / (right - left) : 1;
  return column[index - 1] + (column[index] - column[index - 1]) * mix;
}

export const reducedMotion = () =>
  window.prefersReducedMotion?.() ??
  window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;

// Ближайшее «круглое» значение не меньше заданного: 1, 2, 2.5, 5 × 10ⁿ.
export function niceCeil(value) {
  if (!(value > 0)) return 1;
  const power = 10 ** Math.floor(Math.log10(value));
  for (const step of [1, 2, 2.5, 5, 10])
    if (step * power >= value) return step * power;
  return 10 * power;
}

// Индекс первой точки со временем не меньше t.
export function lowerBound(times, t) {
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

export function tracePath(context, xs, ys, move = true) {
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
