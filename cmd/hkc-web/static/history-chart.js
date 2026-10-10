// Чистые вычисления серий и построение SVG-графика; сетевые запросы выполняет модуль system.
const historyColors = {
  cpu: "var(--chart-1)",
  memory: "var(--chart-2)",
  disk: "var(--chart-3)",
};
// Линейно интерполирует серию по индексам до одинакового числа точек для плавного перехода. Это не пересчёт
// по неравномерным временным промежуткам.
function resampleSeries(points, key, count = 300) {
  if (!points.length) return [];
  if (points.length === 1)
    return Array(count).fill(Number(points[0][key]) || 0);
  return Array.from({ length: count }, (_, index) => {
    const position = (index * (points.length - 1)) / (count - 1);
    const left = Math.floor(position);
    const right = Math.min(points.length - 1, left + 1);
    const mix = position - left;
    return (
      (Number(points[left][key]) || 0) * (1 - mix) +
      (Number(points[right][key]) || 0) * mix
    );
  });
}
// Переводит проценты в координаты пути SVG 1000×200, ограничивая вертикальный диапазон.
function historyPath(values) {
  return values
    .map(
      (value, index) =>
        `${index ? "L" : "M"} ${((index / Math.max(1, values.length - 1)) * 1000).toFixed(2)} ${(195 - Math.min(100, Math.max(0, value)) * 1.9).toFixed(2)}`,
    )
    .join(" ");
}
// Повторно использует существующий SVG или создаёт линии ресурсов, точки и группу маркеров.
function ensureHistorySVG(chart) {
  let svg = chart.querySelector("svg");
  if (svg) return svg;
  chart.replaceChildren();
  const ns = "http://www.w3.org/2000/svg";
  svg = document.createElementNS(ns, "svg");
  svg.setAttribute("viewBox", "0 0 1000 200");
  svg.setAttribute("preserveAspectRatio", "none");
  const markers = document.createElementNS(ns, "g");
  markers.classList.add("history-markers");
  svg.append(markers);
  for (const [key, color] of Object.entries(historyColors)) {
    const path = document.createElementNS(ns, "path");
    path.dataset.series = key;
    path.setAttribute("fill", "none");
    path.setAttribute("stroke", color);
    path.setAttribute("stroke-width", "2");
    path.setAttribute("vector-effect", "non-scaling-stroke");
    const dot = document.createElementNS(ns, "circle");
    dot.dataset.dot = key;
    dot.setAttribute("r", "3.5");
    dot.setAttribute("fill", color);
    dot.setAttribute("vector-effect", "non-scaling-stroke");
    path.style.color = color;
    dot.style.color = color;
    svg.append(path, dot);
  }
  chart.append(svg);
  return svg;
}
// Отменяет прежний кадр и плавно меняет серию; при уменьшенном движении или скрытой странице рисует
// конечное состояние.
function morphHistorySeries(path, dot, next) {
  if (path.motionFrame) cancelAnimationFrame(path.motionFrame);
  const previous =
    path.motionValues?.length === next.length ? path.motionValues : next;
  const reduced = window.prefersReducedMotion?.() || document.hidden;
  const started = performance.now();
  const duration = 820;
  const draw = (now) => {
    const progress = reduced ? 1 : Math.min(1, (now - started) / duration);
    const eased = 1 - Math.pow(1 - progress, 3);
    const values = next.map(
      (value, index) => previous[index] + (value - previous[index]) * eased,
    );
    path.setAttribute("d", historyPath(values));
    dot.setAttribute("cx", "1000");
    dot.setAttribute(
      "cy",
      String(195 - Math.min(100, Math.max(0, values.at(-1))) * 1.9),
    );
    if (progress < 1) path.motionFrame = requestAnimationFrame(draw);
    else {
      path.motionValues = next;
      path.motionFrame = 0;
    }
  };
  path.motionFrame = requestAnimationFrame(draw);
}
export {
  historyColors,
  resampleSeries,
  historyPath,
  ensureHistorySVG,
  morphHistorySeries,
};
