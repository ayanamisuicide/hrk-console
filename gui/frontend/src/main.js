import './style.css';

import {
    Bootstrap, SetFilter, SetShowDebug, CycleMinLevel, SetWatchdog, SetShowSidebar,
    StartBot, StopBot, RestartBot, ClearLog, ExportLog, ApplyUpdate, RestartApp, CheckForUpdate,
    PreflightChecks, ConnectRemote, DisconnectRemote, TestRemoteConnection,
} from '../wailsjs/go/main/App';
import {
    EventsOn, WindowSetTitle, ClipboardSetText, BrowserOpenURL,
    WindowMinimise, WindowToggleMaximise, Quit,
} from '../wailsjs/runtime/runtime';

const LEVEL_DEBUG = 0, LEVEL_INFO = 1, LEVEL_WARNING = 2, LEVEL_ERROR = 3, LEVEL_CRITICAL = 4;
const BADGES = ['DBG', 'INF', 'WRN', 'ERR', 'CRT'];
const BADGE_CLASS = ['dbg', 'inf', 'wrn', 'err', 'crt'];
const MODULE_COLORS = 8; // --mod-1..--mod-8 в style.css

// ─── разметка ────────────────────────────────────────────────────────────

document.querySelector('#app').innerHTML = `
  <div class="preflight-overlay" id="preflight-overlay">
    <div class="preflight-card">
      <div class="preflight-head">
        <span class="preflight-brand">HEROKU</span>
        <span class="preflight-sub">проверка окружения</span>
      </div>
      <div class="preflight-list" id="preflight-list"></div>
      <div class="preflight-foot">
        <div class="preflight-bar"><div class="preflight-bar-fill" id="preflight-bar-fill"></div></div>
        <div class="preflight-status" id="preflight-status">запускаю проверки…</div>
      </div>
      <button class="preflight-continue" id="preflight-continue" style="display:none">продолжить всё равно</button>
    </div>
  </div>
  <div class="topbar">
    <div class="traffic">
      <button class="traffic-dot c" id="btn-close" title="Закрыть"><span>×</span></button>
      <button class="traffic-dot m" id="btn-minimise" title="Свернуть"><span>−</span></button>
      <button class="traffic-dot z" id="btn-maximise" title="Развернуть"><span>+</span></button>
    </div>
    <div class="brand-group">
      <span class="brand">HEROKU</span>
      <span class="version" id="hkc-version"></span>
      <button class="update-badge" id="update-badge" data-state="idle" title="Проверить обновления">⟲ обновить</button>
    </div>
    <div class="spacer"></div>
    <div class="controls">
      <button id="btn-ru" title="Переводить известные шаблонные сообщения на русский (словарь, не живой перевод)">RU</button>
      <button id="btn-debug">debug</button>
      <button id="btn-level">порог: всё</button>
      <button id="btn-export" title="Экспортировать видимый лог в файл">Экспорт</button>
      <button id="btn-clear" title="Очистить экран лога — файл на диске не трогается">Очистить</button>
      <button id="btn-help" title="Горячие клавиши">?</button>
    </div>
  </div>

  <!-- ── статус-строка: единственное, что видно всегда, без карточек ──── -->
  <div class="statusline">
    <span class="sl-seg status-pill down" id="status-pill">
      <span class="status-dot"></span><span id="status-text">…</span>
    </span>
    <span class="sl-seg sl-warn"><span id="sl-warn">0</span></span>
    <span class="sl-seg sl-err"><span id="sl-err">0</span></span>
    <span class="sl-seg sl-mods" id="sl-mods"></span>
    <span id="threshold-badge" class="sl-seg sl-chip" style="display:none"></span>
    <span id="filter-badge" class="sl-seg sl-chip accent" style="display:none"></span>
    <div class="sl-spacer"></div>
    <span class="sl-seg sl-conn" id="sl-conn" style="display:none">
      <span class="conn-dot-wrap"><span class="conn-dot"></span></span>
      <span id="sl-conn-label"></span>
    </span>
    <button class="sl-seg sl-panel-btn" id="btn-sidebar" title="Панель — статы, модули, подключение (s)">⋯</button>
  </div>

  <div class="update-toast" id="update-toast" style="display:none">
    <span class="ut-icon">↑</span>
    <span class="ut-text">Доступна версия <b id="ut-version"></b></span>
    <button id="ut-update" class="primary">Обновить</button>
    <button id="ut-dismiss" class="ut-dismiss" title="Не сейчас">×</button>
  </div>
  <div class="stream-issue" id="stream-issue" style="display:none">
    <span class="si-icon">⚠</span>
    <span class="si-text"><span class="si-reason" id="si-reason"></span><span class="si-hint">команды боту по-прежнему уходят — не читается только поток лога</span></span>
    <span class="si-for" id="si-for"></span>
  </div>

  <div class="body">
    <div class="log-pane">
      <div class="log-scroll" id="log-scroll"></div>
      <div class="log-footer">
        <button id="btn-start" class="primary">Start</button>
        <button id="btn-restart">Restart</button>
        <button id="btn-stop" class="danger">Stop</button>
        <input type="text" id="filter-input" placeholder="подстрока для поиска… (re:шаблон — регулярное выражение)" autocomplete="off" />
      </div>
    </div>
  </div>

  <!-- ── панель по требованию: не постоянная колонка, а оверлей (s / ⋯) ── -->
  <div class="panel-overlay" id="sidebar">
    <div class="panel-card">
      <div class="group">
      <h3><span class="g-icon warn">!</span>Проблемы</h3>
      <div class="problem-counts">
        <span class="warn">⚠ <span id="count-warn">0</span></span>
        <span class="err">✗ <span id="count-err">0</span></span>
      </div>
      </div>

      <div class="group">
      <h3><span class="g-icon accent">◆</span>Поток</h3>
      <canvas class="sparkline-canvas" id="sparkline-canvas" width="220" height="30"></canvas>

      <h3><span class="g-icon accent">▤</span>История <span class="h3-total" id="history-total"></span></h3>
      <div class="history-chart" id="history-chart"></div>
      </div>

      <div class="group">
      <h3><span class="g-icon mod">▣</span>Модули <span class="h3-total" id="module-total"></span></h3>
      <div class="module-list" id="module-list"></div>
      </div>

      <div class="group">
      <h3><span class="g-icon good">▶</span>Процесс</h3>
      <div class="proc-block" id="proc-block">—</div>
      <button id="btn-watchdog" class="proc-watchdog-btn">auto-restart</button>
      <div class="watchdog-note" id="watchdog-note"></div>
      </div>

      <div class="conn-section group" id="conn-section" style="display:none">
        <h3><span class="g-icon accent">⇄</span>Подключение</h3>
        <div class="conn-block" id="conn-block" data-state="local">
          <span class="conn-dot-wrap"><span class="conn-dot"></span></span>
          <span class="conn-label" id="conn-label">локально</span>
          <span class="conn-meta" id="conn-meta">бот на этом компьютере</span>
        </div>
        <button id="btn-remote" class="remote-open-btn"><span id="btn-remote-label">Удалённый бот</span><span class="btn-chevron">›</span></button>
      </div>

      <div class="gui-version" id="gui-version"></div>
    </div>
  </div>
  <div class="help-overlay" id="help-overlay">
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
  <div class="remote-overlay" id="remote-overlay">
    <div class="remote-card">
      <h2>Удалённый бот</h2>
      <p class="remote-hint">Бот управляется на другой машине по SSH — окно только показывает и командует, ничего из бота на этом компьютере не хранится.</p>
      <label>Хост или IP<input type="text" id="remote-host" placeholder="192.168.31.128" autocomplete="off"></label>
      <label>Пользователь<input type="text" id="remote-user" placeholder="ayanami" autocomplete="off"></label>
      <label>Приватный ключ<input type="text" id="remote-key" placeholder="C:\Users\...\.ssh\id_ed25519" autocomplete="off"></label>
      <label>Каталог бота на той машине<input type="text" id="remote-dir" placeholder="Heroku" autocomplete="off"></label>
      <div class="remote-note" id="remote-note"></div>
      <div class="remote-actions">
        <button id="btn-remote-test">Проверить соединение</button>
        <button id="btn-remote-connect" class="primary">Подключиться</button>
        <button id="btn-remote-disconnect" class="danger" style="display:none">Вернуться к локальному боту</button>
        <button id="btn-remote-cancel">Отмена</button>
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
const sparklineCanvas = el('sparkline-canvas');
const sparklineCtx = sparklineCanvas.getContext('2d');
const historyChart = el('history-chart');
const historyTotal = el('history-total');
const moduleList = el('module-list');
const moduleTotal = el('module-total');
const procBlock = el('proc-block');
const watchdogNote = el('watchdog-note');
const logScroll = el('log-scroll');
const filterInput = el('filter-input');
const btnStart = el('btn-start');
const btnRestart = el('btn-restart');
const btnStop = el('btn-stop');
const btnRu = el('btn-ru');
const btnDebug = el('btn-debug');
const btnLevel = el('btn-level');
const btnWatchdog = el('btn-watchdog');
const btnClear = el('btn-clear');
const btnExport = el('btn-export');
const btnHelp = el('btn-help');
const updateBadge = el('update-badge');
const updateToast = el('update-toast');
const utVersion = el('ut-version');
const btnUtUpdate = el('ut-update');
const btnUtDismiss = el('ut-dismiss');
const helpOverlay = el('help-overlay');
const body = document.querySelector('.body');
const btnSidebar = el('btn-sidebar');
const sidebarOverlay = el('sidebar');
const slWarn = el('sl-warn');
const slErr = el('sl-err');
const slMods = el('sl-mods');
const slConn = el('sl-conn');
const slConnLabel = el('sl-conn-label');
const btnClose = el('btn-close');
const btnMinimise = el('btn-minimise');
const btnMaximise = el('btn-maximise');
const preflightOverlay = el('preflight-overlay');
const preflightList = el('preflight-list');
const preflightBarFill = el('preflight-bar-fill');
const preflightStatus = el('preflight-status');
const preflightContinue = el('preflight-continue');
const connSection = el('conn-section');
const connBlock = el('conn-block');
const connLabel = el('conn-label');
const connMeta = el('conn-meta');
const streamIssueReason = el('si-reason');
const streamIssueFor = el('si-for');
const btnRemote = el('btn-remote');
const btnRemoteLabel = el('btn-remote-label');
const remoteOverlay = el('remote-overlay');
const remoteHostInput = el('remote-host');
const remoteUserInput = el('remote-user');
const remoteKeyInput = el('remote-key');
const remoteDirInput = el('remote-dir');
const remoteNote = el('remote-note');
const btnRemoteTest = el('btn-remote-test');
const btnRemoteConnect = el('btn-remote-connect');
const btnRemoteDisconnect = el('btn-remote-disconnect');
const btnRemoteCancel = el('btn-remote-cancel');

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

// historyBuckets — та же идея, что у сперклайна активности (кольцо,
// растущее по мере жизни окна), но на другом масштабе: не последние 40
// секунд «шумит или нет», а последний час «когда именно участились
// проблемы» — секундный сперклайн для этого слишком короткий, за минуту
// прокручивается целиком. Ведро — не общий счёт событий, а warn/err
// отдельно: важно видеть, что именно накопилось, а не просто «было шумно».
const HISTORY_MINUTES = 60;
let historyBuckets = Array.from({ length: HISTORY_MINUTES }, () => ({ warn: 0, err: 0 }));
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

// ─── локализация известных шаблонных сообщений ──────────────────────────
//
// Не перевод произвольного текста — живой поток лога слишком быстрый для
// сетевого переводчика на каждую строку (десятки строк в секунду), а
// трейсбеки и пути переводить нельзя вообще, иначе отладка ломается.
// Вместо этого — словарь: известные повторяющиеся шаблоны (urllib3,
// asyncio, heartbeat загрузчика) подменяются на русский текст регэкспом,
// всё остальное остаётся как есть. Список короткий и лёгкий в расширении —
// новые повторяющиеся строки из своего heroku.log добавляются одной парой.
const RU_DICT = [
    [/^Starting new HTTPS connection \((\d+)\): (.+)$/, (m) => `Новое HTTPS-соединение (${m[1]}): ${m[2]}`],
    [/^Starting new HTTP connection \((\d+)\): (.+)$/, (m) => `Новое HTTP-соединение (${m[1]}): ${m[2]}`],
    [/^Using selector: (.+)$/, (m) => `Используется селектор ввода-вывода: ${m[1]}`],
    [/^Reloaded (\d+) commands?$/, (m) => `Перезагружено команд: ${m[1]}`],
    [/^Loading (.+) from filesystem$/, (m) => `Загрузка «${m[1]}» из файловой системы`],
    [/^Loaded (\d+) modules?,\s*(\d+) skipped$/, (m) => `Загружено модулей: ${m[1]}, пропущено: ${m[2]}`],
];

function translateLine(text) {
    for (const [re, tr] of RU_DICT) {
        const m = text.match(re);
        if (m) return tr(m);
    }
    return text;
}

let ruEnabled = localStorage.getItem('hkc-gui-ru') === '1';

// ─── подсветка совпадений активного фильтра ─────────────────────────────
//
// logfeed.Visible на бэкенде уже решил, что запись видна — здесь просто
// показываем, где именно в тексте совпадение, как Ctrl+F: раньше фильтр
// только прятал нерелевантное, а само совпадение в оставшемся тексте
// ничем не выделялось.
function escapeRegExp(s) {
    return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function filterRegExp(filterValue) {
    if (!filterValue) return null;
    const pattern = filterValue.startsWith('re:') ? filterValue.slice(3) : escapeRegExp(filterValue);
    try {
        return new RegExp(pattern, 'gi');
    } catch {
        // Битый regexp — то же решение, что и в logfeed.matchFilter на
        // бэкенде: трактуем как "не совпало", а не роняем рендер.
        return null;
    }
}

function appendHighlighted(el, text, filterValue) {
    const re = filterRegExp(filterValue);
    if (!re) {
        el.appendChild(document.createTextNode(text));
        return;
    }
    let last = 0, m;
    while ((m = re.exec(text)) !== null) {
        if (m[0] === '') { re.lastIndex++; continue; } // не зациклиться на пустом совпадении
        if (m.index > last) el.appendChild(document.createTextNode(text.slice(last, m.index)));
        const mark = document.createElement('mark');
        mark.className = 'hl';
        mark.textContent = m[0];
        el.appendChild(mark);
        last = m.index + m[0].length;
    }
    if (last < text.length) el.appendChild(document.createTextNode(text.slice(last)));
}

// ─── рендер одной записи ────────────────────────────────────────────────

function buildMsgEl(rec) {
    const msg = document.createElement('span');
    const msgClass = rec.level === LEVEL_CRITICAL ? 'crt' : rec.level === LEVEL_ERROR ? 'err' : rec.level === LEVEL_WARNING ? 'wrn' : '';
    msg.className = 'msg' + (msgClass ? ' ' + msgClass : '');
    // Переводим только настоящий заголовок записи (i === 0, rec.hard[0] не
    // взведён) — физический перенос (трейсбек, дамп) переносит rec.hard[0]
    // в true и в словарь никогда не попадает.
    const translatable = ruEnabled && !(rec.hard && rec.hard[0]);
    const filterValue = filterInput.value;
    (rec.lines || ['']).forEach((line, i) => {
        const display = (i === 0 && translatable) ? translateLine(line) : line;
        if (i > 0) {
            const br = document.createElement('div');
            const arrow = document.createElement('span');
            arrow.className = 'continuation-arrow';
            arrow.textContent = '↳ ';
            br.appendChild(arrow);
            appendHighlighted(br, display, filterValue);
            msg.appendChild(br);
        } else {
            appendHighlighted(msg, display, filterValue);
        }
    });
    return msg;
}

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
    appendHighlighted(mod, rec.module, filterInput.value);
    row.appendChild(mod);

    row.appendChild(buildMsgEl(rec));

    const count = document.createElement('span');
    count.className = 'count';
    if (rec.count > 1) {
        count.textContent = '×' + rec.count;
    } else {
        count.style.visibility = 'hidden';
    }
    row.appendChild(count);

    const copyBtn = document.createElement('span');
    copyBtn.className = 'row-copy';
    copyBtn.title = 'Скопировать';
    copyBtn.textContent = '⧉';
    copyBtn.addEventListener('click', (e) => {
        e.stopPropagation();
        copyRowText(row);
    });
    row.appendChild(copyBtn);

    row._rec = rec;
    return row;
}

// retranslateAll — переключение RU меняет только то, что уже нарисовано,
// без похода к бэкенду: у каждой строки уже есть исходная запись (row._rec),
// достаточно перестроить только .msg, не пересоздавая всю строку заново.
function retranslateAll() {
    for (const row of logScroll.querySelectorAll('.row')) {
        if (!row._rec) continue;
        row.querySelector('.msg').replaceWith(buildMsgEl(row._rec));
    }
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
        const bucket = historyBuckets[historyBuckets.length - 1];
        if (evt.rec.warn) bucket.warn++;
        else if (evt.rec.err) bucket.err++;
    }

    logScroll.appendChild(frag);
    trimLog();
    if (pinned) logScroll.scrollTop = logScroll.scrollHeight; // одна запись в конце пакета
    // Только счётчики — список модулей дороже (innerHTML='' + N узлов и
    // обработчиков), ему хватит тика раз в секунду ниже, даже во время
    // всплеска лога, когда этот путь срабатывает по несколько раз за кадр.
    renderCounts();

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

// Состояния бейджа обновлений в шапке — одна кнопка на весь цикл, состояние
// всегда видно (data-state), а не "бейдж есть = что-то не так, бейджа нет =
// разбирайся сам":
//
//   idle      → клик проверяет (или просто ждём первую авто-проверку при
//               старте — см. 'update-status' ниже)
//   checking  → идёт запрос, клик игнорируется
//   uptodate  → "✓ актуально", клик проверяет заново
//   available → "↑ vX.Y.Z", клик качает и подменяет себя на диске
//               (ApplyUpdate); ПКМ — просто открыть страницу релиза
//   updating  → идёт скачивание/подмена, клик игнорируется
//   done      → "✓ перезапустить", клик перезапускает окно (RestartApp)
//   error     → сеть/GitHub подвели, клик пробует снова
//
// Подмена собственного исполняемого файла необратима без переустановки,
// поэтому и "качать", и "перезапустить" — отдельные явные клики, а не один
// автоматический шаг.
let updateInfo = null; // {version, url} — есть только в состоянии available

function setUpdateBadge(state, text, title) {
    updateBadge.dataset.state = state;
    updateBadge.textContent = text;
    updateBadge.title = title;
}

// Авто-проверка при старте окна: бэкенд шлёт результат, только если запрос
// реально отработал (см. checkUpdateOnce) — на сетевой осечке событие не
// приходит вовсе, и бейдж просто остаётся в idle, приглашая проверить
// вручную, а не рапортует об ошибке без явного запроса пользователя.
EventsOn('update-status', (res) => applyUpdateResult(res, false));

function applyUpdateResult(res, manual) {
    if (res.available) {
        updateInfo = { version: res.latest, url: res.url };
        setUpdateBadge('available', '↑ ' + res.latest,
            `Доступна версия ${res.latest} — клик обновит на месте, ПКМ откроет релиз в браузере`);
        // Тост — только по итогам АВТО-проверки при старте окна, не ручной:
        // пользователь, только что сам нажавший "проверить", уже смотрит на
        // бейдж и не нуждается в отдельном приглашении поверх того же
        // ответа. И только если ещё не отклонял именно эту версию в этом
        // запуске — иначе он мигал бы заново на каждый случайный ре-рендер.
        if (!manual && dismissedUpdateVersion !== res.latest) {
            showUpdateToast(res.latest);
        }
        return;
    }
    updateInfo = null;
    hideUpdateToast();
    if (res.ok) {
        setUpdateBadge('uptodate', '✓ актуально',
            `Уже последняя версия (${res.current}) — клик проверит ещё раз`);
    } else if (manual) {
        setUpdateBadge('error', '⚠ ошибка проверки', `Не удалось проверить: ${res.message} — клик попробует снова`);
    }
}

// dismissedUpdateVersion — версия, которую явно отклонили крестиком: тост
// для неё больше не всплывает сам, бейдж в шапке остаётся тихим
// напоминанием и обновляет всё равно можно кликом по нему в любой момент.
let dismissedUpdateVersion = null;

function showUpdateToast(version) {
    utVersion.textContent = version;
    updateToast.style.display = '';
    updateToast.classList.remove('ut-enter');
    void updateToast.offsetWidth; // перезапуск анимации, если тост уже был показан
    updateToast.classList.add('ut-enter');
}

function hideUpdateToast() {
    updateToast.style.display = 'none';
}

// startUpdate — общее действие для клика по бейджу в состоянии "available"
// и для кнопки "Обновить" в тосте: один и тот же переход (скачать →
// подменить на диске → предложить перезапустить), запускать который может
// любой из двух — тост просто более заметный способ добраться до того же
// самого действия.
async function startUpdate() {
    hideUpdateToast();
    setUpdateBadge('updating', '⟳ обновляю…', 'Скачиваю и подменяю приложение на диске');
    const res = await ApplyUpdate();
    if (res.ok) {
        setUpdateBadge('done', '✓ перезапустить', `Обновлено до ${res.message || updateInfo.version} — клик перезапустит окно`);
    } else {
        setUpdateBadge('available', '↑ ' + updateInfo.version, `Не удалось обновить: ${res.message} — клик попробует снова`);
        showBanner(`✗  обновление: ${res.message}`, true);
    }
}

btnUtUpdate.addEventListener('click', startUpdate);
btnUtDismiss.addEventListener('click', () => {
    dismissedUpdateVersion = updateInfo ? updateInfo.version : null;
    hideUpdateToast();
});

updateBadge.addEventListener('contextmenu', (e) => {
    e.preventDefault();
    if (updateInfo) BrowserOpenURL(updateInfo.url);
    else BrowserOpenURL('https://github.com/ayanamisuicide/hrk-console/releases');
});

updateBadge.addEventListener('click', async () => {
    const state = updateBadge.dataset.state;
    if (state === 'checking' || state === 'updating') return;

    if (state === 'done') {
        RestartApp();
        return;
    }

    if (state === 'available') {
        await startUpdate();
        return;
    }

    // idle / uptodate / error — все три ведут к ручной проверке.
    hideUpdateToast();
    setUpdateBadge('checking', '⟳ проверяю…', 'Проверяю GitHub на новый релиз');
    applyUpdateResult(await CheckForUpdate(), true);
});

// ─── проверки перед стартом ──────────────────────────────────────────────
//
// Те же восемь проверок, что у TUI (preflight.All), тем же контрактом:
// по одной, а не разом — прогон должен читаться как последовательность
// шагов. Бэкенд начинает гонять их сразу при старте окна (gui/app.go,
// runPreflight), параллельно с загрузкой истории лога — сама проверка
// окружения не блокирует то, что уже можно показать.
//
// Имена проверок и их результаты приезжают разными путями: PreflightChecks()
// — обычный запрос-ответ, событие 'preflight-check' — поток. Порядок их
// прибытия не гарантирован (первое событие вполне может успеть раньше, чем
// разрешится запрос имён), поэтому события, пришедшие до того, как строки
// отрисованы, копятся в очереди и применяются разом, как только список
// готов — тот же приём, что и pendingTail для строк лога.
let preflightRows = [];
let preflightPending = [];
let preflightFailed = false;

EventsOn('preflight-check', (ev) => {
    if (preflightRows.length === 0) {
        preflightPending.push(ev);
        return;
    }
    applyPreflightEvent(ev);
});

function applyPreflightEvent(ev) {
    const row = preflightRows[ev.index];
    if (!row) return;

    row.dataset.state = ev.status;
    row.querySelector('.pf-detail').textContent = ev.detail || '';
    if (ev.status === 'failed') preflightFailed = true;

    preflightBarFill.style.width = Math.round(((ev.index + 1) / ev.total) * 100) + '%';

    const next = preflightRows[ev.index + 1];
    if (next) {
        next.dataset.state = 'running';
        preflightStatus.className = 'preflight-status';
        preflightStatus.textContent = next.querySelector('.pf-name').textContent + '…';
    } else {
        finishPreflight();
    }
}

function finishPreflight() {
    if (preflightFailed) {
        preflightStatus.textContent = 'не всё в порядке';
        preflightStatus.className = 'preflight-status bad';
        preflightContinue.style.display = '';
    } else {
        preflightStatus.textContent = 'всё готово';
        preflightStatus.className = 'preflight-status ok';
        // Короткая пауза, чтобы взгляд успел долистать до "всё готово" —
        // без неё зелёная строка появлялась бы и тут же сменялась логом.
        setTimeout(dismissPreflight, 900);
    }
}

function dismissPreflight() {
    preflightOverlay.classList.add('dismissed');
}

preflightContinue.addEventListener('click', dismissPreflight);

function renderPreflightChecks(names) {
    if (!names || names.length === 0) {
        dismissPreflight();
        return;
    }
    preflightRows = names.map((name, i) => {
        const row = document.createElement('div');
        row.className = 'pf-row';
        row.dataset.state = i === 0 ? 'running' : 'pending';
        row.innerHTML = '<span class="pf-dot"></span><span class="pf-name"></span><span class="pf-detail"></span>';
        row.querySelector('.pf-name').textContent = name;
        preflightList.appendChild(row);
        return row;
    });
    preflightStatus.textContent = names[0] + '…';
    preflightPending.forEach(applyPreflightEvent);
    preflightPending = [];
}

PreflightChecks().then(renderPreflightChecks);

// ─── удалённое подключение ───────────────────────────────────────────────
//
// remote в uiState (Bootstrap/status) — те же четыре поля, что и
// state.Remote на бэкенде; host пустой значит "локально". Подключение и
// отключение оба идут через честный перезапуск приложения (ConnectRemote/
// DisconnectRemote вызывают RestartApp на бэкенде) — переключение
// источника бота на лету было бы источником гонок между старыми и новыми
// горутинами, а перезапуск окна занимает меньше секунды и уже есть готовым
// после самообновления.
// Блок «Подключение» показывается не всегда. На машине, где бот и так
// установлен рядом (Linux), удалённый режим — не то, ради чего открывают
// окно: строка «локально» и кнопка под ней занимали место в панели, ничего
// при этом не сообщая. Он существует ради Windows, где локального бота
// быть не может. Исключение — если удалённый хост всё-таки настроен: тогда
// блок нужен везде, иначе настройку негде было бы увидеть и отменить.
let platform = 'linux';

function showConnSection(remote) {
    const show = platform === 'windows' || (remote && remote.host);
    connSection.style.display = show ? '' : 'none';
    // Тот же вопрос, что решает connSection ("вообще имеет смысл говорить
    // о подключении на этой платформе"), но для компактного индикатора в
    // статус-строке, который виден и без открытой панели.
    slConn.style.display = show ? '' : 'none';
}

// Куда подключены — отдельно от того, в каком состоянии подключение:
// адрес меняется только по воле пользователя, состояние — само по себе,
// каждую секунду (см. Status.RemoteState).
function renderRemoteTarget(remote) {
    if (remote && remote.host) {
        connLabel.textContent = remote.host;
        connMeta.textContent = remote.user ? remote.user + ' · подключаюсь…' : 'подключаюсь…';
        connBlock.dataset.state = 'connecting';
        btnRemoteDisconnect.style.display = '';
        btnRemoteLabel.textContent = 'Изменить подключение';
        return;
    }
    connBlock.dataset.state = 'local';
    btnRemoteDisconnect.style.display = 'none';
    btnRemoteLabel.textContent = 'Удалённый бот';
    // «Локально» на Windows означало бы бота, которого там нет и быть не
    // может: сам Heroku запускается только на Linux. Пока машина не
    // указана, окну попросту нечем управлять, и сказать об этом честно
    // полезнее, чем показать успокаивающее «локально».
    if (platform === 'windows') {
        connLabel.textContent = 'не настроено';
        connMeta.textContent = 'укажите машину, где стоит бот';
    } else {
        connLabel.textContent = 'локально';
        connMeta.textContent = 'бот на этом компьютере';
    }
}

const CONN_LABEL = { connecting: 'подключаюсь…', online: 'на связи', offline: 'нет связи' };

function renderConnState(st) {
    if (!st.remoteState) return; // локальный бот — блок про адрес, а не про соединение
    connBlock.dataset.state = st.remoteState;
    connLabel.textContent = CONN_LABEL[st.remoteState] || st.remoteState;
    connMeta.textContent = (uiState.remote && uiState.remote.host ? uiState.remote.host + ' · ' : '') +
        (st.remoteStateNote || '');
    slConn.dataset.state = st.remoteState;
    slConnLabel.textContent = (uiState.remote && uiState.remote.host) || CONN_LABEL[st.remoteState] || st.remoteState;
}

function openRemoteOverlay(remote) {
    remoteHostInput.value = (remote && remote.host) || '';
    remoteUserInput.value = (remote && remote.user) || '';
    remoteKeyInput.value = (remote && remote.keyPath) || '';
    remoteDirInput.value = (remote && remote.dir) || 'Heroku';
    remoteNote.textContent = '';
    remoteNote.className = 'remote-note';
    btnRemoteTest.disabled = false;
    btnRemoteConnect.disabled = false;
    remoteOverlay.classList.add('visible');
}

function closeRemoteOverlay() {
    remoteOverlay.classList.remove('visible');
}

btnRemote.addEventListener('click', () => openRemoteOverlay(uiState.remote));
btnRemoteCancel.addEventListener('click', closeRemoteOverlay);
remoteOverlay.addEventListener('click', (e) => {
    if (e.target === remoteOverlay) closeRemoteOverlay();
});

// readRemoteForm — общее чтение+валидация для "проверить" и "подключиться":
// обе кнопки хотят один и тот же конфиг, дублировать проверку незачем.
function readRemoteForm() {
    const cfg = {
        host: remoteHostInput.value.trim(),
        user: remoteUserInput.value.trim(),
        keyPath: remoteKeyInput.value.trim(),
        dir: remoteDirInput.value.trim() || 'Heroku',
    };
    if (!cfg.host || !cfg.user || !cfg.keyPath) {
        remoteNote.textContent = 'заполните хост, пользователя и путь к ключу';
        remoteNote.className = 'remote-note';
        return null;
    }
    return cfg;
}

btnRemoteTest.addEventListener('click', async () => {
    const cfg = readRemoteForm();
    if (!cfg) return;
    btnRemoteTest.disabled = true;
    remoteNote.textContent = 'проверяю…';
    remoteNote.className = 'remote-note';
    const res = await TestRemoteConnection(cfg);
    btnRemoteTest.disabled = false;
    remoteNote.textContent = res.message;
    remoteNote.className = 'remote-note' + (res.ok ? ' ok' : ' bad');
});

btnRemoteConnect.addEventListener('click', async () => {
    const cfg = readRemoteForm();
    if (!cfg) return;
    btnRemoteConnect.disabled = true;
    remoteNote.textContent = 'подключаюсь и перезапускаю окно…';
    remoteNote.className = 'remote-note';
    const res = await ConnectRemote(cfg);
    if (!res.ok) {
        btnRemoteConnect.disabled = false;
        remoteNote.textContent = 'не удалось: ' + res.message;
        remoteNote.className = 'remote-note bad';
    }
    // При успехе окно вот-вот перезапустится само (RestartApp на бэкенде) —
    // отдельно закрывать оверлей не нужно.
});

btnRemoteDisconnect.addEventListener('click', async () => {
    btnRemoteDisconnect.disabled = true;
    remoteNote.textContent = 'возвращаюсь к локальному боту…';
    const res = await DisconnectRemote();
    if (!res.ok) {
        btnRemoteDisconnect.disabled = false;
        remoteNote.textContent = 'не удалось: ' + res.message;
    }
});

// ─── шапка / статус ──────────────────────────────────────────────────────

// streamIssueShown — statusLoop зовёт renderStatus раз в секунду, пока
// проблема с логом не решится; без этого флага полоса переигрывала бы
// анимацию появления на каждый тик, а не один раз в момент, когда она
// реально появилась.
let streamIssueShown = false;

function renderStatus(st) {
    el('hkc-version').textContent = 'hkc ' + (st.hkcVersion || 'dev');
    setProjectVersion(st.hkcVersion || 'dev');

    renderConnState(st);

    statusPill.classList.remove('live', 'down', 'warn');
    if (st.streamIssue) {
        statusPill.classList.add('warn');
        statusText.textContent = '⚠ лог не читается';
        if (!streamIssueShown) {
            streamIssueBox.style.display = '';
            streamIssueBox.classList.add('si-enter');
            streamIssueShown = true;
        }
        streamIssueReason.textContent = st.streamIssue;
        streamIssueFor.textContent = st.streamIssueFor || '';
    } else {
        streamIssueBox.style.display = 'none';
        streamIssueBox.classList.remove('si-enter');
        streamIssueShown = false;
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

// renderCounts — дешёвая часть панели: счётчики и бейджи, только textContent
// и toggle стилей. Зовётся на каждый пакет строк, даже во время всплеска
// лога — здесь нет ничего, что стоило бы приберечь.
function renderCounts() {
    countWarn.textContent = warnCount;
    countErr.textContent = errCount;
    // Дубли в статус-строке — она видна всегда, панель с count-warn/
    // count-err выше открывается только по требованию; ⚠/✗ должны
    // читаться и без открытой панели.
    slWarn.textContent = '⚠ ' + warnCount;
    slErr.textContent = '✗ ' + errCount;

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
}

// renderModules — дорогая часть: пересобирает весь список модулей заново
// (innerHTML='' + N узлов + N новых обработчиков клика). Список теперь
// полный, а не топ-7, поэтому N может быть за сорок — звать это на каждый
// кадр всплеска лога (как раньше) значит десятки полных пересборок панели
// в секунду ради данных, которые всё равно обновит ближайший тик спарклайна.
// Дёргается из renderSidebar (полная перерисовка) и из тикера ниже — не из
// «горячего» пути flushTail.
function renderModules() {
    const stats = [...moduleStats.entries()]
        .map(([name, s]) => ({ name, ...s }))
        .sort((a, b) => b.count - a.count);
    const peak = stats.length ? stats[0].count : 1;
    moduleTotal.textContent = stats.length ? '· ' + stats.length : '';
    // Топ-3 в статус-строке — тот же смысл, что раньше нёс весь список в
    // постоянной колонке (кто вообще шумит), только без постоянно занятого
    // места: полный список по-прежнему доступен по клику на панель.
    slMods.textContent = stats.slice(0, 3).map((m) => m.name).join(' · ');
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
}

// renderSparkline — живая кривая на canvas (в духе Activity Monitor), а не
// столбики: тот же массив activityBuckets, просто читается плавной линией
// с затуханием по краям, а не решёткой отдельных прямоугольников.
// Перерисовывается тем же тиком раз в секунду, что и раньше двигал
// столбики, — здесь не нужен отдельный requestAnimationFrame-цикл, "живой"
// вид даёт сама форма кривой, а не непрерывная анимация.
function renderSparkline() {
    const W = sparklineCanvas.width, H = sparklineCanvas.height;
    const ctx = sparklineCtx;
    ctx.clearRect(0, 0, W, H);
    const peak = Math.max(1, ...activityBuckets);
    const n = activityBuckets.length;
    const step = W / (n - 1);
    const points = activityBuckets.map((v, i) => [
        i * step,
        H - 2 - (v / peak) * (H - 6),
    ]);

    const grad = ctx.createLinearGradient(0, 0, W, 0);
    grad.addColorStop(0, 'rgba(156,139,255,.15)');
    grad.addColorStop(1, 'rgba(156,139,255,.95)');
    ctx.strokeStyle = grad;
    ctx.lineWidth = 1.6;
    ctx.lineJoin = 'round';
    ctx.lineCap = 'round';
    ctx.shadowColor = 'rgba(156,139,255,.5)';
    ctx.shadowBlur = 6;
    ctx.beginPath();
    points.forEach(([x, y], i) => (i === 0 ? ctx.moveTo(x, y) : ctx.lineTo(x, y)));
    ctx.stroke();
    ctx.shadowBlur = 0;

    // Заливка под кривой — тише, чем сама линия, читается как объём, а не
    // ещё один акцент, спорящий с ней за внимание.
    ctx.lineTo(W, H);
    ctx.lineTo(0, H);
    ctx.closePath();
    const fill = ctx.createLinearGradient(0, 0, 0, H);
    fill.addColorStop(0, 'rgba(156,139,255,.16)');
    fill.addColorStop(1, 'rgba(156,139,255,0)');
    ctx.fillStyle = fill;
    ctx.fill();
}

// renderHistory — 60 столбиков, каждый минута жизни окна, самый правый —
// текущая. Высота — суммарный объём warn+err за минуту относительно пика
// за весь час, цвет — что там накопилось (err перекрывает warn: если в
// минуте была хоть одна ошибка, она и определяет цвет всей минуты, warn
// сам по себе не должен теряться на фоне более частых warning). Подсказка
// при наведении — точное число и когда это было, столбик сам по себе
// только про «плохо/терпимо/тихо».
function renderHistory() {
    historyChart.innerHTML = '';
    const peak = Math.max(1, ...historyBuckets.map((b) => b.warn + b.err));
    let totalWarn = 0, totalErr = 0;
    historyBuckets.forEach((b, i) => {
        totalWarn += b.warn;
        totalErr += b.err;
        const total = b.warn + b.err;
        const bar = document.createElement('span');
        bar.className = 'history-bar' + (b.err > 0 ? ' err' : b.warn > 0 ? ' warn' : '');
        bar.style.height = (total === 0 ? 4 : Math.max(10, Math.round((total / peak) * 100))) + '%';
        const minutesAgo = historyBuckets.length - 1 - i;
        bar.title = (minutesAgo === 0 ? 'эта минута' : minutesAgo + ' мин назад') +
            (total ? `  ·  ⚠${b.warn} ✗${b.err}` : '  ·  тихо');
        historyChart.appendChild(bar);
    });
    historyTotal.textContent = (totalWarn + totalErr) ? `· ⚠${totalWarn} ✗${totalErr}` : '';
}

// renderSidebar — полная перерисовка панели. Дёшево звать по факту события
// (смена фильтра/порога/DEBUG, полная пересборка) — дорого на каждый кадр
// потока лога, поэтому «горячий» путь (flushTail) зовёт только renderCounts.
function renderSidebar() {
    renderCounts();
    renderModules();
    renderSparkline();
    renderHistory();
}

setInterval(() => {
    activityBuckets = [...activityBuckets.slice(1), activityNow];
    activityNow = 0;
    renderModules();
    renderSparkline();
}, 1000);

// Отдельный, более редкий тикер — минутные вёдра не обязаны ждать секундный,
// вращать их каждую секунду означало бы почти всегда просто перерисовывать
// тот же самый набор без изменений.
setInterval(() => {
    historyBuckets = [...historyBuckets.slice(1), { warn: 0, err: 0 }];
    renderHistory();
}, 60000);

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

// ─── копирование строки/блока ────────────────────────────────────────────

function rowPlainText(row) {
    const rec = row._rec;
    if (!rec) return row.textContent.trim();
    const head = [rec.time, BADGES[rec.level], rec.module].filter(Boolean).join('  ');
    const lines = (rec.lines || ['']).map((l, i) => i === 0 ? l : '  ↳ ' + l);
    return (head ? head + '  ' : '') + lines.join('\n');
}

// copyRowText — на ошибке/критике забирает и идущие следом строки-продолжения
// (трейсбек): у них пустое time и взведён hard[0], то есть они физически
// принадлежат этой записи, а не отдельные события (см. logfeed.Parser.Feed).
function copyRowText(row) {
    const lines = [rowPlainText(row)];
    let next = row.nextElementSibling;
    while (next && next.classList && next.classList.contains('row')) {
        const r = next._rec;
        if (!r || r.time || !(r.hard && r.hard[0])) break;
        lines.push(rowPlainText(next));
        next = next.nextElementSibling;
    }
    ClipboardSetText(lines.join('\n')).then((ok) => {
        flashHint(ok ? 'скопировано' : 'не удалось скопировать');
    });
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

btnRu.addEventListener('click', () => {
    ruEnabled = !ruEnabled;
    localStorage.setItem('hkc-gui-ru', ruEnabled ? '1' : '0');
    btnRu.classList.toggle('toggled', ruEnabled);
    retranslateAll();
});

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

btnExport.addEventListener('click', async () => {
    // Экспортируется именно то, что видно на экране — тот же порядок и тот
    // же набор строк, что и в момент клика, включая активный фильтр: это
    // ожидание "экспортируй то, на что я сейчас смотрю", а не всю историю.
    const lines = [...logScroll.querySelectorAll('.row')].map(rowPlainText);
    if (lines.length === 0) {
        flashHint('лог пуст — нечего экспортировать');
        return;
    }
    const res = await ExportLog(lines.join('\n'));
    if (!res.ok) {
        showBanner(`✗  экспорт: ${res.message}`, true);
    } else if (res.message !== 'отменено') {
        showBanner(`✓  экспорт  ·  ${res.message}`, false);
    }
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

    if (helpOverlay.classList.contains('visible') && e.key !== '?') {
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

// Панель — оверлей по требованию, не постоянная колонка: лог всегда на
// всю ширину, .panel-overlay всплывает поверх него (тот же механизм, что
// у help-overlay/remote-overlay — .visible на fixed-элементе), а не
// раздвигает layout своей шириной. showSidebar в состоянии (общем с TUI)
// теперь значит "оверлей открыт", а не "колонка видна", но само поле и
// SetShowSidebar на бэкенде не меняются — это по-прежнему один и тот же
// вопрос "показывать статы сейчас или нет".
function toggleSidebar(force) {
    const show = force === undefined ? !sidebarOverlay.classList.contains('visible') : force;
    sidebarOverlay.classList.toggle('visible', show);
    btnSidebar.classList.toggle('open', show);
    uiState.showSidebar = show;
    SetShowSidebar(show);
}
btnSidebar.addEventListener('click', () => toggleSidebar());
sidebarOverlay.addEventListener('click', (e) => {
    if (e.target === sidebarOverlay) toggleSidebar(false);
});

// Трафик-лайты — единственный способ управлять окном теперь, когда оно
// Frameless (см. gui/main.go): системной рамки с своими кнопками больше
// нет. Тот же порядок и те же цвета, что у настоящих macOS-кнопок, но это
// декоративное соответствие — приложение по-прежнему собирается только под
// Linux и Windows, а не macOS; это просто выбранный стиль собственной шапки.
btnClose.addEventListener('click', () => Quit());
btnMinimise.addEventListener('click', () => WindowMinimise());
btnMaximise.addEventListener('click', () => WindowToggleMaximise());

function toggleHelp(force) {
    const show = force === undefined ? !helpOverlay.classList.contains('visible') : force;
    helpOverlay.classList.toggle('visible', show);
}

btnHelp.addEventListener('click', () => toggleHelp());
helpOverlay.addEventListener('click', () => toggleHelp(false));

// ─── старт ───────────────────────────────────────────────────────────────

btnRu.classList.toggle('toggled', ruEnabled);

Bootstrap().then((boot) => {
    uiState = boot.uiState;
    // Оверлей по умолчанию и так закрыт (базовое состояние .panel-overlay
    // в CSS, без .visible) — в отличие от прежней раздвижной колонки, тут
    // не бывает вспышки "нарисовалась открытой и тут же скрылась", открыть
    // явно нужно только если пользователь сам оставил её открытой в
    // прошлый раз.
    if (uiState.showSidebar) toggleSidebar(true);
    btnDebug.classList.toggle('toggled', uiState.showDebug);
    btnWatchdog.classList.toggle('toggled', uiState.watchdog);
    btnLevel.textContent = uiState.minLevel === LEVEL_WARNING ? 'порог: warning+'
        : uiState.minLevel === LEVEL_ERROR ? 'порог: error+' : 'порог: всё';
    platform = boot.platform || platform;
    showConnSection(uiState.remote);
    renderRemoteTarget(uiState.remote);
    renderStatus(boot.status);
    renderAll(boot.records);
});
