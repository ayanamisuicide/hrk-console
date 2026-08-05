import './style.css';

import {
    Bootstrap, SetFilter, SetShowDebug, CycleMinLevel, SetWatchdog,
    StartBot, StopBot, RestartBot, ClearLog,
} from '../wailsjs/go/main/App';
import { EventsOn, WindowSetTitle } from '../wailsjs/runtime/runtime';

const LEVEL_DEBUG = 0, LEVEL_INFO = 1, LEVEL_WARNING = 2, LEVEL_ERROR = 3, LEVEL_CRITICAL = 4;
const BADGES = ['DBG', 'INF', 'WRN', 'ERR', 'CRT'];
const BADGE_CLASS = ['dbg', 'inf', 'wrn', 'err', 'crt'];
const MODULE_COLORS = 8; // --mod-1..--mod-8 в style.css

// ─── разметка ────────────────────────────────────────────────────────────

document.querySelector('#app').innerHTML = `
  <div class="topbar">
    <span class="brand">HEROKU</span>
    <span class="version" id="hkc-version"></span>
    <span class="status-pill down" id="status-pill">
      <span class="status-dot"></span><span id="status-text">…</span>
    </span>
    <div class="spacer"></div>
    <div class="controls">
      <button id="btn-debug">debug</button>
      <button id="btn-level">порог: всё</button>
      <button id="btn-clear" title="Очистить экран лога — файл на диске не трогается">Очистить</button>
    </div>
  </div>
  <div class="stream-issue" id="stream-issue" style="display:none"></div>
  <div class="body">
    <div class="sidebar">
      <h3>Проблемы</h3>
      <div class="problem-counts">
        <span class="warn">⚠ <span id="count-warn">0</span></span>
        <span class="err">✗ <span id="count-err">0</span></span>
      </div>
      <div id="threshold-badge" class="threshold-badge" style="display:none"></div>
      <div id="filter-badge" class="filter-badge" style="display:none"></div>

      <h3>Поток</h3>
      <div class="sparkline" id="sparkline"></div>

      <h3>Модули</h3>
      <div id="module-list"></div>

      <div style="flex:1"></div>

      <h3>Процесс</h3>
      <div class="proc-block" id="proc-block">—</div>
      <div class="proc-controls">
        <button id="btn-start" class="primary">Start</button>
        <button id="btn-restart">Restart</button>
        <button id="btn-stop" class="danger">Stop</button>
      </div>
      <button id="btn-watchdog" class="proc-watchdog-btn">auto-restart</button>
      <div class="watchdog-note" id="watchdog-note"></div>
      <div class="gui-version" id="gui-version"></div>
    </div>
    <div class="log-pane">
      <div class="log-scroll" id="log-scroll"></div>
      <div class="log-footer">
        <input type="text" id="filter-input" placeholder="подстрока для поиска… (re:шаблон — регулярное выражение)" autocomplete="off" />
      </div>
    </div>
  </div>
`;

const el = (id) => document.getElementById(id);
const statusPill = el('status-pill');
const statusText = el('status-text');
const streamIssueBox = el('stream-issue');
const countWarn = el('count-warn');
const countErr = el('count-err');
const thresholdBadge = el('threshold-badge');
const filterBadge = el('filter-badge');
const sparkline = el('sparkline');
const moduleList = el('module-list');
const procBlock = el('proc-block');
const watchdogNote = el('watchdog-note');
const logScroll = el('log-scroll');
const filterInput = el('filter-input');
const btnStart = el('btn-start');
const btnRestart = el('btn-restart');
const btnStop = el('btn-stop');
const btnDebug = el('btn-debug');
const btnLevel = el('btn-level');
const btnWatchdog = el('btn-watchdog');
const btnClear = el('btn-clear');

el('gui-version').textContent = 'gui dev';

// ─── состояние ───────────────────────────────────────────────────────────

let uiState = { showDebug: false, showSidebar: true, minLevel: LEVEL_DEBUG, watchdog: false };
let lastRowEl = null;   // DOM-узел последней записи — для схлопывания повторов
let moduleStats = new Map(); // имя → {count, warn, err}
let activityBuckets = new Array(40).fill(0);
let activityNow = 0;
// warnCount/errCount считаются нарастающим итогом по КАЖДОМУ событию, а не
// по числу строк на экране: одно и то же предупреждение, повторившееся 10
// раз и схлопнутое в одну строку "×10", всё равно означает 10 срабатываний.
let warnCount = 0, errCount = 0;

function moduleColor(name) {
    let h = 2166136261;
    for (let i = 0; i < name.length; i++) {
        h ^= name.charCodeAt(i);
        h = Math.imul(h, 16777619);
    }
    return `var(--mod-${(Math.abs(h) % MODULE_COLORS) + 1})`;
}

// ─── рендер одной записи ────────────────────────────────────────────────

function buildRow(rec, zebra) {
    const row = document.createElement('div');
    row.className = 'row' + (zebra ? ' zebra' : '');

    const time = document.createElement('span');
    time.className = 'time';
    time.textContent = rec.time || '';
    row.appendChild(time);

    const badge = document.createElement('span');
    badge.className = 'badge ' + BADGE_CLASS[rec.level];
    badge.textContent = BADGES[rec.level];
    row.appendChild(badge);

    const mod = document.createElement('span');
    mod.className = 'module';
    mod.style.color = moduleColor(rec.module);
    mod.textContent = rec.module;
    row.appendChild(mod);

    const msg = document.createElement('span');
    const msgClass = rec.level === LEVEL_CRITICAL ? 'crt' : rec.level === LEVEL_ERROR ? 'err' : rec.level === LEVEL_WARNING ? 'wrn' : '';
    msg.className = 'msg' + (msgClass ? ' ' + msgClass : '');
    (rec.lines || ['']).forEach((line, i) => {
        if (i > 0) {
            const br = document.createElement('div');
            const arrow = document.createElement('span');
            arrow.className = 'continuation-arrow';
            arrow.textContent = '↳ ';
            br.appendChild(arrow);
            br.appendChild(document.createTextNode(line));
            msg.appendChild(br);
        } else {
            msg.appendChild(document.createTextNode(line));
        }
    });
    row.appendChild(msg);

    const count = document.createElement('span');
    count.className = 'count';
    if (rec.count > 1) {
        count.textContent = '×' + rec.count;
    } else {
        count.style.visibility = 'hidden';
    }
    row.appendChild(count);

    row._rec = rec;
    return row;
}

function updateRowCount(rowEl, rec) {
    rowEl._rec = rec;
    rowEl.querySelector('.time').textContent = rec.time || '';
    const count = rowEl.querySelector('.count');
    count.textContent = '×' + rec.count;
    count.style.visibility = 'visible';
}

function isPinnedToBottom() {
    return logScroll.scrollHeight - logScroll.scrollTop - logScroll.clientHeight < 24;
}

// DOM разрастается неограниченно на живом потоке — держим тот же порядок
// величины, что и кольцевой буфер бэкенда, а не всю историю сессии разом.
const MAX_ROWS = 4000;
function trimLog() {
    while (logScroll.children.length > MAX_ROWS) {
        logScroll.removeChild(logScroll.firstChild);
    }
}

function trackModule(rec) {
    const s = moduleStats.get(rec.module) || { count: 0, warn: 0, err: 0 };
    s.count += 1;
    if (rec.level === LEVEL_WARNING) s.warn += 1;
    if (rec.level >= LEVEL_ERROR) s.err += 1;
    moduleStats.set(rec.module, s);
}

function renderAll(recs) {
    logScroll.innerHTML = '';
    moduleStats = new Map();
    warnCount = 0;
    errCount = 0;
    const frag = document.createDocumentFragment();
    recs.forEach((rec, i) => frag.appendChild(buildRow(rec, i % 2 === 1)));
    logScroll.appendChild(frag); // один append, а не N — одна перекомпоновка вместо N
    recs.forEach((rec) => {
        trackModule(rec);
        // rec.count — сколько повторов схлопнуто в эту строку; каждый считался
        // отдельным срабатыванием ещё до схлопывания.
        const n = rec.count > 1 ? rec.count : 1;
        if (rec.warn) warnCount += n;
        else if (rec.err) errCount += n;
    });
    lastRowEl = logScroll.lastElementChild;
    trimLog();
    logScroll.scrollTop = logScroll.scrollHeight;
    renderSidebar();
}

// ─── события с бэкенда ───────────────────────────────────────────────────
//
// Строки лога приходят пачками (бот может выдать десятки штук за секунду).
// Раньше каждая строка сразу лезла в DOM и читала scrollHeight/scrollTop,
// чтобы понять, прижат ли скролл к низу, — чтение сразу после чужой записи
// в DOM заставляет браузер пересчитывать layout на месте (forced reflow), и
// на пачке из сотен строк это превращалось в заметное подвисание. Теперь
// события только копятся в очереди, а весь накопленный пакет применяется
// разом по кадру: одно чтение layout и одна запись scrollTop на пакет,
// а не на строку.
let pendingTail = [];
let flushScheduled = false;

// flushChunk — сколько строк пачки разбирать за один кадр. Если разом
// прилетело больше — не пытаемся впихнуть всё в один кадр (это и была бы
// та же самая подвисающая пауза, просто отложенная), а достраиваем
// остаток следующими кадрами: пользователь видит прогресс, а не паузу.
const flushChunk = 300;

EventsOn('log-tail', (evt) => {
    pendingTail.push(evt);
    if (!flushScheduled) {
        flushScheduled = true;
        requestAnimationFrame(flushTail);
    }
});

function flushTail() {
    flushScheduled = false;
    if (pendingTail.length === 0) return;

    const batch = pendingTail.splice(0, flushChunk);
    const pinned = isPinnedToBottom(); // единственное чтение layout на весь пакет
    const frag = document.createDocumentFragment();
    let appended = 0;

    for (const evt of batch) {
        if (evt.replace && lastRowEl) {
            const prev = lastRowEl._rec;
            if (prev) { prev.warn = evt.rec.warn; prev.err = evt.rec.err; }
            updateRowCount(lastRowEl, evt.rec);
        } else {
            const zebra = (logScroll.children.length + appended) % 2 === 1;
            const rowEl = buildRow(evt.rec, zebra);
            frag.appendChild(rowEl);
            lastRowEl = rowEl;
            appended++;
        }
        trackModule(evt.rec);
        activityNow++;
        // Каждое событие — одно срабатывание, даже если оно легло повтором в
        // уже показанную строку (replace): счётчик не про число строк на
        // экране, а про то, сколько раз это реально произошло.
        if (evt.rec.warn) warnCount++;
        else if (evt.rec.err) errCount++;
    }

    logScroll.appendChild(frag);
    trimLog();
    if (pinned) logScroll.scrollTop = logScroll.scrollHeight; // одна запись в конце пакета
    renderSidebar(); // один раз на пакет, а не на каждую строку

    if (pendingTail.length > 0) {
        flushScheduled = true;
        requestAnimationFrame(flushTail);
    }
}

EventsOn('status', (st) => {
    uiState.watchdog = st.watchdog;
    renderStatus(st);
});

EventsOn('notice', (text) => {
    showBanner(text, true);
});

// ─── шапка / статус ──────────────────────────────────────────────────────

function renderStatus(st) {
    el('hkc-version').textContent = 'hkc ' + (st.hkcVersion || 'dev');

    statusPill.classList.remove('live', 'down', 'warn');
    if (st.streamIssue) {
        statusPill.classList.add('warn');
        statusText.textContent = '⚠ лог не читается';
        streamIssueBox.style.display = '';
        streamIssueBox.textContent = st.streamIssue;
    } else {
        streamIssueBox.style.display = 'none';
        if (st.alive) {
            statusPill.classList.add('live');
            statusText.textContent = `live · ${st.botVersion || '?'} · ${st.uptime}`;
        } else {
            statusPill.classList.add('down');
            statusText.textContent = 'не запущен';
        }
    }

    procBlock.innerHTML = st.alive
        ? `<span class="live-label">● live</span><br/>pid ${st.pid}<br/>${st.uptime}`
        : `<span class="down-label">○ не запущен</span>`;

    if (st.restarting) {
        watchdogNote.textContent = '⟳ перезапускаю…';
        watchdogNote.className = 'watchdog-note restarting';
    } else if (st.watchdog) {
        watchdogNote.textContent = '⟳ авто-перезапуск включён';
        watchdogNote.className = 'watchdog-note on';
    } else {
        watchdogNote.textContent = '';
        watchdogNote.className = 'watchdog-note';
    }
    btnWatchdog.classList.toggle('toggled', !!st.watchdog);

    // Заголовок ОКНА, а не вкладки браузера — Wails не тянет document.title
    // в нативный тайтлбар сам, нужен его собственный runtime-вызов.
    WindowSetTitle(st.alive ? `Heroku · ⚠ ${countWarn.textContent} ✗ ${countErr.textContent}` : 'Heroku · не запущен');
}

function showBanner(text, alert) {
    const b = document.createElement('div');
    b.className = 'banner' + (alert ? ' alert' : '');
    b.textContent = text;
    const pinned = isPinnedToBottom();
    logScroll.appendChild(b);
    if (pinned) logScroll.scrollTop = logScroll.scrollHeight;
}

// ─── панель ──────────────────────────────────────────────────────────────

function renderSidebar() {
    countWarn.textContent = warnCount;
    countErr.textContent = errCount;

    if (uiState.minLevel === LEVEL_WARNING) {
        thresholdBadge.style.display = '';
        thresholdBadge.textContent = 'порог: warning+';
    } else if (uiState.minLevel === LEVEL_ERROR) {
        thresholdBadge.style.display = '';
        thresholdBadge.textContent = 'порог: error+';
    } else {
        thresholdBadge.style.display = 'none';
    }

    if (filterInput.value) {
        filterBadge.style.display = '';
        filterBadge.textContent = '/' + filterInput.value;
    } else {
        filterBadge.style.display = 'none';
    }

    // ─── модули: громче всех — сверху, максимум 7, как в TUI ───
    const stats = [...moduleStats.entries()]
        .map(([name, s]) => ({ name, ...s }))
        .sort((a, b) => b.count - a.count)
        .slice(0, 7);
    const peak = stats.length ? stats[0].count : 1;
    moduleList.innerHTML = '';
    for (const m of stats) {
        const row = document.createElement('div');
        row.className = 'module-row';
        const name = document.createElement('span');
        name.className = 'module-name' + (m.err > 0 ? ' has-err' : m.warn > 0 ? ' has-warn' : '');
        name.style.color = (m.err > 0 || m.warn > 0) ? '' : moduleColor(m.name);
        name.textContent = m.name;
        name.title = m.name;
        const meter = document.createElement('span');
        meter.className = 'meter';
        const mask = document.createElement('span');
        mask.className = 'mask';
        mask.style.width = (100 - Math.round((m.count / peak) * 100)) + '%';
        meter.appendChild(mask);
        const count = document.createElement('span');
        count.className = 'module-count';
        count.textContent = m.count;
        row.append(name, meter, count);
        moduleList.appendChild(row);
    }

    // ─── поток: спарклайн по последним секундам ───
    sparkline.innerHTML = '';
    const peakA = Math.max(1, ...activityBuckets);
    for (const v of activityBuckets) {
        const bar = document.createElement('span');
        const pct = Math.max(6, Math.round((v / peakA) * 100));
        bar.className = 'bar' + (v > 0 && pct > 70 ? ' hot' : '');
        bar.style.height = pct + '%';
        sparkline.appendChild(bar);
    }
}

setInterval(() => {
    activityBuckets = [...activityBuckets.slice(1), activityNow];
    activityNow = 0;
    renderSidebar();
}, 1000);

// ─── управление ──────────────────────────────────────────────────────────

btnStart.addEventListener('click', () => StartBot());
btnRestart.addEventListener('click', () => RestartBot());
btnStop.addEventListener('click', () => StopBot());

btnDebug.addEventListener('click', async () => {
    uiState.showDebug = !uiState.showDebug;
    btnDebug.classList.toggle('toggled', uiState.showDebug);
    renderAll(await SetShowDebug(uiState.showDebug));
});

btnLevel.addEventListener('click', async () => {
    const recs = await CycleMinLevel();
    uiState.minLevel = uiState.minLevel === LEVEL_WARNING ? LEVEL_ERROR
        : uiState.minLevel === LEVEL_ERROR ? LEVEL_DEBUG : LEVEL_WARNING;
    btnLevel.textContent = uiState.minLevel === LEVEL_WARNING ? 'порог: warning+'
        : uiState.minLevel === LEVEL_ERROR ? 'порог: error+' : 'порог: всё';
    renderAll(recs);
});

btnWatchdog.addEventListener('click', () => {
    uiState.watchdog = !uiState.watchdog;
    btnWatchdog.classList.toggle('toggled', uiState.watchdog);
    SetWatchdog(uiState.watchdog);
});

btnClear.addEventListener('click', () => {
    // Файл на диске не трогаем — только буфер и экран, поэтому чистим сразу,
    // не дожидаясь ответа бэкенда, и на всякий случай гасим то, что уже
    // стояло в очереди на отрисовку (иначе недорисованный всплеск вернул бы
    // старые строки сразу после очистки).
    pendingTail = [];
    flushScheduled = false;
    activityBuckets = activityBuckets.map(() => 0);
    activityNow = 0;
    renderAll([]);
    ClearLog();
});

let filterDebounce = null;
filterInput.addEventListener('input', () => {
    clearTimeout(filterDebounce);
    filterDebounce = setTimeout(async () => {
        renderAll(await SetFilter(filterInput.value));
    }, 150);
});

// ─── старт ───────────────────────────────────────────────────────────────

Bootstrap().then((boot) => {
    uiState = boot.uiState;
    btnDebug.classList.toggle('toggled', uiState.showDebug);
    btnWatchdog.classList.toggle('toggled', uiState.watchdog);
    btnLevel.textContent = uiState.minLevel === LEVEL_WARNING ? 'порог: warning+'
        : uiState.minLevel === LEVEL_ERROR ? 'порог: error+' : 'порог: всё';
    renderStatus(boot.status);
    renderAll(boot.records);
});
