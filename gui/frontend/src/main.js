import './style.css';

import {
    Bootstrap, SetFilter, SetShowDebug, CycleMinLevel, SetWatchdog,
    StartBot, StopBot, RestartBot, ClearLog, RestartApp, CheckForUpdate,
    SetUpdateChannel, PreflightChecks, ConnectRemote, DisconnectRemote, TestRemoteConnection,
    CheckChannels, ApplyUpdateFrom, FixEnvironment, DetectSSHKeys,
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
      <button class="gate-primary preflight-fix" id="preflight-fix" style="display:none">Установить недостающее</button>
      <button class="gate-primary preflight-connect" id="preflight-connect" style="display:none">Подключиться по SSH</button>
      <button class="preflight-continue" id="preflight-continue" style="display:none">продолжить всё равно</button>
    </div>
  </div>

  <!-- ── экран обновлений: сразу после проверок, до самого окна ──────────
       Показывается только если есть что предложить (ChannelsResult.Offer).
       Два канала рядом, каждый со своей датой публикации — сравнить их по
       номеру нельзя (в dev-теге хеш коммита, у хеша нет порядка), поэтому
       здесь только факты, а выбор за человеком. -->
  <div class="gate-overlay" id="update-gate">
    <div class="gate-card">
      <div class="preflight-head">
        <span class="preflight-brand">ОБНОВЛЕНИЯ</span>
        <span class="preflight-sub" id="gate-current"></span>
      </div>
      <div class="gate-list" id="gate-list"></div>
      <button class="gate-skip" id="gate-skip">продолжить без обновления</button>
    </div>
  </div>

  <!-- ── ход обновления: шаги, которые selfupdate реально делает ───────── -->
  <div class="gate-overlay" id="update-progress">
    <div class="gate-card">
      <div class="preflight-head">
        <span class="preflight-brand">ОБНОВЛЕНИЕ</span>
        <span class="preflight-sub" id="up-target"></span>
      </div>
      <div class="up-steps" id="up-steps"></div>
      <div class="up-foot" id="up-foot"></div>
      <button class="preflight-continue" id="up-close" style="display:none">закрыть</button>
      <button class="gate-primary" id="up-restart" style="display:none">перезапустить сейчас</button>
    </div>
  </div>
  <!-- ── рельса: вся хром-обвязка окна в одной вертикальной полосе слева —
       управление процессом, режимы показа, версия, обновления. Раньше это
       было двумя горизонтальными полосами сверху (шапка + статус-строка) —
       теперь ровно один пульт слева, всё содержимое (лог) получает
       оставшуюся ширину целиком, а не делит высоту с чужой обвязкой. -->
  <div class="rail" id="app-rail">
    <div class="traffic">
      <button class="traffic-dot c" id="btn-close" title="Закрыть"><span>×</span></button>
      <button class="traffic-dot m" id="btn-minimise" title="Свернуть"><span>−</span></button>
      <button class="traffic-dot z" id="btn-maximise" title="Развернуть"><span>+</span></button>
    </div>

    <div class="rail-brand">
      <span class="brand">HEROKU</span>
      <span class="version" id="hkc-version"></span>
    </div>

    <div class="rail-group">
      <button id="btn-start" class="rail-btn primary" title="Запустить бота">▶ Start</button>
      <button id="btn-restart" class="rail-btn" title="Перезапустить бота">⟳ Restart</button>
      <button id="btn-stop" class="rail-btn danger" title="Остановить бота">■ Stop</button>
    </div>

    <div class="rail-group">
      <button id="btn-debug" class="rail-btn" title="Показывать строки уровня DEBUG (d)">debug</button>
      <button id="btn-level" class="rail-btn" title="Порог показа: всё → warning+ → error+ (w)">порог: всё</button>
      <button id="btn-watchdog" class="rail-btn" title="Перезапускать бота автоматически, если он упал (a)">auto-restart</button>
      <button id="btn-clear" class="rail-btn" title="Очистить экран лога — файл на диске не трогается">Очистить</button>
    </div>

    <div class="rail-spacer"></div>

    <div class="rail-group rail-bottom">
      <button class="update-badge" id="update-badge" data-state="idle" title="Проверить обновления">⟲ обновить</button>
      <button class="channel-badge" id="channel-badge" data-channel="" title="Канал обновлений — клик переключает stable/dev">stable</button>
      <button id="btn-help" class="rail-btn" title="Горячие клавиши (?)">? справка</button>
    </div>
  </div>

  <div class="main-col">
    <!-- ── статус-строка: всё, что нужно знать не отрываясь от лога ────── -->
    <div class="statusline">
      <span class="sl-seg status-pill down" id="status-pill">
        <span class="status-dot"></span><span id="status-text">…</span>
      </span>
      <span class="sl-seg sl-warn" id="sl-warn">⚠ 0</span>
      <span class="sl-seg sl-err" id="sl-err">✗ 0</span>
      <button class="sl-seg sl-mods" id="btn-mods" title="Самые шумные модули — клик открывает полный список (s)">
        <span id="sl-mods-names">—</span><span class="sl-caret">▾</span>
      </button>
      <!-- порог — жёлтым, а не акцентом: он означает «часть строк сейчас
           скрыта», и путать его с активным фильтром одним цветом нельзя -->
      <span id="threshold-badge" class="sl-seg sl-chip warn" style="display:none"></span>
      <span id="filter-badge" class="sl-seg sl-chip accent" style="display:none"></span>
      <span id="sl-restarting" class="sl-seg sl-chip warn" style="display:none">⟳ перезапускаю…</span>
      <div class="sl-spacer"></div>
      <button class="sl-seg sl-conn" id="sl-conn" style="display:none" title="Подключение — клик настроит">
        <span class="conn-dot"></span><span id="sl-conn-label"></span>
      </button>
    </div>

    <!-- ── панель модулей: выезжает справа во всю высоту ────────────────
         Полный список, отсортированный по шуму — те же данные, что и топ-3
         прямо в статус-строке, но с местом на счёт и полосу объёма у
         каждого. Позиция фиксирована (правый край) — координаты кнопки
         больше не нужны, см. openMods в main.js. -->
    <div class="mod-menu" id="mod-menu">
      <div class="mod-menu-head">модули · по шуму</div>
      <div class="module-list" id="module-list"></div>
      <div class="mod-menu-empty" id="mod-menu-empty">пока ни одного модуля</div>
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
          <input type="text" id="filter-input" placeholder="подстрока для поиска… (re:шаблон — регулярное выражение)" autocomplete="off" />
        </div>
      </div>
    </div>
  </div>

  <div class="help-overlay" id="help-overlay">
    <div class="help-card">
      <h2>Горячие клавиши</h2>
      <table>
        <tr><td><kbd>d</kbd></td><td>показать/скрыть строки уровня DEBUG</td></tr>
        <tr><td><kbd>s</kbd></td><td>список модулей</td></tr>
        <tr><td><kbd>w</kbd></td><td>порог показа по кругу: всё → warning+ → error+</td></tr>
        <tr><td><kbd>a</kbd></td><td>авто-перезапуск бота при падении</td></tr>
        <tr><td><kbd>/</kbd></td><td>поиск: подстрока или <code>re:шаблон</code></td></tr>
        <tr><td><kbd>n</kbd> <kbd>N</kbd></td><td>к следующей / предыдущей проблеме (warning и выше)</td></tr>
        <tr><td><kbd>r</kbd> <kbd>R</kbd></td><td>к следующему / предыдущему перезапуску</td></tr>
        <tr><td><kbd>g</kbd> <kbd>G</kbd></td><td>в начало / в конец лога</td></tr>
        <tr><td><kbd>клик</kbd></td><td>по модулю в панели — добавить/убрать из фильтра, можно несколько сразу</td></tr>
        <tr><td><kbd>клик</kbd></td><td>по ⧉ у строки — скопировать её (с трейсбеком, если есть)</td></tr>
        <tr><td><kbd>клик</kbd></td><td>по ⊙ у warning/error — все повторения этой записи одним списком</td></tr>
        <tr><td><kbd>Esc</kbd></td><td>закрыть справку, панель модулей и список повторений, снять фильтр</td></tr>
      </table>
      <p class="help-foot">Те же клавиши, что в консольной версии <code>hkc</code>.</p>
    </div>
  </div>

  <!-- ── все повторения одной записи ──────────────────────────────────────
       ⊙ у строки warning/error открывает это окно: не панель, что живёт
       постоянно (как модули), а разовый снимок — какой бы бот молчаливый ни
       был, сама запись никуда не убегает, пока окно открыто. -->
  <div class="matches-overlay" id="matches-overlay">
    <div class="matches-card">
      <div class="matches-head">
        <span class="matches-title" id="matches-title"></span>
        <button class="matches-close" id="matches-close" title="Закрыть (Esc)">×</button>
      </div>
      <div class="matches-list" id="matches-list"></div>
      <div class="matches-empty" id="matches-empty" style="display:none">кроме этой — повторений не нашлось</div>
    </div>
  </div>
  <div class="remote-overlay" id="remote-overlay">
    <div class="remote-card">
      <h2>Удалённый бот</h2>
      <p class="remote-hint">Бот управляется на другой машине по SSH — окно только показывает и командует, ничего из бота на этом компьютере не хранится.</p>
      <label>Хост или IP<input type="text" id="remote-host" placeholder="192.168.31.128" autocomplete="off"></label>
      <label>Пользователь<input type="text" id="remote-user" placeholder="ayanami" autocomplete="off"></label>
      <label>Приватный ключ<input type="text" id="remote-key" list="remote-key-options" placeholder="C:\Users\...\.ssh\id_ed25519" autocomplete="off"></label>
      <datalist id="remote-key-options"></datalist>
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
const thresholdBadge = el('threshold-badge');
const filterBadge = el('filter-badge');
const moduleList = el('module-list');
const modMenu = el('mod-menu');
const modMenuEmpty = el('mod-menu-empty');
const slRestarting = el('sl-restarting');
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
const updateBadge = el('update-badge');
const channelBadge = el('channel-badge');
const helpOverlay = el('help-overlay');
const matchesOverlay = el('matches-overlay');
const matchesTitle = el('matches-title');
const matchesList = el('matches-list');
const matchesEmpty = el('matches-empty');
const matchesClose = el('matches-close');
const btnMods = el('btn-mods');
const slWarn = el('sl-warn');
const slErr = el('sl-err');
const slMods = el('sl-mods-names');
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
const preflightFix = el('preflight-fix');
const preflightConnect = el('preflight-connect');
const updateGate = el('update-gate');
const gateList = el('gate-list');
const gateCurrent = el('gate-current');
const gateSkip = el('gate-skip');
const updateProgress = el('update-progress');
const upTarget = el('up-target');
const upSteps = el('up-steps');
const upFoot = el('up-foot');
const upClose = el('up-close');
const upRestart = el('up-restart');
const streamIssueReason = el('si-reason');
const streamIssueFor = el('si-for');
const remoteOverlay = el('remote-overlay');
const remoteHostInput = el('remote-host');
const remoteUserInput = el('remote-user');
const remoteKeyInput = el('remote-key');
const remoteKeyOptions = el('remote-key-options');
const remoteDirInput = el('remote-dir');
const remoteNote = el('remote-note');
const btnRemoteTest = el('btn-remote-test');
const btnRemoteConnect = el('btn-remote-connect');
const btnRemoteDisconnect = el('btn-remote-disconnect');
const btnRemoteCancel = el('btn-remote-cancel');

// ─── состояние ───────────────────────────────────────────────────────────

let uiState = { showDebug: false, minLevel: LEVEL_DEBUG, watchdog: false };
let lastRowEl = null;   // DOM-узел последней записи — для схлопывания повторов
let moduleStats = new Map(); // имя → {count, warn, err}
// selectedModules — множественный выбор в панели модулей: клик добавляет/
// убирает модуль, а не заменяет фильтр целиком, как раньше. Собранное в
// filterFromSelectedModules() ОБЪЕДИНЕНИЕ модулей — то же самое, что можно
// набрать руками через "re:", просто без ручного экранирования точек в
// именах модулей.
let selectedModules = new Set();
// modRowEls — переиспользуемые DOM-узлы строк панели модулей, по имени.
// renderModules раньше пересоздавал список целиком (innerHTML='') на каждый
// вызов, а вызывается он и раз в секунду, и на каждое обновление лога —
// строка играла анимацию появления заново каждый раз, даже когда в ней не
// менялось вообще ничего. Теперь существующая строка переиспользуется и
// просто обновляется на месте; анимация остаётся только для НОВОЙ строки
// и для перестановки при смене места в сортировке (см. renderModules).
let modRowEls = new Map();
// modOrder — текущий видимый порядок строк (имена), отдельно от «истинной»
// сортировки по счётчику. renderModules раньше сортировал по count заново
// каждый вызов — при всплеске лога, где счётчики десятка модулей меняются
// за один тик, это двигало разом всё, что оказалось между старым и новым
// местом строки (плотный список, без зазоров), и выглядело как общая
// тряска, а не как «эти два поменялись местами». Теперь на каждый вызов
// список лишь ОДИН проход бабл-сорта: каждая строка может обогнать соседа
// максимум на одну позицию за раз, если её счётчик больше. Дальний прыжок
// (был 40-м, стал первым) займёт несколько тиков и на каждом будет видно
// ровно одну смену местами, а не единомоментный скачок через весь список.
let modOrder = [];
// openPeak — счётчик самого шумного модуля, зафиксированный в момент
// открытия панели (resortModOrder). См. комментарий в renderModules —
// шкала объёма больше не гоняется за живым максимумом каждый тик.
let openPeak = 1;

function escapeRegex(s) { return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'); }

function filterFromSelectedModules() {
    const names = [...selectedModules];
    if (names.length === 0) return '';
    if (names.length === 1) return names[0];
    return 're:^(' + names.map(escapeRegex).join('|') + ')$';
}
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
    const msgClass = rec.level === LEVEL_CRITICAL ? 'crt' : rec.level === LEVEL_ERROR ? 'err'
        : rec.level === LEVEL_WARNING ? 'wrn' : rec.level === LEVEL_DEBUG ? 'dbg' : '';
    msg.className = 'msg' + (msgClass ? ' ' + msgClass : '');
    const filterValue = filterInput.value;
    (rec.lines || ['']).forEach((line, i) => {
        if (i > 0) {
            const br = document.createElement('div');
            const arrow = document.createElement('span');
            arrow.className = 'continuation-arrow';
            arrow.textContent = '↳ ';
            br.appendChild(arrow);
            appendHighlighted(br, line, filterValue);
            msg.appendChild(br);
        } else {
            appendHighlighted(msg, line, filterValue);
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
    // Строка показывает только часы:минуты:секунды — heroku.log живёт
    // неделями, без даты в подсказке "вчера" от "три дня назад" не отличить.
    if (rec.date) time.title = rec.date + ' ' + rec.time;
    row.appendChild(time);

    const badge = document.createElement('span');
    badge.className = 'badge ' + BADGE_CLASS[rec.level];
    badge.textContent = BADGES[rec.level];
    row.appendChild(badge);

    const mod = document.createElement('span');
    mod.className = 'module';
    mod.style.color = moduleColor(rec.module);
    mod.title = rec.module; // колонка узкая, длинное имя обрезается многоточием
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

    // "Все повторения" — только у проблемных записей (warning и выше): у
    // обычных INF компилировать нечего смотреть, а кнопка на девяти строках
    // из десяти была бы чистым шумом.
    if (rec.level >= LEVEL_WARNING) {
        const matchBtn = document.createElement('span');
        matchBtn.className = 'row-copy row-match';
        matchBtn.title = 'Показать все повторения этой ошибки';
        matchBtn.textContent = '⊙';
        matchBtn.addEventListener('click', (e) => {
            e.stopPropagation();
            openMatches(rec);
        });
        row.appendChild(matchBtn);
    }

    row._rec = rec;
    return row;
}

function updateRowCount(rowEl, rec) {
    rowEl._rec = rec;
    const time = rowEl.querySelector('.time');
    time.textContent = rec.time || '';
    if (rec.date) time.title = rec.date + ' ' + rec.time;
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
    renderCounts();
    renderModules();
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
        // Каждое событие — одно срабатывание, даже если оно легло повтором в
        // уже показанную строку (replace): счётчик не про число строк на
        // экране, а про то, сколько раз это реально произошло.
        if (evt.rec.warn) warnCount++;
        else if (evt.rec.err) errCount++;
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
//   idle      → клик проверяет (бейдж выставляется при старте из
//               CheckChannels — см. openUpdateGate)
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
// applyUpdateResult раскладывает ответ ручной проверки по состояниям бейджа.
// Отдельного «тоста» с предложением обновиться больше нет: при старте окна
// то же самое, только подробнее и про оба канала сразу, показывает экран
// обновлений (openUpdateGate), и держать два способа сообщить одну новость
// значило бы вернуть ровно то дублирование, ради снятия которого убирали
// панель в 1.14.0.
function applyUpdateResult(res, manual) {
    if (res.available) {
        updateInfo = { version: res.latest, url: res.url };
        setUpdateBadge('available', '↑ ' + res.latest,
            `Доступна версия ${res.latest} — клик обновит на месте, ПКМ откроет релиз в браузере`);
        return;
    }
    updateInfo = null;
    if (res.ok) {
        setUpdateBadge('uptodate', '✓ актуально',
            `Уже последняя версия (${res.current}) — клик проверит ещё раз`);
    } else if (manual) {
        setUpdateBadge('error', '⚠ ошибка проверки', `Не удалось проверить: ${res.message} — клик попробует снова`);
    }
}

// startUpdate — путь «бейдж в шапке». Ведёт в то же самое окно с шагами, что
// и экран обновлений при старте: одно действие должно выглядеть одинаково,
// откуда бы его ни запустили, и «⟳ обновляю…» одной строкой в бейдже было
// ровно тем непрозрачным ожиданием, ради которого окно и заводилось.
async function startUpdate() {
    setUpdateBadge('updating', '⟳ обновляю…', 'Скачиваю и подменяю приложение на диске');
    const channel = channelBadge.dataset.channel === 'dev' ? 'dev' : '';
    await startUpdateFrom(channel, updateInfo ? updateInfo.version : '');
}
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
    setUpdateBadge('checking', '⟳ проверяю…', 'Проверяю GitHub на новый релиз');
    applyUpdateResult(await CheckForUpdate(), true);
});

// ─── канал обновлений ───────────────────────────────────────────────────
//
// stable — релизы из main (как раньше, единственный вариант). dev — сборки
// прямо с ветки dev (см. selfupdate.CheckChannel/dev-release.yml): не для
// повседневной работы, зато не нужно ждать, пока что-то из dev дойдёт до
// релиза, чтобы попробовать. Клик переключает и сразу перепроверяет —
// separate CheckForUpdate после переключения был бы тем же самым в два шага.
function renderChannelBadge(channel) {
    const dev = channel === 'dev';
    channelBadge.dataset.channel = channel || '';
    channelBadge.textContent = dev ? 'dev' : 'stable';
    channelBadge.title = dev
        ? 'Канал: dev — сборки прямо с ветки разработки, могут быть сырыми. Клик — вернуться на stable'
        : 'Канал: stable — только проверенные релизы. Клик — переключиться на dev';
}

channelBadge.addEventListener('click', async () => {
    const next = channelBadge.dataset.channel === 'dev' ? '' : 'dev';
    channelBadge.disabled = true;
    setUpdateBadge('checking', '⟳ проверяю…', 'Проверяю GitHub на новый релиз');
    const res = await SetUpdateChannel(next);
    channelBadge.disabled = false;
    renderChannelBadge(res.channel);
    applyUpdateResult(res, true);
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
// Отдельная псевдо-проверка preflight.Unsupported() (единственная строка
// "локальный режим") — единственный случай, где "Установить недостающее"
// бессмысленно предлагать: FixEnvironment на не-Linux всё равно откажет.
// Тут нужна не установка, а прямое подключение по SSH.
let preflightUnsupported = false;

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
        if (preflightUnsupported) {
            preflightConnect.style.display = '';
        } else {
            preflightFix.style.display = '';
        }
        preflightContinue.style.display = '';
    } else {
        preflightStatus.textContent = 'всё готово';
        preflightStatus.className = 'preflight-status ok';
        // Короткая пауза, чтобы взгляд успел долистать до "всё готово" —
        // без неё зелёная строка появлялась бы и тут же сменялась логом.
        setTimeout(dismissPreflight, 900);
    }
}

// dismissPreflight гасит экран проверок и передаёт эстафету экрану
// обновлений — не наоборот: проверки окружения про бота, обновления про
// саму консоль, мешать их в один список значило бы отвечать двумя разными
// вопросами в одной строке.
function dismissPreflight() {
    preflightOverlay.classList.add('dismissed');
    openUpdateGate();
}

preflightContinue.addEventListener('click', dismissPreflight);

// Установка недостающего идёт не здесь, а в НАСТОЯЩЕМ окне терминала,
// которое открывает FixEnvironment: часть шагов просит пароль sudo, а
// webview его показать негде — молчаливый вызов просто завис бы навсегда на
// невидимом запросе. Экран проверок при этом не гасим: терминал открылся
// рядом, и, когда он закончит, сюда возвращаются и перезапускают окно —
// перепроверять окружение на лету, пока apt ещё ставит пакеты, значило бы
// показывать заведомо промежуточный результат.
preflightFix.addEventListener('click', async () => {
    preflightFix.disabled = true;
    const res = await FixEnvironment();
    if (res.ok) {
        preflightStatus.textContent = 'открыл окно установки — вернитесь сюда, когда закончится';
        preflightStatus.className = 'preflight-status';
    } else {
        preflightFix.disabled = false;
        preflightStatus.textContent = 'не удалось открыть терминал: ' + res.message;
        preflightStatus.className = 'preflight-status bad';
    }
});

// Открывает ту же форму настройки удалённого подключения, что и клик по
// сегменту в шапке (slConn) — только доступную поверх экрана проверок:
// на не-Linux он завешивает окно первым же кадром, и до самой шапки
// пользователь бы просто не добрался.
preflightConnect.addEventListener('click', () => openRemoteOverlay(uiState.remote));

// ─── экран обновлений ────────────────────────────────────────────────────
//
// Опрос обоих каналов идёт параллельно на бэкенде (selfupdate.CheckBoth) и
// НЕ задерживает запуск: экран проверок уже погашен, окно с логом под ним
// живое, а этот оверлей всплывает поверх, только если действительно есть
// что предложить. Сеть молчит — не всплывает вовсе, и никто ничего не ждёт.
const CHANNEL_TITLE = { '': 'stable', dev: 'dev' };

// humanAgo — «когда вышло» словами. Точная дата тут не нужна: вопрос, на
// который отвечает строка, — «свежее ли это того, что у меня», а не «какого
// числа собрано».
function humanAgo(iso) {
    if (!iso) return '';
    const then = new Date(iso).getTime();
    if (!Number.isFinite(then)) return '';
    const mins = Math.max(0, Math.round((Date.now() - then) / 60000));
    if (mins < 1) return 'только что';
    if (mins < 60) return mins + ' мин назад';
    const hours = Math.round(mins / 60);
    if (hours < 24) return hours + ' ч назад';
    return Math.round(hours / 24) + ' дн назад';
}

function humanBytes(n) {
    if (!n || n < 0) return '';
    if (n < 1024 * 1024) return (n / 1024).toFixed(0) + ' КБ';
    return (n / 1048576).toFixed(1) + ' МБ';
}

async function openUpdateGate() {
    let res;
    try {
        res = await CheckChannels();
    } catch {
        return; // не смогли спросить — молча живём дальше, как и checkUpdateOnce
    }
    if (!res) return;

    // Бейдж в шапке выставляется из этого же ответа — отдельной проверки при
    // старте окна больше нет (см. gui/app.go startup): она спрашивала GitHub
    // о том, что здесь уже известно, и удваивала расход часового лимита API.
    const own = res.channel === 'dev' ? res.dev : res.stable;
    if (own && own.ok && !own.isCurrent) {
        updateInfo = { version: own.tag, url: own.url };
        setUpdateBadge('available', '↑ ' + own.tag,
            `Доступна версия ${own.tag} — клик обновит на месте, ПКМ откроет релиз в браузере`);
    } else if (own && own.ok) {
        setUpdateBadge('uptodate', '✓ актуально', `Уже последняя версия (${res.current}) — клик проверит ещё раз`);
    }

    if (!res.offer) return;

    gateCurrent.textContent = 'сейчас запущено: ' + (res.current || 'dev');
    gateList.innerHTML = '';
    for (const st of [res.stable, res.dev]) {
        if (!st) continue;
        const row = document.createElement('div');
        row.className = 'gate-row';

        const name = document.createElement('span');
        name.className = 'gate-chan' + (st.channel === 'dev' ? ' dev' : '');
        name.textContent = CHANNEL_TITLE[st.channel] ?? st.channel;
        row.appendChild(name);

        const tag = document.createElement('span');
        tag.className = 'gate-tag';
        tag.textContent = st.ok ? st.tag : '—';
        row.appendChild(tag);

        const when = document.createElement('span');
        when.className = 'gate-when';
        // Канал не ответил — показываем причину, а не прячем строку: пустое
        // место читалось бы как «в этом канале ничего нет», что неправда.
        when.textContent = st.ok ? humanAgo(st.publishedAt) : (st.message || 'не удалось спросить');
        if (!st.ok) when.classList.add('bad');
        row.appendChild(when);

        const act = document.createElement('span');
        act.className = 'gate-act';
        if (st.ok && st.isCurrent) {
            act.textContent = 'сейчас у вас';
            act.classList.add('is-current');
        } else if (st.ok) {
            const btn = document.createElement('button');
            // Переход на другой канал — это ещё и смена канала навсегда
            // (ApplyUpdateFrom её сохраняет), поэтому подпись честно говорит
            // «перейти», а не «обновить», когда канал не тот, где сидим.
            btn.textContent = st.channel === res.channel ? 'Обновить' : 'Перейти';
            btn.className = st.channel === res.channel ? 'gate-primary' : '';
            btn.addEventListener('click', () => startUpdateFrom(st.channel, st.tag));
            act.appendChild(btn);
        }
        row.appendChild(act);

        gateList.appendChild(row);
    }
    updateGate.classList.add('visible');
}

gateSkip.addEventListener('click', () => updateGate.classList.remove('visible'));

// ─── ход обновления ──────────────────────────────────────────────────────
//
// Шаги ровно те, что selfupdate реально проходит (см. selfupdate.Stage), а
// не выдуманные ради заполнения экрана. Скачивание и распаковка — ОДНА
// строка сознательно: tar тянет из gzip, gzip из сети, они происходят
// одновременно, и отдельная строка «распаковка» открылась бы и закрылась в
// один и тот же кадр, изображая шаг, которого во времени не существует.
const UP_STEPS = [
    { stage: 'query', label: 'запрос к GitHub' },
    { stage: 'find', label: 'поиск сборки в релизе' },
    { stage: 'download', label: 'скачивание и распаковка' },
    { stage: 'swap', label: 'подмена на диске' },
];

let upRows = new Map();

function renderUpSteps() {
    upSteps.innerHTML = '';
    upRows = new Map();
    for (const s of UP_STEPS) {
        const row = document.createElement('div');
        row.className = 'up-row';
        row.dataset.state = 'pending';
        // data-stage нужен не JS (тот держит Map), а CSS: полоса прогресса
        // положена только скачиванию, у остальных шагов нет ни объёма, ни
        // длительности — без этого признака пустая серая линия висела бы
        // под каждым шагом, изображая прогресс, которого нет.
        row.dataset.stage = s.stage;
        row.innerHTML =
            '<span class="up-dot"></span>' +
            '<span class="up-label"></span>' +
            '<span class="up-note"></span>' +
            '<span class="up-bar"><span class="up-bar-fill"></span></span>';
        row.querySelector('.up-label').textContent = s.label;
        upSteps.appendChild(row);
        upRows.set(s.stage, row);
    }
}

async function startUpdateFrom(channel, tag) {
    updateGate.classList.remove('visible');
    upTarget.textContent = 'до ' + (tag || '?') + (channel === 'dev' ? ' · канал dev' : '');
    renderUpSteps();
    upFoot.textContent = '';
    upFoot.className = 'up-foot';
    upClose.style.display = 'none';
    upRestart.style.display = 'none';
    updateProgress.classList.add('visible');

    const res = await ApplyUpdateFrom(channel);
    if (res.ok) {
        upFoot.textContent = 'обновлено до ' + res.message;
        upFoot.className = 'up-foot ok';
        upRestart.style.display = '';
        setUpdateBadge('done', '✓ перезапустить', `Обновлено до ${res.message} — клик перезапустит окно`);
    }
    // Ошибка приезжает событием 'update-failed' ниже — оно же гасит
    // крутящийся шаг, чего один только возврат сделать не может.
}

EventsOn('update-step', (p) => {
    // Распаковка закрывается вместе со скачиванием — своей строки у неё нет
    // (см. UP_STEPS), поэтому событие про неё просто некуда класть.
    if (p.stage === 'unpack') return;
    if (p.stage === 'done') return;

    const row = upRows.get(p.stage);
    if (!row) return;

    row.dataset.state = p.done ? 'done' : 'running';
    const note = row.querySelector('.up-note');
    const bar = row.querySelector('.up-bar');
    const fill = row.querySelector('.up-bar-fill');

    if (p.stage === 'download') {
        // Полоса только когда сервер назвал размер: рисовать проценты от
        // неизвестного целого — врать. Без Content-Length остаётся честное
        // «сколько уже приехало», без доли.
        if (p.total > 0) {
            bar.style.display = '';
            fill.style.width = Math.min(100, Math.round((p.bytes / p.total) * 100)) + '%';
            note.textContent = humanBytes(p.bytes) + ' / ' + humanBytes(p.total);
        } else {
            bar.style.display = 'none';
            note.textContent = humanBytes(p.bytes);
        }
        if (p.done && p.total > 0) fill.style.width = '100%';
    } else if (p.note) {
        note.textContent = p.note;
    }

    if (p.done) {
        const i = UP_STEPS.findIndex((s) => s.stage === p.stage);
        const next = UP_STEPS[i + 1];
        if (next) upRows.get(next.stage).dataset.state = 'running';
    }
});

EventsOn('update-failed', (msg) => {
    for (const row of upRows.values()) {
        if (row.dataset.state === 'running') row.dataset.state = 'failed';
    }
    upFoot.textContent = 'не удалось: ' + msg;
    upFoot.className = 'up-foot bad';
    upClose.style.display = '';
    // Бейдж в шапке возвращается в «есть новее»: обновление не состоялось,
    // и оставить его в «⟳ обновляю…» значило бы врать после закрытия окна.
    if (updateInfo) {
        setUpdateBadge('available', '↑ ' + updateInfo.version, `Не удалось обновить: ${msg} — клик попробует снова`);
    } else {
        setUpdateBadge('error', '⚠ ошибка', `Не удалось обновить: ${msg} — клик проверит снова`);
    }
    showBanner(`✗  обновление: ${msg}`, true);
});

upClose.addEventListener('click', () => updateProgress.classList.remove('visible'));
upRestart.addEventListener('click', () => RestartApp());

function renderPreflightChecks(names) {
    if (!names || names.length === 0) {
        dismissPreflight();
        return;
    }
    preflightUnsupported = names.length === 1 && names[0] === 'локальный режим';
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
// Индикатор подключения показывается не всегда. На машине, где бот и так
// установлен рядом (Linux), удалённый режим — не то, ради чего открывают
// окно: сегмент «локально» занимал бы место, ничего при этом не сообщая.
// Он существует ради Windows, где локального бота быть не может.
// Исключение — если удалённый хост всё-таки настроен: тогда сегмент нужен
// везде, иначе настройку негде было бы увидеть и отменить.
//
// Раньше про подключение говорили два элемента сразу: сегмент в статус-
// строке и отдельный блок в панели, дублировавший его слово в слово.
// Остался один — он же и кнопка, открывающая настройку, вместо отдельной
// кнопки «Удалённый бот» под блоком.
let platform = 'linux';

function showConnSection(remote) {
    const show = platform === 'windows' || (remote && remote.host);
    slConn.style.display = show ? '' : 'none';
}

// Куда подключены — отдельно от того, в каком состоянии подключение:
// адрес меняется только по воле пользователя, состояние — само по себе,
// каждую секунду (см. Status.RemoteState).
function renderRemoteTarget(remote) {
    if (remote && remote.host) {
        slConn.dataset.state = 'connecting';
        slConnLabel.textContent = remote.host;
        btnRemoteDisconnect.style.display = '';
        return;
    }
    slConn.dataset.state = 'local';
    btnRemoteDisconnect.style.display = 'none';
    // «Локально» на Windows означало бы бота, которого там нет и быть не
    // может: сам Heroku запускается только на Linux. Пока машина не
    // указана, окну попросту нечем управлять, и сказать об этом честно
    // полезнее, чем показать успокаивающее «локально».
    slConnLabel.textContent = platform === 'windows' ? 'бот не настроен' : 'локально';
}

const CONN_LABEL = { connecting: 'подключаюсь…', online: 'на связи', offline: 'нет связи' };

function renderConnState(st) {
    if (!st.remoteState) return; // локальный бот — сегмент про адрес, а не про соединение
    slConn.dataset.state = st.remoteState;
    slConnLabel.textContent = (uiState.remote && uiState.remote.host) || CONN_LABEL[st.remoteState] || st.remoteState;
    // Подробности — в подсказке, а не отдельной строкой: сегмент обязан
    // оставаться одной строкой в один ряд с остальными.
    slConn.title = [CONN_LABEL[st.remoteState] || st.remoteState, st.remoteStateNote]
        .filter(Boolean).join(' · ') + ' — клик настроит';
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

    // Ключей в ~/.ssh может быть несколько — самый свежий сразу подставляем
    // в поле (для однократно сгенерированного под это подключение ключа
    // почти всегда верно), а остальные кладём в datalist: браузерная
    // подсказка под полем, из которой можно выбрать другой без похода в
    // проводник за путём. Список не трогаем, если ключ уже вписан руками.
    remoteKeyOptions.innerHTML = '';
    DetectSSHKeys().then((paths) => {
        if (!paths || paths.length === 0) return;
        paths.forEach((p) => {
            const opt = document.createElement('option');
            opt.value = p;
            remoteKeyOptions.appendChild(opt);
        });
        if (!remoteKeyInput.value) remoteKeyInput.value = paths[0];
    });
}

function closeRemoteOverlay() {
    remoteOverlay.classList.remove('visible');
}

slConn.addEventListener('click', () => openRemoteOverlay(uiState.remote));
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

function renderStatus(st) {
    el('hkc-version').textContent = st.hkcVersion || 'dev';

    renderConnState(st);

    statusPill.classList.remove('live', 'down', 'warn');
    if (st.streamIssue) {
        statusPill.classList.add('warn');
        statusText.textContent = '⚠ лог не читается';
        // statusLoop зовёт renderStatus раз в секунду, пока проблема не
        // решится, — но выставить уже выставленный display ничего не
        // перезапускает, так что анимация появления играет ровно один раз.
        streamIssueBox.style.display = '';
        streamIssueReason.textContent = st.streamIssue;
        streamIssueFor.textContent = st.streamIssueFor || '';
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

    // pid раньше жил отдельной строкой в панели («Процесс»), где дублировал
    // uptime из этой же пилюли. Он нужен редко и ровно один раз — когда
    // хочется убить процесс руками мимо окна, — так что подсказка честнее
    // постоянно занятого места.
    statusPill.title = st.alive ? `pid ${st.pid} · запущен ${st.uptime} назад` : 'бот не запущен';

    slRestarting.style.display = st.restarting ? '' : 'none';
    btnWatchdog.classList.toggle('toggled', !!st.watchdog);

    // Заголовок ОКНА, а не вкладки браузера — Wails не тянет document.title
    // в нативный тайтлбар сам, нужен его собственный runtime-вызов.
    WindowSetTitle(st.alive ? `Heroku · ⚠ ${warnCount} ✗ ${errCount}` : 'Heroku · не запущен');
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

// renderCounts — дешёвая часть статус-строки: счётчики и бейджи, только
// textContent и toggle стилей. Зовётся на каждый пакет строк, даже во время
// всплеска лога — здесь нет ничего, что стоило бы приберечь.
function renderCounts() {
    slWarn.textContent = '⚠ ' + warnCount;
    slErr.textContent = '✗ ' + errCount;
    slWarn.classList.toggle('zero', warnCount === 0);
    slErr.classList.toggle('zero', errCount === 0);

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

// buildModuleRow создаёт DOM-узел строки один раз — при первом появлении
// модуля в списке. Дальше он живёт в modRowEls и просто обновляется на
// месте (updateModuleRow), а не пересоздаётся: пересоздание на каждый
// вызов было тем самым источником "мигания" — заново игралась анимация
// появления у строки, в которой ничего не изменилось, кроме счётчика.
function buildModuleRow(name) {
    const row = document.createElement('div');
    row.className = 'module-row clickable';
    // Клик добавляет/убирает модуль из выбора — можно накопить несколько
    // сразу (объединяются через "re:", см. filterFromSelectedModules),
    // панель при этом не закрывается: закрыть её отдельное действие
    // (клик по сегменту в статус-строке или мимо панели), а не побочный
    // эффект выбора модуля.
    row.addEventListener('click', () => {
        if (selectedModules.has(name)) selectedModules.delete(name);
        else selectedModules.add(name);
        applyFilter(filterFromSelectedModules());
        renderModules();
    });

    // Точка — состояние модуля (проблема ярче самого частого случая, что
    // это просто разные модули): свой цвет в норме, warn/err — цветом
    // проблемы. Полоса ниже красит объём тем же цветом модуля всегда —
    // так объём и состояние не спорят за один и тот же сигнал.
    const dot = document.createElement('span');
    dot.className = 'module-dot';
    row.appendChild(dot);

    const label = document.createElement('span');
    label.className = 'module-name';
    label.textContent = name;
    row.appendChild(label);

    const meter = document.createElement('span');
    meter.className = 'meter';
    const fill = document.createElement('span');
    fill.className = 'fill';
    meter.appendChild(fill);
    row.appendChild(meter);

    const count = document.createElement('span');
    count.className = 'module-count';
    row.appendChild(count);

    return row;
}

function updateModuleRow(row, m, isSelected, peak) {
    row.classList.toggle('active', isSelected);
    row.title = isSelected ? 'убрать из фильтра' : 'добавить в фильтр';
    const dot = row.querySelector('.module-dot');
    dot.style.background = m.err > 0 ? 'var(--critical)' : m.warn > 0 ? 'var(--warning)' : moduleColor(m.name);
    const name = row.querySelector('.module-name');
    name.className = 'module-name' + (m.err > 0 ? ' has-err' : m.warn > 0 ? ' has-warn' : '');
    const fill = row.querySelector('.fill');
    // peak теперь снимок на момент открытия панели (openPeak) — модуль
    // вполне может его обогнать, пока панель открыта; клампим на 100%,
    // а не ломаем раскладку шкалы, которая физически не может быть шире.
    fill.style.width = Math.min(100, Math.round((m.count / peak) * 100)) + '%';
    fill.style.background = moduleColor(m.name);
    row.querySelector('.module-count').textContent = m.count;
}

// renderModules зовётся и раз в секунду, и на каждое обновление лога — не
// только пока панель открыта (топ-3 в самой статус-строке считаются здесь
// же). Строки — переиспользуемые DOM-узлы (modRowEls, по имени модуля), не
// пересоздаются каждый раз: только обновляются на месте. Анимация — FLIP,
// и только для того, что реально сдвинулось местами при пересортировке
// (новый модуль или сменившийся порядок по шуму); строка, которая просто
// получила +1 к счётчику и осталась на месте, не шевелится вообще.
function renderModules() {
    // "Истинная" сортировка — только для топ-3 в статус-строке. Шкала объёма
    // (peak) больше НЕ считается здесь заново каждый тик — см. openPeak ниже:
    // самый частый (по трафику) модуль почти всегда продолжает расти между
    // тиками, и пересчёт peak на его текущее значение каждую секунду сжимал
    // ВСЕ остальные полоски одновременно — не перестановка строк была
    // источником "всё двигается разом", а именно это; заморозка одного
    // порядка строк (см. ниже) эту часть не трогала и на глаз ничего не
    // меняла.
    const trueSorted = [...moduleStats.entries()]
        .map(([name, s]) => ({ name, ...s }))
        .sort((a, b) => b.count - a.count);
    // Топ-3 прямо в строке — кто вообще шумит, без единого клика; полный
    // список за тем же сегментом, если этих трёх не хватило.
    slMods.textContent = trueSorted.slice(0, 3).map((m) => m.name).join(' · ') || '—';
    modMenuEmpty.style.display = trueSorted.length ? 'none' : '';
    // openPeak — снимок peak на момент открытия панели (см. resortModOrder).
    // Пока панель открыта, шкала не гуляет вслед за самым шумным модулем;
    // если кто-то всё же обгонит замороженный peak, unclamped-ширина просто
    // держится на 100% (Math.min ниже), а не ломает раскладку.
    const peak = openPeak || 1;

    // Порядок строк больше не гонится за счётчиком на каждый тик вообще —
    // сколько ни ограничивай перестановки (один свап за раз и то было
    // заметно), пересортировка живого списка, пока на него смотрят, всегда
    // читается как дёрганье. Ранг по шуму пересчитывается ТОЛЬКО в момент
    // открытия панели (см. resortModOrder ниже, зовётся из openMods) — раз
    // открыл, увидел организованный список и он остаётся на месте, пока
    // панель открыта. Здесь — только синхронизация состава: пропавшие
    // модули вон, новые дописываются в конец (не встревают в середину списка,
    // который сейчас читают).
    modOrder = modOrder.filter((name) => moduleStats.has(name));
    for (const m of trueSorted) {
        if (!modOrder.includes(m.name)) modOrder.push(m.name);
    }

    // FLIP "First": позиции существующих строк ДО перестановки.
    const prevRects = new Map();
    for (const [name, row] of modRowEls) prevRects.set(name, row.getBoundingClientRect());

    const seen = new Set();
    const entering = [];
    // prevRow — последняя уже расставленная строка; используется, чтобы НЕ
    // трогать DOM для строк, чья позиция и так верна. appendChild безусловно
    // на каждой строке каждый тик (как было раньше) вызывает физическое
    // remove+insert узла, даже если он остаётся на своём месте, — сам факт
    // такого layout-безобидного перемещения где-то (судя по всему, в
    // WebView2, не в обычном Chromium) перезапускал CSS-анимацию появления
    // на классе .entering, который не снимается: весь список каждую секунду
    // проигрывал стаггер-анимацию заново, будто все строки только что
    // появились. Порядок не пересчитывается уже (см. выше), так что в
    // typичный тик ни одна строка не должна переставляться вообще —
    // но appendChild всё равно дёргал DOM просто по инерции цикла.
    let prevRow = null;
    modOrder.forEach((name) => {
        const s = moduleStats.get(name);
        if (!s) return; // не должно происходить (modOrder уже отфильтрован), но не рушить весь рендер, если всё же случилось
        seen.add(name);
        const isSelected = selectedModules.has(name);
        let row = modRowEls.get(name);
        if (!row) {
            row = buildModuleRow(name);
            row.style.setProperty('--i', entering.length);
            entering.push(row);
            modRowEls.set(name, row);
        }
        // moduleStats хранит {count, warn, err} без имени (ключ — сам ключ Map) —
        // updateModuleRow же читает m.name (для moduleColor). Раньше m приходило
        // из stats, где было {name, ...s}; здесь имя нужно добавить явно.
        updateModuleRow(row, { name, ...s }, isSelected, peak);
        // row.parentNode !== moduleList — обязательная часть условия: у ещё
        // не вставленного (только что созданного) узла previousElementSibling
        // тоже null, как и у prevRow на первой строке прохода. Без проверки
        // родителя новая строка молча никогда не попадала бы в DOM.
        if (row.parentNode !== moduleList || row.previousElementSibling !== prevRow) {
            if (prevRow) prevRow.after(row);
            else moduleList.prepend(row);
        }
        prevRow = row;
    });

    // Модуль пропал из статистики (обнулилась/пересобралась moduleStats,
    // см. renderAll) — узел больше не нужен.
    for (const [name, row] of modRowEls) {
        if (!seen.has(name)) { row.remove(); modRowEls.delete(name); }
    }

    // "Invert" + "Play": у новых строк — обычная анимация появления (класс
    // .entering остаётся навсегда — animation-fill-mode:forwards держит
    // конечное состояние, а сам keyframe без class-триггера повторно не
    // играет, снимать класс незачем). У всех прочих — короткий transform от
    // старой позиции к новой, только если она реально изменилась.
    for (const row of entering) row.classList.add('entering');
    for (const [name, row] of modRowEls) {
        if (entering.includes(row)) continue;
        const prev = prevRects.get(name);
        if (!prev) continue;
        const next = row.getBoundingClientRect();
        const dy = prev.top - next.top;
        if (Math.abs(dy) < 1) continue;
        row.style.transition = 'none';
        row.style.transform = `translateY(${dy}px)`;
        void row.getBoundingClientRect(); // форсирует reflow между установкой и снятием transform
        row.style.transition = 'transform .28s cubic-bezier(.19, 1, .22, 1)';
        row.style.transform = '';
        row.addEventListener('transitionend', () => { row.style.transition = ''; }, { once: true });
    }
}

// ─── панель модулей ────────────────────────────────────────────────────
//
// Панель прибита к правому краю окна (CSS), координаты кнопки ей больше не
// нужны — раньше выпадашка ставилась под сегмент «модули» вручную здесь,
// потому что тот едет по горизонтали вслед за счётчиками разной ширины;
// полноширинная панель от этого не зависит вовсе.
// resortModOrder — единственное место, где порядок строк пересчитывается
// по шуму. Зовётся только при открытии панели: список организуется один
// раз к моменту, когда на него смотрят, и дальше не трогается (renderModules
// только дописывает новые модули в конец и убирает пропавшие — см. выше).
function resortModOrder() {
    const sorted = [...moduleStats.entries()]
        .map(([name, s]) => ({ name, ...s }))
        .sort((a, b) => b.count - a.count);
    modOrder = sorted.map((m) => m.name);
    openPeak = sorted.length ? sorted[0].count : 1;
}

function openMods() {
    resortModOrder();
    renderModules();
    modMenu.classList.add('visible');
    btnMods.classList.add('open');
}

function closeMods() {
    modMenu.classList.remove('visible');
    btnMods.classList.remove('open');
}

function toggleMods() {
    if (modMenu.classList.contains('visible')) closeMods();
    else openMods();
}

btnMods.addEventListener('click', (e) => {
    e.stopPropagation(); // иначе тот же клик тут же закроет её обработчиком ниже
    toggleMods();
});
// Клик мимо закрывает — обычное поведение меню; внутри самой выпадашки
// клик по модулю закрывает её сам (см. renderModules).
document.addEventListener('click', (e) => {
    if (modMenu.classList.contains('visible') && !modMenu.contains(e.target)) closeMods();
});
window.addEventListener('resize', closeMods);

// Список модулей копит статистику на каждой строке (trackModule в «горячем»
// пути), но перерисовывается раз в секунду: на всплеске лога иначе вышли бы
// десятки полных пересборок в секунду ради чисел, которые всё равно никто
// не успевает прочитать между кадрами.
setInterval(renderModules, 1000);

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

// ─── все повторения одной записи ──────────────────────────────────────────
//
// SetFilter — единственный способ спросить бэкенд про совпадения по всему
// буферу (а не только по тому, что уже отрисовано на экране): он фильтрует
// заново от кольцевого буфера целиком, а не от текущего DOM. У бэкенда нет
// отдельного "только посчитать, не трогая текущий фильтр" — поэтому здесь
// временно подставляется фильтр записи, забирается результат, и тут же,
// не дожидаясь, фильтр возвращается обратно: с точки зрения остального
// окна (видимого лога, поля ввода) ничего не изменилось.
async function openMatches(rec) {
    const query = (rec.lines && rec.lines[0]) || '';
    if (!query) return;
    const prevFilter = filterInput.value;
    const recs = await SetFilter(query);
    if (filterInput.value === prevFilter) SetFilter(prevFilter); // не наступить на ручной ввод, случившийся за это время

    matchesTitle.textContent = query;
    matchesTitle.title = query;
    matchesList.innerHTML = '';
    const filtered = recs.filter((r) => r.lines && r.lines[0] === query);
    matchesEmpty.style.display = filtered.length <= 1 ? '' : 'none';
    filtered.forEach((r, i) => {
        const row = buildRow(r, i % 2 === 1);
        matchesList.appendChild(row);
    });
    matchesOverlay.classList.add('visible');
}

function closeMatches() {
    matchesOverlay.classList.remove('visible');
}

matchesClose.addEventListener('click', closeMatches);
matchesOverlay.addEventListener('click', (e) => {
    if (e.target === matchesOverlay) closeMatches();
});

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
    renderAll([]);
    ClearLog();
});

let filterDebounce = null;
filterInput.addEventListener('input', () => {
    // Ручной ввод в поле — уже не тот фильтр, что собрала панель модулей:
    // старый выбор (подсветка "активных" строк там) больше не отражает
    // реальность, если его не сбросить.
    selectedModules.clear();
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
            if (filterInput.value) { selectedModules.clear(); applyFilter(''); }
        }
        return;
    }

    if (helpOverlay.classList.contains('visible') && e.key !== '?') {
        // Любая клавиша закрывает справку — она перекрывает лог, и первое
        // желание после прочтения именно такое.
        toggleHelp(false);
        if (e.key === 'Escape') return;
    }

    if (matchesOverlay.classList.contains('visible')) {
        if (e.key === 'Escape') { closeMatches(); return; }
    }

    switch (e.key) {
        case 'd': btnDebug.click(); break;
        case 'w': btnLevel.click(); break;
        case 'a': btnWatchdog.click(); break;
        case 's': toggleMods(); break;
        case '?': toggleHelp(); break;
        case 'n': jumpTo('.row.problem', 1); break;
        case 'N': jumpTo('.row.problem', -1); break;
        case 'r': jumpTo('.banner', 1); break;
        case 'R': jumpTo('.banner', -1); break;
        case 'g': logScroll.scrollTop = 0; break;
        case 'G': logScroll.scrollTop = logScroll.scrollHeight; break;
        case 'Escape':
            // Сначала закрываем то, что открыто поверх лога, и только если
            // ничего не открыто — снимаем фильтр: иначе один Esc делал бы
            // два дела разом.
            if (modMenu.classList.contains('visible')) closeMods();
            else if (filterInput.value) { selectedModules.clear(); applyFilter(''); }
            break;
        case '/':
            e.preventDefault(); // иначе "/" попадёт в поле первым же символом
            filterInput.focus();
            filterInput.select();
            break;
        default: return;
    }
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

Bootstrap().then((boot) => {
    uiState = boot.uiState;
    btnDebug.classList.toggle('toggled', uiState.showDebug);
    btnWatchdog.classList.toggle('toggled', uiState.watchdog);
    btnLevel.textContent = uiState.minLevel === LEVEL_WARNING ? 'порог: warning+'
        : uiState.minLevel === LEVEL_ERROR ? 'порог: error+' : 'порог: всё';
    renderChannelBadge(uiState.updateChannel);
    platform = boot.platform || platform;
    showConnSection(uiState.remote);
    renderRemoteTarget(uiState.remote);
    renderStatus(boot.status);
    renderAll(boot.records);
});
