// Поток SSE через fetch: EventSource не умеет передавать заголовок Authorization, а мини-приложение
// входит сессией в заголовке. Повторяет ту часть EventSource, которой пользуется журнал: onopen,
// onmessage, onerror, именованные события через addEventListener и close; переподключается сам.
export function openEventStream(url, headers) {
  const listeners = new Map();
  const stream = { onopen: null, onmessage: null, onerror: null, closed: false };
  let controller;

  stream.addEventListener = (type, fn) => {
    if (!listeners.has(type)) listeners.set(type, []);
    listeners.get(type).push(fn);
  };
  stream.close = () => {
    stream.closed = true;
    controller?.abort();
  };

  function dispatch(type, data) {
    const event = { type, data };
    if (type === "message") stream.onmessage?.(event);
    for (const fn of listeners.get(type) || []) fn(event);
  }

  // Разбирает текст потока на события: строки «event:» и «data:», пустая строка — конец события.
  function feed(buffer) {
    let rest = buffer;
    let cut;
    while ((cut = rest.indexOf("\n\n")) >= 0) {
      const block = rest.slice(0, cut);
      rest = rest.slice(cut + 2);
      let type = "message";
      const data = [];
      for (const line of block.split("\n")) {
        if (line.startsWith(":")) continue;
        if (line.startsWith("event:")) type = line.slice(6).trim();
        else if (line.startsWith("data:")) data.push(line.slice(5).replace(/^ /, ""));
      }
      if (data.length) dispatch(type, data.join("\n"));
    }
    return rest;
  }

  async function run() {
    while (!stream.closed) {
      controller = new AbortController();
      try {
        const response = await fetch(url, { headers: headers(), signal: controller.signal, cache: "no-store" });
        if (!response.ok || !response.body) throw new Error(`HTTP ${response.status}`);
        stream.onopen?.({ type: "open" });
        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer = feed((buffer + decoder.decode(value, { stream: true })).replace(/\r\n?/g, "\n"));
        }
      } catch (_) {
        /* Обрыв или ошибка: ниже сообщим и переподключимся. */
      }
      if (stream.closed) return;
      stream.onerror?.({ type: "error" });
      await new Promise((resolve) => setTimeout(resolve, 3000));
    }
  }

  run();
  return stream;
}
