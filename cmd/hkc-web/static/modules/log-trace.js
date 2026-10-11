// Разбор и отрисовка Python-traceback из записи журнала. Разбор — чистая функция без DOM
// (проверяется тестами), отрисовка собирает узлы без innerHTML: текст журнала — чужие данные.

const FRAME = /^\s*File "(.+)", line (\d+)(?:, in (.+))?\s*$/;
// Имя исключения с необязательным текстом: «ValueError: плохо», «asyncio.TimeoutError».
const EXCEPTION = /^([A-Za-z_][\w.]*)(?::\s?(.*))?$/;
const CHAIN = /^(During handling of the above exception|The above exception was the direct cause)/;
// Подчёркивания вида «^^^^^» и «~~~^^^» Python 3.11+ ставит под проблемной частью строки.
const MARKER = /^[\s^~]+$/;
// Кадры чужих библиотек и самого интерпретатора сворачиваются: ошибка обычно в своём коде.
const LIBRARY = /site-packages|dist-packages|[\\/]lib[\\/]python\d|^<frozen |[\\/]importlib[\\/]/;

const SUMMARY_MAX = 110;

// parseTrace раскладывает строки traceback на блоки (цепочка исключений даёт несколько).
// ok=false — формат не Python: вызывающий показывает строки как есть.
export function parseTrace(lines) {
  const blocks = [];
  let block = null;
  const open = (note = "") => {
    block = { note, frames: [], exception: null };
    blocks.push(block);
  };
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const text = line.trim();
    if (!text) continue;
    if (CHAIN.test(text)) {
      open(text.replace(/:$/, ""));
      continue;
    }
    if (text.startsWith("Traceback (most recent call last)")) {
      // Заголовок после «During handling…» продолжает уже открытый блок.
      if (!block || block.frames.length || block.exception) open();
      continue;
    }
    const frame = line.match(FRAME);
    if (frame) {
      if (!block) open();
      const entry = { file: frame[1], line: Number(frame[2]), func: frame[3] || "", code: "", own: !LIBRARY.test(frame[1]) };
      const next = lines[i + 1];
      if (next !== undefined && /^\s{3,}\S/.test(next) && !FRAME.test(next) && !MARKER.test(next)) {
        entry.code = next.trim();
        i += 1;
        if (lines[i + 1] !== undefined && MARKER.test(lines[i + 1])) i += 1;
      }
      block.frames.push(entry);
      continue;
    }
    // Строка без отступа после кадров — само исключение.
    if (block && block.frames.length && !block.exception && !/^\s/.test(line)) {
      const m = text.match(EXCEPTION);
      if (m) block.exception = { type: m[1], message: m[2] || "", raw: text };
    }
  }
  const ok = blocks.some((b) => b.frames.length || b.exception);
  const last = [...blocks].reverse().find((b) => b.exception);
  return { ok, blocks, summary: last ? last.exception.raw : "" };
}

// Где упало: «updates.py:88 в fetch» — последний кадр своего кода (или любой последний).
export function traceOrigin(lines) {
  const frames = parseTrace(lines).blocks.flatMap((b) => b.frames);
  const frame = [...frames].reverse().find((f) => f.own) || frames.at(-1);
  if (!frame) return "";
  const base = frame.file.split(/[\\/]/).pop();
  return `${base}:${frame.line}${frame.func ? ` в ${frame.func}` : ""}`;
}

// Короткая подпись для сложенного traceback: «ValueError: плохо».
export function traceSummary(lines) {
  const parsed = parseTrace(lines);
  if (!parsed.summary) return "";
  return parsed.summary.length > SUMMARY_MAX ? `${parsed.summary.slice(0, SUMMARY_MAX - 1)}…` : parsed.summary;
}

// «1 вызов», «2 вызова», «5 вызовов».
function callsWord(n) {
  const m10 = n % 10;
  const m100 = n % 100;
  if (m10 === 1 && m100 !== 11) return "вызов";
  if (m10 >= 2 && m10 <= 4 && (m100 < 12 || m100 > 14)) return "вызова";
  return "вызовов";
}

const node = (tag, cls, text) => {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text != null) n.textContent = text;
  return n;
};

function renderFrame(frame) {
  const row = node("div", `jr-fr${frame.own ? "" : " lib"}`);
  const head = node("div", "jr-fr-head");
  const parts = frame.file.split(/[\\/]/);
  const base = parts.pop();
  head.append(node("span", "jr-fr-func", frame.func || "<модуль>"));
  const loc = node("span", "jr-fr-loc");
  if (parts.length) loc.append(node("span", "jr-fr-dir", `${parts.join("/")}/`));
  loc.append(node("b", "", base), node("span", "jr-fr-line", `:${frame.line}`));
  loc.title = `${frame.file}:${frame.line}`;
  head.append(loc);
  row.append(head);
  if (frame.code) row.append(node("code", "jr-fr-code", frame.code));
  return row;
}

// renderTrace строит удобное представление: кадры своего кода видны, подряд идущие кадры
// библиотек свёрнуты, исключение выделено. Без распознанных кадров — исходные строки.
export function renderTrace(lines) {
  const parsed = parseTrace(lines);
  const root = node("div", "jr-tv");
  if (!parsed.ok) {
    root.append(node("pre", "jr-tv-raw", lines.join("\n")));
    return root;
  }
  for (const block of parsed.blocks) {
    if (!block.frames.length && !block.exception) continue;
    if (block.note) root.append(node("div", "jr-tv-note", block.note));
    const list = node("div", "jr-frames");
    for (let i = 0; i < block.frames.length; ) {
      if (block.frames[i].own) {
        list.append(renderFrame(block.frames[i++]));
        continue;
      }
      let end = i;
      while (end < block.frames.length && !block.frames[end].own) end++;
      const run = block.frames.slice(i, end);
      const fold = node("details", "jr-lib");
      fold.append(node("summary", "", `${run.length} ${callsWord(run.length)} библиотек`));
      run.forEach((f) => fold.append(renderFrame(f)));
      list.append(fold);
      i = end;
    }
    root.append(list);
    if (block.exception) {
      const exc = node("div", "jr-exc");
      exc.append(node("b", "", block.exception.type));
      if (block.exception.message) exc.append(`: ${block.exception.message}`);
      root.append(exc);
    }
  }
  return root;
}
