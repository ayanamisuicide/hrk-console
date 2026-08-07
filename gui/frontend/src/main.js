import './style.css';

import {
    Bootstrap, SetFilter, SetShowDebug, CycleMinLevel, SetWatchdog, SetShowSidebar,
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
    <div class="brand-group">
      <span class="brand">HEROKU</span>
      <span class="version" id="hkc-version"></span>
    </div>
    <span class="status-pill down" id="status-pill">
      <span class="status-dot"></span><span id="status-text">…</span>
    </span>
    <div class="spacer"></div>
    <div class="controls">
      <button id="btn-debug">debug</button>
      <button id="btn-level">порог: всё</button>
      <button id="btn-clear" title="Очистить экран лога — файл на диске не трогается">Очистить</button>
      <button id="btn-help" title="Горячие клавиши">?</button>
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

      <h3>Модули <span class="h3-total" id="module-total"></span></h3>
      <div class="module-list" id="module-list"></div>

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
  <div class="help-overlay" id="help-overlay" style="display:none">
    <div class="help-card">
      <h2>Горячие клавиши</h2>
      <table>
        <tr><td><kbd>d</kbd></td><td>показать/скрыть строки уровня DEBUG</td></tr>
        <tr><td><kbd>s</kbd></td><td>показать/скрыть панель</td></tr>
        <tr><td><kbd>w</kbd></td><td>порог показа по кругу: всё → warning+ → error+</td></tr>
        <tr><td><kbd>a</kbd></td><td>авто-перезапуск бота при падении</td></tr>
        <tr><td><kbd>/</kbd></td><td>поиск: подстрока или <code>re:шаблон</code></td></tr>
        <tr><td><kbd>n</kbd> <kbd>N</kbd></td><td>к следующей / предыдущей проблеме (warning и выше)</td></tr>
        <tr><td><kbd>r</kbd> <kbd>R</kbd></td><td>к следующему / предыдущему перезапуску</td></tr>
        <tr><td><kbd>g</kbd> <kbd>G</kbd></td><td>в начало / в конец лога</td></tr>
        <tr><td><kbd>клик</kbd></td><td>по модулю в панели — фильтр по нему, повторный клик снимает</td></tr>
        <tr><td><kbd>Esc</kbd></td><td>закрыть справку, снять фильтр</td></tr>
      </table>
      <p class="help-foot">Те же клавиши, что в консольной версии <code>hkc</code>.</p>
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
const moduleTotal = el('module-total');
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
const btnHelp = el('btn-help');
const helpOverlay = el('help-overlay');
const sidebar = document.querySelector('.sidebar');

// setProjectVersion — номер самого проекта (hrk-console), а не бота: тот
// виден отдельно в status-pill ("live · Heroku X.X.X · ..."). Имя и номер —
// разными span'ами, чтобы номер был заметнее тусклой подписи рядом с ним.
function setProjectVersion(v) {
    el('gui-version').innerHTML = '<span class="gv-name">hrk-console</span><span class="gv-num">' + v + '</span>';
}
setProjectVersion('dev');

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
    // problem — метка для прыжков n/N. Ставится по уровню записи, как в TUI:
    // продолжения трейсбека наследуют уровень родителя, поэтому длинная
    // ошибка остаётся одной целью прыжка, а не десятком подряд.
    row.className = 'row' + (zebra ? ' zebra' : '') + (rec.level >= LEVEL_WARNING ? ' problem' : '');

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

// trackModule учитывает запись в статистике панели.
//
// Продолжения (трейсбек, дамп) приходят с пустым модулем — они принадлежат
// записи выше, и считать их отдельным «модулем без имени» значит выдумать
// самого шумного участника из ничего. TUI их пропускает, здесь тоже.
//
// n — сколько срабатываний схлопнуто в эту запись: при пересборке истории
// одна строка «×50» означает полсотни событий, а не одно.
function trackModule(rec, n = 1) {
    if (!rec.module) return;
    const s = moduleStats.get(rec.module) || { count: 0, warn: 0, err: 0 };
    s.count += n;
    if (rec.level === LEVEL_WARNING) s.warn += n;
    if (rec.level >= LEVEL_ERROR) s.err += n;
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
        // rec.count — сколько повторов схлопнуто в эту строку; каждый считался
        // отдельным срабатыванием ещё до схлопывания.
        const n = rec.count > 1 ? rec.count : 1;
        trackModule(rec, n);
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
    setProjectVersion(st.hkcVersion || 'dev');

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
        ? `<span class="live-label">● live</span><span class="proc-meta">pid ${st.pid} · ${st.uptime}</span>`
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

    // ─── модули: громче всех — сверху, список полный (не 7, как раньше) —
    // сама область прокручивается своим скроллом, чтобы длинный список не
    // выталкивал блок «Процесс» с Start/Stop за пределы окна.
    const stats = [...moduleStats.entries()]
        .map(([name, s]) => ({ name, ...s }))
        .sort((a, b) => b.count - a.count);
    const peak = stats.length ? stats[0].count : 1;
    moduleTotal.textContent = stats.length ? '· ' + stats.length : '';
    moduleList.innerHTML = '';
    const active = filterInput.value;
    for (const m of stats) {
        const row = document.createElement('div');
        // Клик по модулю ставит его имя обычным фильтром — отдельного
        // «режима модуля» нет, как и в TUI: снимается он тем же способом,
        // что любой другой фильтр, и объяснять две механики не нужно.
        row.className = 'module-row clickable' + (m.name === active ? ' active' : '');
        row.title = m.name === active ? 'снять фильтр' : 'фильтр по модулю ' + m.name;
        row.addEventListener('click', () => applyFilter(m.name === active ? '' : m.name));

        // Точка — состояние модуля (проблема ярче самого частого случая, что
        // это просто разные модули): свой цвет в норме, warn/err — цветом
        // проблемы. Полоса ниже красит объём тем же цветом модуля всегда —
        // так объём и состояние не спорят за один и тот же сигнал.
        const dot = document.createElement('span');
        dot.className = 'module-dot';
        dot.style.background = m.err > 0 ? 'var(--critical)' : m.warn > 0 ? 'var(--warning)' : moduleColor(m.name);
        row.appendChild(dot);

        const name = document.createElement('span');
        name.className = 'module-name' + (m.err > 0 ? ' has-err' : m.warn > 0 ? ' has-warn' : '');
        name.textContent = m.name;
        row.appendChild(name);

        const meter = document.createElement('span');
        meter.className = 'meter';
        const fill = document.createElement('span');
        fill.className = 'fill';
        fill.style.width = Math.round((m.count / peak) * 100) + '%';
        fill.style.background = moduleColor(m.name);
        meter.appendChild(fill);
        row.appendChild(meter);

        const count = document.createElement('span');
        count.className = 'module-count';
        count.textContent = m.count;
        row.appendChild(count);

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

// ─── прыжки по логу ──────────────────────────────────────────────────────
//
// Ищем ближайшую отметку в направлении прыжка относительно текущей позиции
// прокрутки — то же правило, что у n/N и r/R в TUI. Упёрлись в край —
// уезжаем в конец (или в начало), а не замираем молча на месте: иначе
// нажатие выглядит как зависшая клавиша.
function jumpTo(selector, dir) {
    const marks = [...logScroll.querySelectorAll(selector)];
    if (marks.length === 0) {
        flashHint(dir > 0 ? 'дальше отметок нет' : 'выше отметок нет');
        return;
    }
    const cur = logScroll.scrollTop;
    // +1/-1 — чтобы повторное нажатие уходило дальше, а не залипало на той
    // же отметке, к которой мы только что прокрутились.
    const target = dir > 0
        ? marks.find((m) => m.offsetTop > cur + 1)
        : [...marks].reverse().find((m) => m.offsetTop < cur - 1);
    logScroll.scrollTop = target ? target.offsetTop : (dir > 0 ? logScroll.scrollHeight : 0);
}

let hintTimer = null;
function flashHint(text) {
    let hint = el('jump-hint');
    if (!hint) {
        hint = document.createElement('div');
        hint.id = 'jump-hint';
        hint.className = 'jump-hint';
        document.querySelector('.log-pane').appendChild(hint);
    }
    hint.textContent = text;
    hint.classList.add('visible');
    clearTimeout(hintTimer);
    hintTimer = setTimeout(() => hint.classList.remove('visible'), 1200);
}

// ─── управление ──────────────────────────────────────────────────────────

// Перезапуск отмечается в самом логе: без этой строки падение с автоподъёмом
// и ручной рестарт выглядят в истории одинаково — просто обрыв и новый старт.
// Заодно это цель для прыжков r/R.
async function withBanner(action, label) {
    const res = await action();
    if (res && res.ok) {
        const time = new Date().toTimeString().slice(0, 8);
        showBanner(`⟳  ${label}  ·  ${time}  ·  ${res.message}`, false);
    } else if (res) {
        showBanner(`✗  ${label}: ${res.message}`, true);
    }
}

btnStart.addEventListener('click', () => withBanner(StartBot, 'ЗАПУСК'));
btnRestart.addEventListener('click', () => withBanner(RestartBot, 'ПЕРЕЗАПУСК'));
btnStop.addEventListener('click', () => withBanner(StopBot, 'ОСТАНОВКА'));

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

// applyFilter ставит фильтр помимо ввода — кликом по модулю или Esc. Правит
// и поле ввода тоже: оно показывает действующий фильтр, и разойтись с
// реальностью ему нельзя.
async function applyFilter(text) {
    clearTimeout(filterDebounce); // отменяем ещё не сработавший ввод, иначе он вернёт старое
    filterInput.value = text;
    renderAll(await SetFilter(text));
}

// ─── горячие клавиши ─────────────────────────────────────────────────────
//
// Те же буквы, что в TUI: заглавная — то же действие в обратную сторону.
// Пока курсор в поле поиска, клавиши не перехватываются — там печатают
// текст, а не командуют.
document.addEventListener('keydown', (e) => {
    if (e.ctrlKey || e.metaKey || e.altKey) return;

    if (document.activeElement === filterInput) {
        if (e.key === 'Escape') {
            filterInput.blur();
            if (filterInput.value) applyFilter('');
        }
        return;
    }

    if (helpOverlay.style.display === '' && e.key !== '?') {
        // Любая клавиша закрывает справку — она перекрывает лог, и первое
        // желание после прочтения именно такое.
        toggleHelp(false);
        if (e.key === 'Escape') return;
    }

    switch (e.key) {
        case 'd': btnDebug.click(); break;
        case 'w': btnLevel.click(); break;
        case 'a': btnWatchdog.click(); break;
        case 's': toggleSidebar(); break;
        case '?': toggleHelp(); break;
        case 'n': jumpTo('.row.problem', 1); break;
        case 'N': jumpTo('.row.problem', -1); break;
        case 'r': jumpTo('.banner', 1); break;
        case 'R': jumpTo('.banner', -1); break;
        case 'g': logScroll.scrollTop = 0; break;
        case 'G': logScroll.scrollTop = logScroll.scrollHeight; break;
        case 'Escape': if (filterInput.value) applyFilter(''); break;
        case '/':
            e.preventDefault(); // иначе "/" попадёт в поле первым же символом
            filterInput.focus();
            filterInput.select();
            break;
        default: return;
    }
});

function toggleSidebar(force) {
    const show = force === undefined ? sidebar.style.display === 'none' : force;
    sidebar.style.display = show ? '' : 'none';
    uiState.showSidebar = show;
    SetShowSidebar(show);
}

function toggleHelp(force) {
    const show = force === undefined ? helpOverlay.style.display === 'none' : force;
    helpOverlay.style.display = show ? '' : 'none';
}

btnHelp.addEventListener('click', () => toggleHelp());
helpOverlay.addEventListener('click', () => toggleHelp(false));

// ─── старт ───────────────────────────────────────────────────────────────

Bootstrap().then((boot) => {
    uiState = boot.uiState;
    if (!uiState.showSidebar) sidebar.style.display = 'none';
    btnDebug.classList.toggle('toggled', uiState.showDebug);
    btnWatchdog.classList.toggle('toggled', uiState.watchdog);
    btnLevel.textContent = uiState.minLevel === LEVEL_WARNING ? 'порог: warning+'
        : uiState.minLevel === LEVEL_ERROR ? 'порог: error+' : 'порог: всё';
    renderStatus(boot.status);
    renderAll(boot.records);
});
