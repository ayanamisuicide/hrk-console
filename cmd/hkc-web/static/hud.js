// HUD-слой: фон-канвас, прицел, бегущая строка, загрузочная заставка и «шифрованный» вывод чисел.
// Скрипт не зависит от остальных модулей панели и работает на всех страницах.
(() => {
  const root = document.documentElement;
  const reduced = !!window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
  const touch = !!window.matchMedia?.("(hover: none)").matches;
  const ACCENTS = ["cyan", "magenta", "green", "amber"];
  const FX = ["min", "mid", "max"];
  const store = {
    get(key, fallback) {
      try {
        return localStorage.getItem(key) || fallback;
      } catch {
        return fallback;
      }
    },
    set(key, value) {
      try {
        localStorage.setItem(key, value);
      } catch {
        /* хранилище недоступно: настройка живёт до перезагрузки */
      }
    },
  };

  let fx = store.get("hkc-fx", reduced ? "min" : "mid");
  if (!FX.includes(fx)) fx = "mid";
  let accent = store.get("hkc-accent", "cyan");
  if (!ACCENTS.includes(accent)) accent = "cyan";
  root.dataset.fx = fx;
  if (accent !== "cyan") root.dataset.accent = accent;
  else delete root.dataset.accent;

  const el = (tag, className, text) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text != null) node.textContent = text;
    return node;
  };
  const $ = (selector) => document.querySelector(selector);

  // ---------- Цвета акцента для канваса ----------
  let rgb1 = [0, 229, 255];
  let rgb2 = [255, 43, 214];
  const readColors = () => {
    const style = getComputedStyle(root);
    const parse = (name, fallback) => {
      const hex = style.getPropertyValue(name).trim();
      return /^#[0-9a-f]{6}$/i.test(hex)
        ? [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16))
        : fallback;
    };
    rgb1 = parse("--hud-c1", rgb1);
    rgb2 = parse("--hud-c2", rgb2);
  };

  // ---------- Оверлеи ----------
  const bg = el("canvas");
  bg.id = "hud-bg";
  bg.setAttribute("aria-hidden", "true");
  const scan = el("div", "hud-scan");
  const vignette = el("div", "hud-vignette");
  const corners = el("div", "hud-corners");
  corners.append(el("span"), el("span"), el("span"), el("span"));
  for (const node of [scan, vignette, corners]) node.setAttribute("aria-hidden", "true");

  const tag = el("span", "hud-tag", "HKC//CORE");
  const marquee = el("div", "hud-marquee");
  const track = el("div", "hud-track");
  const segA = el("span", "hud-seg");
  const segB = el("span", "hud-seg");
  track.append(segA, segB);
  marquee.append(track);
  const fxButton = el("button");
  fxButton.type = "button";
  fxButton.title = "Интенсивность эффектов";
  const accentButton = el("button");
  accentButton.type = "button";
  accentButton.title = "Цвет интерфейса";
  const ticker = el("div", "hud-ticker");
  ticker.append(tag, marquee, accentButton, fxButton);

  const paintButtons = () => {
    fxButton.textContent = `FX: ${{ max: "МАКС", mid: "СРЕД", min: "МИН" }[fx]}`;
    accentButton.textContent = `◐ ${accent.toUpperCase()}`;
    const label = $("#theme-toggle span");
    if (label) label.textContent = `Акцент: ${accent.toUpperCase()}`;
    const admin = $("#admin-theme");
    if (admin) admin.textContent = `◐ ${accent.toUpperCase()}`;
  };

  const setAccent = (next) => {
    accent = next;
    if (accent === "cyan") delete root.dataset.accent;
    else root.dataset.accent = accent;
    store.set("hkc-accent", accent);
    readColors();
    paintButtons();
    drawStatic();
  };
  const cycleAccent = () => setAccent(ACCENTS[(ACCENTS.indexOf(accent) + 1) % ACCENTS.length]);
  const setFx = (next) => {
    fx = next;
    root.dataset.fx = fx;
    store.set("hkc-fx", fx);
    paintButtons();
    seed();
    restartLoop();
  };
  fxButton.addEventListener("click", () => setFx(FX[(FX.indexOf(fx) + 1) % FX.length]));
  accentButton.addEventListener("click", cycleAccent);
  // Прежняя кнопка темы теперь переключает цветовой акцент.
  document.addEventListener(
    "click",
    (event) => {
      if (!event.target.closest?.("#theme-toggle, #admin-theme")) return;
      event.stopPropagation();
      event.preventDefault();
      cycleAccent();
    },
    true,
  );

  // ---------- Фон: сетка-горизонт, частицы, шестнадцатеричный дождь ----------
  const ctx2d = bg.getContext("2d");
  let W = 0;
  let H = 0;
  let mouseX = 0.5;
  let mouseY = 0.5;
  let particles = [];
  let rain = [];
  let raf = 0;
  let last = 0;
  let t = 0;
  const HEX = "0123456789ABCDEF";

  const resize = () => {
    W = bg.width = Math.max(1, window.innerWidth);
    H = bg.height = Math.max(1, window.innerHeight);
    seed();
    drawStatic();
  };
  const seed = () => {
    const density = fx === "max" ? 1 : fx === "mid" ? 0.4 : 0.2;
    const count = Math.round(Math.min(110, (W * H) / 16000) * density);
    particles = Array.from({ length: count }, () => ({
      x: Math.random() * W,
      y: Math.random() * H,
      vx: (Math.random() - 0.5) * 0.35,
      vy: -0.1 - Math.random() * 0.45,
      r: 0.8 + Math.random() * 1.6,
    }));
    const columns = fx === "max" ? Math.round(W / 130) : 0;
    rain = Array.from({ length: columns }, () => ({
      x: Math.random() * W,
      y: Math.random() * H,
      speed: 0.7 + Math.random() * 1.8,
      len: 6 + Math.floor(Math.random() * 12),
    }));
  };
  const rgba = (c, a) => `rgba(${c[0]},${c[1]},${c[2]},${a})`;

  const frame = (dt) => {
    t += dt;
    ctx2d.clearRect(0, 0, W, H);
    const horizon = H * 0.58;
    const vx = W * (0.5 + (mouseX - 0.5) * 0.12);

    // Свечение горизонта
    const glow = ctx2d.createLinearGradient(0, horizon - 90, 0, horizon + 40);
    glow.addColorStop(0, rgba(rgb1, 0));
    glow.addColorStop(0.7, rgba(rgb1, 0.14));
    glow.addColorStop(1, rgba(rgb2, 0.05));
    ctx2d.fillStyle = glow;
    ctx2d.fillRect(0, horizon - 90, W, 130);

    // Перспективная сетка пола
    ctx2d.lineWidth = 1;
    ctx2d.strokeStyle = rgba(rgb1, fx === "max" ? 0.22 : 0.13);
    ctx2d.beginPath();
    for (let i = -14; i <= 14; i++) {
      ctx2d.moveTo(vx, horizon);
      ctx2d.lineTo(vx + i * (W / 6), H);
    }
    const scroll = (t / (fx === "max" ? 1800 : 6000)) % 1;
    for (let i = 0; i < 16; i++) {
      const z = (i + scroll) / 16;
      const y = horizon + (H - horizon) * z * z;
      ctx2d.moveTo(0, y);
      ctx2d.lineTo(W, y);
    }
    ctx2d.stroke();

    // Зеркальная сетка потолка
    ctx2d.strokeStyle = rgba(rgb2, 0.08);
    ctx2d.beginPath();
    for (let i = 0; i < 10; i++) {
      const z = (i + scroll) / 10;
      const y = horizon - horizon * z * z * 0.9;
      ctx2d.moveTo(0, y);
      ctx2d.lineTo(W, y);
    }
    ctx2d.stroke();

    // Частицы и связи
    const mx = mouseX * W;
    const my = mouseY * H;
    ctx2d.fillStyle = rgba(rgb1, fx === "max" ? 0.8 : 0.5);
    for (const p of particles) {
      p.x += p.vx;
      p.y += p.vy;
      if (p.y < -4) p.y = H + 4;
      if (p.x < -4) p.x = W + 4;
      if (p.x > W + 4) p.x = -4;
      ctx2d.fillRect(p.x, p.y, p.r, p.r);
    }
    if (fx === "max") {
      ctx2d.lineWidth = 0.6;
      for (let i = 0; i < particles.length; i++) {
        const a = particles[i];
        const dm = Math.hypot(a.x - mx, a.y - my);
        if (dm < 190) {
          ctx2d.strokeStyle = rgba(rgb2, (1 - dm / 190) * 0.7);
          ctx2d.beginPath();
          ctx2d.moveTo(a.x, a.y);
          ctx2d.lineTo(mx, my);
          ctx2d.stroke();
        }
        for (let j = i + 1; j < particles.length; j++) {
          const b = particles[j];
          const d = Math.hypot(a.x - b.x, a.y - b.y);
          if (d < 100) {
            ctx2d.strokeStyle = rgba(rgb1, (1 - d / 100) * 0.3);
            ctx2d.beginPath();
            ctx2d.moveTo(a.x, a.y);
            ctx2d.lineTo(b.x, b.y);
            ctx2d.stroke();
          }
        }
      }
    }

    // Шестнадцатеричный дождь
    ctx2d.font = "12px monospace";
    for (const column of rain) {
      column.y += column.speed;
      if (column.y - column.len * 14 > H) {
        column.y = -20;
        column.x = Math.random() * W;
      }
      for (let k = 0; k < column.len; k++) {
        ctx2d.fillStyle = rgba(k === 0 ? [255, 255, 255] : rgb2, k === 0 ? 0.9 : 0.4 * (1 - k / column.len));
        ctx2d.fillText(HEX[(Math.random() * 16) | 0], column.x, column.y - k * 14);
      }
    }

    // Периодический лучевой проход сверху вниз
    const beam = ((t / 7000) % 1.4) - 0.2;
    if (fx === "max" && beam > 0 && beam < 1) {
      const by = beam * H;
      const g = ctx2d.createLinearGradient(0, by - 40, 0, by + 2);
      g.addColorStop(0, rgba(rgb1, 0));
      g.addColorStop(1, rgba(rgb1, 0.22));
      ctx2d.fillStyle = g;
      ctx2d.fillRect(0, by - 40, W, 42);
    }
  };

  const drawStatic = () => {
    if (!W || (fx !== "min" && !reduced)) return;
    frame(0);
  };
  // Рисуем не чаще ~40 кадров в секунду и останавливаемся на скрытой вкладке.
  const loop = (now) => {
    raf = requestAnimationFrame(loop);
    const dt = now - last;
    if (dt < 25) return;
    last = now;
    fps = fps * 0.9 + (1000 / dt) * 0.1;
    frame(Math.min(dt, 100));
  };
  const restartLoop = () => {
    cancelAnimationFrame(raf);
    raf = 0;
    if (fx === "min" || reduced || document.hidden) {
      ctx2d.clearRect(0, 0, W, H);
      drawStatic();
      return;
    }
    last = performance.now();
    raf = requestAnimationFrame(loop);
  };
  let fps = 60;
  document.addEventListener("visibilitychange", restartLoop);
  window.addEventListener("resize", resize);
  window.addEventListener(
    "pointermove",
    (event) => {
      mouseX = event.clientX / Math.max(1, window.innerWidth);
      mouseY = event.clientY / Math.max(1, window.innerHeight);
    },
    { passive: true },
  );

  // ---------- Бегущая строка ----------
  const started = Date.now();
  const pad = (n) => String(n).padStart(2, "0");
  const renderTicker = () => {
    const now = new Date();
    const up = Math.floor((Date.now() - started) / 1000);
    const state = $("#status-label")?.textContent?.trim() || $("#public-state strong")?.textContent?.trim() || "—";
    const meta = $("#status-meta")?.textContent?.trim() || "—";
    const view = $("#view-title")?.textContent?.trim() || document.title;
    const version = $("#version")?.textContent?.trim() || "HKC";
    const parts = [
      ["SYS", state.toUpperCase()],
      ["LINK", meta.toUpperCase()],
      ["VIEW", view.toUpperCase()],
      ["UTC", `${pad(now.getUTCHours())}:${pad(now.getUTCMinutes())}:${pad(now.getUTCSeconds())}`],
      ["FPS", String(Math.round(Math.min(fps, 99))).padStart(2, "0")],
      ["SESSION", `${pad(Math.floor(up / 60))}:${pad(up % 60)}`],
      ["NODE", version.toUpperCase()],
    ];
    for (const seg of [segA, segB]) {
      seg.textContent = "";
      parts.forEach(([key, value], index) => {
        const b = el("b", "", value);
        seg.append(`${key} ▸ `, b, index < parts.length - 1 ? el("em", "", "  ◆  ") : "  ◆◆◆  ");
      });
    }
  };

  // ---------- Состояние бота → настроение интерфейса ----------
  const watchBot = () => {
    const card = $("#status-card") || $("#public-state");
    if (!card) return;
    const apply = () => {
      root.dataset.bot = card.classList.contains("online") ? "online" : "down";
    };
    apply();
    new MutationObserver(apply).observe(card, { attributes: true, attributeFilter: ["class"] });
  };

  // ---------- Прицел и клики ----------
  if (!touch) {
    const ret = el("div", "hud-ret");
    const dot = el("div", "hud-dot");
    ret.setAttribute("aria-hidden", "true");
    dot.setAttribute("aria-hidden", "true");
    document.body.append(ret, dot);
    let tx = -100;
    let ty = -100;
    let rx = -100;
    let ry = -100;
    window.addEventListener(
      "pointermove",
      (event) => {
        tx = event.clientX;
        ty = event.clientY;
        dot.style.transform = `translate(${tx}px, ${ty}px)`;
        ret.toggleAttribute(
          "data-hot",
          !!event.target.closest?.("a, button, input, select, summary, label, [role='button'], .module-row"),
        );
      },
      { passive: true },
    );
    const follow = () => {
      rx += (tx - rx) * 0.2;
      ry += (ty - ry) * 0.2;
      ret.style.transform = `translate(${rx}px, ${ry}px)`;
      requestAnimationFrame(follow);
    };
    follow();
  }
  window.addEventListener("pointerdown", (event) => {
    if (fx !== "max") return;
    const burst = el("div", "hud-burst");
    burst.style.left = `${event.clientX}px`;
    burst.style.top = `${event.clientY}px`;
    for (let i = 0; i < 8; i++) {
      const shard = el("i");
      shard.style.setProperty("--r", `${i * 45}deg`);
      burst.append(shard);
    }
    document.body.append(burst);
    setTimeout(() => burst.remove(), 800);
  });

  // ---------- Прожектор и наклон карточек ----------
  const SPOT = ".surface-card, .resource-card, .admin-section, .summary article, .bot-deck, .update-versions, .update-timeline, .update-hero, .service-card, .modules-total, .modules-map, .modules-counters button, .invite-card, .security-grid > *, .update-readiness, .public-status-card, .log-frame, .status-card, .sidebar, .shell > main";
  const TILT = ".resource-card, .modules-counters button, .security-grid > *, .invite-card";
  let active = null;
  let pending = null;
  const resetActive = () => {
    if (!active) return;
    for (const name of ["--mx", "--my", "--rx", "--ry"]) active.style.removeProperty(name);
    active = null;
  };
  if (!touch) {
    window.addEventListener(
      "pointermove",
      (event) => {
        if (fx === "min") return;
        const queued = pending !== null;
        pending = event;
        if (queued) return;
        requestAnimationFrame(() => {
          const e = pending;
          pending = null;
          if (!e) return;
          const target = e.target.closest?.(SPOT);
          if (target !== active) resetActive();
          if (!target) return;
          active = target;
          const box = target.getBoundingClientRect();
          const x = e.clientX - box.left;
          const y = e.clientY - box.top;
          target.style.setProperty("--mx", `${x}px`);
          target.style.setProperty("--my", `${y}px`);
          if (fx === "max" && target.matches(TILT)) {
            target.style.setProperty("--ry", `${((x / box.width - 0.5) * 8).toFixed(2)}deg`);
            target.style.setProperty("--rx", `${((0.5 - y / box.height) * 8).toFixed(2)}deg`);
          }
        });
      },
      { passive: true },
    );
    document.addEventListener("pointerleave", resetActive);
  }

  // ---------- «Шифрованный» вывод текста и чисел ----------
  const SCRAMBLE = "#view-title, .view-intro h3, .resource-heading strong, .modules-number strong, .modules-counters strong, .load-values strong, #line-count, #system-uptime, .admin-page-heading h2, .update-hero h2";
  const GLYPHS = "▓▒░█<>/\\|#$%&01ABCDEF";
  const known = new WeakMap();
  const busy = new WeakMap();
  const observer = new MutationObserver((records) => {
    if (fx !== "max" || reduced) return;
    for (const record of records) {
      const node = record.target.nodeType === 3 ? record.target.parentElement : record.target;
      const holder = node?.closest?.(SCRAMBLE);
      if (!holder || holder.children.length) continue;
      const text = holder.textContent;
      if (busy.has(holder)) {
        busy.get(holder).final = text;
        continue;
      }
      if (known.get(holder) === text || text.length > 28 || !text.trim()) {
        known.set(holder, text);
        continue;
      }
      scramble(holder, text);
    }
  });
  const scramble = (holder, final) => {
    const job = { final };
    busy.set(holder, job);
    const begin = performance.now();
    const duration = 420;
    const tick = (now) => {
      const progress = Math.min(1, (now - begin) / duration);
      const target = job.final;
      const reveal = Math.floor(progress * target.length);
      let out = "";
      for (let i = 0; i < target.length; i++) {
        const ch = target[i];
        out += i < reveal || /[\s—:.,%-]/.test(ch) ? ch : GLYPHS[(Math.random() * GLYPHS.length) | 0];
      }
      holder.textContent = out;
      observer.takeRecords();
      if (progress < 1) requestAnimationFrame(tick);
      else {
        holder.textContent = job.final;
        observer.takeRecords();
        known.set(holder, job.final);
        busy.delete(holder);
      }
    };
    requestAnimationFrame(tick);
  };

  // ---------- Загрузочная заставка ----------
  const boot = () => {
    let seen = false;
    try {
      seen = sessionStorage.getItem("hkc-booted") === "1";
      sessionStorage.setItem("hkc-booted", "1");
    } catch {
      seen = false;
    }
    if (seen || fx !== "max" || reduced) return;
    const screen = el("div", "hud-boot");
    const body = el("div");
    const pre = el("pre");
    const bar = el("div", "hud-boot-bar");
    const fill = el("i");
    bar.append(fill);
    body.append(pre, bar);
    screen.append(body);
    document.body.append(screen);
    const lines = [
      "HKC//CORE  BOOT SEQUENCE v2",
      "[ OK ] ИНИЦИАЛИЗАЦИЯ ЯДРА ............ ГОТОВО",
      "[ OK ] НЕЙРОННАЯ СВЯЗЬ ............... УСТАНОВЛЕНА",
      "[ OK ] КАНАЛ ТЕЛЕМЕТРИИ .............. АКТИВЕН",
      "[ OK ] МОДУЛИ HEROKU ................. СИНХРОНИЗАЦИЯ",
      "[ OK ] ИНТЕРФЕЙС HUD ................. ЗАГРУЖЕН",
      "ДОБРО ПОЖАЛОВАТЬ, ОПЕРАТОР.",
    ];
    let done = false;
    const finish = () => {
      if (done) return;
      done = true;
      screen.classList.add("is-out");
      setTimeout(() => screen.remove(), 600);
      window.removeEventListener("keydown", finish);
      screen.removeEventListener("pointerdown", finish);
    };
    window.addEventListener("keydown", finish);
    screen.addEventListener("pointerdown", finish);
    let index = 0;
    const next = () => {
      if (done) return;
      if (index >= lines.length) return setTimeout(finish, 280);
      pre.textContent += `${lines[index].replace("[ OK ]", "[ OK ]")}\n`;
      index += 1;
      fill.style.width = `${Math.round((index / lines.length) * 100)}%`;
      setTimeout(next, 120 + Math.random() * 90);
    };
    next();
  };

  // ---------- Пасхалка ----------
  const code = ["ArrowUp", "ArrowUp", "ArrowDown", "ArrowDown", "ArrowLeft", "ArrowRight", "ArrowLeft", "ArrowRight", "b", "a"];
  let progress = 0;
  window.addEventListener("keydown", (event) => {
    progress = event.key === code[progress] ? progress + 1 : event.key === code[0] ? 1 : 0;
    if (progress === code.length) {
      progress = 0;
      root.classList.add("hud-rave");
      setTimeout(() => root.classList.remove("hud-rave"), 6000);
    }
  });

  // ---------- Запуск ----------
  const start = () => {
    document.body.prepend(bg);
    document.body.append(scan, vignette, corners, ticker);
    readColors();
    paintButtons();
    resize();
    restartLoop();
    renderTicker();
    setInterval(renderTicker, 1000);
    watchBot();
    observer.observe(document.body, { childList: true, characterData: true, subtree: true });
    boot();
  };
  if (document.body) start();
  else document.addEventListener("DOMContentLoaded", start, { once: true });
})();
