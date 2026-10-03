const $ = (selector) => document.querySelector(selector);
const logEl = $('#log');
const notice = $('#notice');
const filterInput = $('#filter');
const autoscroll = $('#autoscroll');
const timestamps = $('#timestamps');
const authDialog = $('#auth-dialog');
const initialInvite = new URLSearchParams(location.search).get('invite') || '';
const maxLines = 1500;
let authMode = initialInvite ? 'register' : 'login';
let authenticated = false;
let allLines = [];
let stream;
let activeLevel = 'ALL';
let streamPaused = false;
let pausedLines = [];
let logClearedByUser = false;
let currentView = 'logs';
let lastStatus = null;
let statusBusy = false;
let incidentsBusy = false;
let incidentsSignature = '';
let savedFilters = {};
let bookmarks = new Set();
let bookmarksOnly = false;
let historyRange = 'live';
try { bookmarks = new Set(JSON.parse(localStorage.getItem('hkc-log-bookmarks') || '[]')); } catch (_) { localStorage.removeItem('hkc-log-bookmarks'); }
function lineKey(raw) { let hash = 2166136261; for (let i = 0; i < raw.length; i++) hash = Math.imul(hash ^ raw.charCodeAt(i), 16777619); return (hash >>> 0).toString(36); }
function animateValue(element, value) {
  if (window.motionValue) { window.motionValue(element, value); return; }
  const next = String(value);
  if (!element || element.textContent === next) return;
  element.textContent = next;
  if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return;
  element.classList.remove('value-change');
  void element.offsetWidth;
  element.classList.add('value-change');
  element.addEventListener('animationend', () => element.classList.remove('value-change'), {once: true});
}
const numericAnimations = new WeakMap();
function animateNumber(element, target, {decimals = 0, suffix = '', formatter} = {}) {
  if (!element || !Number.isFinite(target)) return;
  const format = formatter || ((value) => `${value.toFixed(decimals)}${suffix}`);
  const reduced = window.prefersReducedMotion?.() ?? window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
  const previousState = numericAnimations.get(element);
  if (previousState?.frame) cancelAnimationFrame(previousState.frame);
  const parsed = Number.parseFloat(element.dataset.motionValue);
  const from = Number.isFinite(parsed) ? parsed : target;
  element.dataset.motionValue = String(target);
  if (reduced || document.hidden || from === target) { element.textContent = format(target); return; }
  const state = {frame: 0}; numericAnimations.set(element, state);
  const started = performance.now();
  const duration = 760;
  element.classList.add('number-tweening');
  const draw = (now) => {
    const progress = Math.min(1, (now - started) / duration);
    const eased = 1 - Math.pow(1 - progress, 4);
    element.textContent = format(from + (target - from) * eased);
    if (progress < 1) state.frame = requestAnimationFrame(draw);
    else { element.textContent = format(target); element.classList.remove('number-tweening'); state.frame = 0; }
  };
  state.frame = requestAnimationFrame(draw);
}
function pulseText(element, value) {
  if (!element || element.textContent === value) return;
  element.textContent = value;
  if (window.prefersReducedMotion?.() || document.hidden) return;
  element.classList.remove('live-text-update'); void element.offsetWidth; element.classList.add('live-text-update');
}
try {
  const stored = JSON.parse(localStorage.getItem('hkc-log-presets') || '{}');
  if (stored && typeof stored === 'object' && !Array.isArray(stored)) savedFilters = stored;
} catch (_) {
  localStorage.removeItem('hkc-log-presets');
}

function lineModule(raw) { return raw.match(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \[[A-Z]+\] ([^:]+):/)?.[1] || ''; }
function matchesAdvanced(raw) {
  const module = $('#module-filter').value;
  if (module && lineModule(raw) !== module) return false;
  const match = raw.match(/^(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2})/);
  const from = $('#time-from').value;
  const to = $('#time-to').value;
  if (from || to) {
    if (!match) return false;
    const stamp = `${match[1]}T${match[2]}`;
    if (from && stamp < from) return false;
    if (to && stamp > `${to}:59`) return false;
  }
  return true;
}

function refreshModuleOptions() {
  const select = $('#module-filter'); const selected = select.value;
  const modules = [...new Set(allLines.map(lineModule).filter(Boolean))].sort();
  select.replaceChildren(new Option('Все модули', ''));
  for (const module of modules) select.add(new Option(module, module));
  if (modules.includes(selected)) select.value = selected;
}

function refreshPresetOptions() {
  const select = $('#preset-select'); const selected = select.value;
  select.replaceChildren(new Option('Выберите', ''));
  for (const name of Object.keys(savedFilters).sort()) select.add(new Option(name, name));
  if (savedFilters[selected]) select.value = selected;
  $('#preset-delete').disabled = !select.value;
}
refreshPresetOptions();

function setView(view) {
  if (!['logs', 'system', 'incidents'].includes(view)) view = 'logs';
  currentView = view;
  document.querySelectorAll('.workspace-view').forEach((panel) => { panel.hidden = panel.id !== `${view}-view`; });
  document.querySelectorAll('[data-view]').forEach((item) => item.classList.toggle('active', item.dataset.view === view));
  $('#journal-nav').classList.toggle('section-active', view === 'logs' || view === 'incidents');
  if (view === 'incidents' && typeof setJournalNavOpen === 'function') setJournalNavOpen(true);
  animateValue($('#view-title'), {logs: 'Журнал событий', system: 'Состояние системы', incidents: 'Происшествия'}[view]);
  if (view === 'incidents' && authenticated) refreshIncidents();
  if (view === 'system' && authenticated) refreshHistory();
  localStorage.setItem('hkc-view', view);
}

document.querySelectorAll('[data-view], [data-jump]').forEach((item) => item.addEventListener('click', () => setView(item.dataset.view || item.dataset.jump)));
function setJournalNavOpen(open) {
  $('#journal-nav').classList.toggle('open', open);
  $('#journal-nav-toggle').setAttribute('aria-expanded', String(open));
  $('#journal-nav-toggle').setAttribute('aria-label', open ? 'Свернуть раздел журнала' : 'Раскрыть раздел журнала');
  localStorage.setItem('hkc-journal-nav-open', String(open));
}
setJournalNavOpen(localStorage.getItem('hkc-journal-nav-open') === 'true');
$('#journal-nav-toggle').addEventListener('click', () => setJournalNavOpen(!$('#journal-nav').classList.contains('open')));
setView(localStorage.getItem('hkc-view') || 'logs');

function setTheme(theme, animate = false) {
  if (animate) window.motionTheme?.();
  document.documentElement.dataset.theme = theme;
  localStorage.setItem('hkc-theme', theme);
  $('#theme-toggle span').textContent = theme === 'light' ? 'Тёмная тема' : 'Светлая тема';
}
setTheme(localStorage.getItem('hkc-theme') || 'dark');
$('#theme-toggle').addEventListener('click', () => setTheme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark', true));

function lineLevel(raw) {
  return raw.match(/\[([A-Z]+)\]/)?.[1] || 'OTHER';
}

function matchesLevel(raw) {
  if (activeLevel === 'ALL') return true;
  const level = lineLevel(raw);
  return activeLevel === 'ERROR' ? level === 'ERROR' || level === 'CRITICAL' : level === activeLevel;
}

async function request(path, options = {}) {
  const response = await fetch(path, options);
  if (response.status === 401) {
    authenticated = false;
    if (!authDialog.open) openAuth();
    throw new Error('требуется вход');
  }
  const body = await response.json();
  if (!response.ok) throw new Error(body.message || `HTTP ${response.status}`);
  return body;
}

function showNotice(message, kind = 'ok') {
  notice.textContent = message;
  notice.className = `notice ${kind}`;
  if (window.motionShow) { window.motionShow(notice, 5000); return; }
  notice.hidden = false;
  clearTimeout(showNotice.timer);
  showNotice.timer = setTimeout(() => { notice.hidden = true; }, 5000);
}

document.addEventListener('keydown', (event) => {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 'k') {
    event.preventDefault(); openCommands(); return;
  }
  if (event.key === '/' && document.activeElement !== filterInput && !authDialog.open) {
    event.preventDefault();
    setView('logs');
    filterInput.focus();
  }
  if (event.key === 'Escape' && document.activeElement === filterInput) {
    filterInput.value = '';
    filterInput.blur();
    renderLines();
  }
});

function renderLines() {
  const query = filterInput.value.trim().toLowerCase();
  const visible = allLines.filter((line) => matchesLevel(line) && matchesAdvanced(line) && (!query || line.toLowerCase().includes(query)) && (!bookmarksOnly || bookmarks.has(lineKey(line))));
  const fragment = document.createDocumentFragment();
  for (const raw of visible) fragment.append(createLine(raw));
  if (!visible.length) fragment.append(createLogEmpty());
  logEl.replaceChildren(fragment);
  animateValue($('#line-count'), visible.length);
  $('#export-logs').disabled = visible.length === 0;
  $('#clear').disabled = allLines.length === 0 && pausedLines.length === 0;
  if (autoscroll.checked) logEl.scrollTop = logEl.scrollHeight;
}

function createLogEmpty() {
  const state = document.createElement('div');
  state.className = 'log-empty';
  const icon = document.createElement('span'); icon.textContent = logClearedByUser ? '✓' : '⌁';
  const title = document.createElement('strong');
  const detail = document.createElement('small');
  if (logClearedByUser) {
    title.textContent = 'Журнал очищен';
    detail.textContent = 'Новые события появятся здесь автоматически.';
  } else if (allLines.length) {
    title.textContent = 'Ничего не найдено';
    detail.textContent = 'Измените поиск или выбранные фильтры.';
  } else {
    title.textContent = 'Событий пока нет';
    detail.textContent = 'Поток подключён — новые записи появятся автоматически.';
  }
  state.append(icon, title, detail);
  return state;
}

function createLine(raw, live = false) {
  const row = document.createElement('div');
  const motionReduced = window.prefersReducedMotion?.() ?? window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
  const animateArrival = live && !motionReduced && document.visibilityState === 'visible';
  row.className = `line${animateArrival ? ' live' : ''}`;
  if (animateArrival) row.addEventListener('animationend', (event) => {
    if (event.target === row && !event.pseudoElement) row.classList.remove('live');
  });
  const match = raw.match(/^(\d{4}-\d{2}-\d{2}) (\d{2}:\d{2}:\d{2}) \[([A-Z]+)\] ([^:]+):\s?(.*)$/);
  if (!match) {
    row.classList.add('continuation');
    row.textContent = raw;
    return row;
  }
  const [, date, time, level, module, message] = match;
  row.dataset.level = level;
  if (timestamps.checked) {
    const timeEl = document.createElement('time');
    timeEl.dateTime = `${date}T${time}`;
    timeEl.textContent = time;
    row.append(timeEl);
  }
  const levelEl = document.createElement('span');
  levelEl.className = 'level';
  levelEl.textContent = level;
  const moduleEl = document.createElement('span');
  moduleEl.className = 'module';
  moduleEl.textContent = module;
  const messageEl = document.createElement('span');
  messageEl.className = 'message';
  messageEl.textContent = message;
  const mark = document.createElement('button');
  mark.type = 'button'; mark.className = 'line-bookmark'; mark.textContent = bookmarks.has(lineKey(raw)) ? '★' : '☆';
  mark.title = 'Отметить строку'; mark.setAttribute('aria-label', 'Отметить строку'); mark.setAttribute('aria-pressed', String(bookmarks.has(lineKey(raw))));
  mark.addEventListener('click', () => {
    const key = lineKey(raw);
    if (bookmarks.has(key)) bookmarks.delete(key); else bookmarks.add(key);
    localStorage.setItem('hkc-log-bookmarks', JSON.stringify([...bookmarks].slice(-500)));
    mark.textContent = bookmarks.has(key) ? '★' : '☆'; mark.setAttribute('aria-pressed', String(bookmarks.has(key)));
    if (bookmarksOnly) renderLines();
  });
  row.append(levelEl, moduleEl, messageEl, mark);
  return row;
}

function appendLiveLine(raw) {
  if (streamPaused) {
    pausedLines.push(raw);
    $('#pause-stream').innerHTML = `<span>▶</span> Продолжить · ${pausedLines.length}`;
    $('#clear').disabled = false;
    return;
  }
  logClearedByUser = false;
  allLines.push(raw);
  $('#clear').disabled = false;
  const module = lineModule(raw);
  if (module && ![...$('#module-filter').options].some((option) => option.value === module)) $('#module-filter').add(new Option(module, module));
  let trimmed = false;
  if (allLines.length > maxLines) {
    allLines.splice(0, allLines.length - maxLines);
    trimmed = true;
  }
  const query = filterInput.value.trim().toLowerCase();
  if (trimmed && query) {
    renderLines();
    return;
  }
  if (trimmed && logEl.firstChild) logEl.firstChild.remove();
  if (!matchesLevel(raw) || !matchesAdvanced(raw) || (query && !raw.toLowerCase().includes(query)) || (bookmarksOnly && !bookmarks.has(lineKey(raw)))) {
    if (logEl.querySelector('.log-empty')) renderLines();
    return;
  }
  logEl.querySelector('.log-empty')?.remove();
  logEl.append(createLine(raw, true));
  const visibleCount = logEl.querySelectorAll('.line').length;
  animateValue($('#line-count'), visibleCount);
  $('#export-logs').disabled = false;
  $('#clear').disabled = false;
  if (autoscroll.checked) logEl.scrollTop = logEl.scrollHeight;
}

document.querySelectorAll('.filter-chip').forEach((button) => {
  button.addEventListener('click', () => {
    activeLevel = button.dataset.level;
    document.querySelectorAll('.filter-chip').forEach((item) => item.classList.toggle('active', item === button));
    renderLines();
  });
});

$('#pause-stream').addEventListener('click', () => {
  streamPaused = !streamPaused;
  $('#pause-stream').classList.toggle('active', streamPaused);
  if (!streamPaused) {
    const queued = pausedLines.splice(0);
    $('#pause-stream').innerHTML = '<span>Ⅱ</span> Пауза';
    queued.forEach(appendLiveLine);
  } else {
    $('#pause-stream').innerHTML = '<span>▶</span> Продолжить';
  }
});

async function refreshStatus() {
  if (statusBusy) return;
  statusBusy = true;
  try {
    const status = await request('/api/status');
    lastStatus = status;
    $('#status-dot').classList.toggle('online', status.running);
    $('#status-card').classList.toggle('online', status.running);
    animateValue($('#status-label'), status.running ? 'бот запущен' : 'бот остановлен');
    $('#status-meta').textContent = status.running ? `PID ${status.pid} · ${status.uptime}` : 'процесс не найден';
    $('#version').textContent = `Панель ${window.panelVersion || '—'} · бот ${status.version || 'не определён'}`;
    $('#heroku-dir').textContent = status.herokuDir;
    document.querySelector('[data-action="start"]').disabled = status.running;
    document.querySelector('[data-action="stop"]').disabled = !status.running;
    document.querySelector('[data-action="restart"]').disabled = !status.running;
    if (!status.running && status.startupLog) $('#status-meta').title = status.startupLog;
  } catch (error) {
    $('#status-label').textContent = 'сервер недоступен';
    $('#status-meta').textContent = error.message;
  } finally { statusBusy = false; }
}

function formatMemory(bytes) { return bytes ? `${(bytes / 1048576).toFixed(1)} МБ` : '—'; }
function formatBytes(bytes) {
  if (!bytes) return '—';
  const units = ['Б', 'КБ', 'МБ', 'ГБ', 'ТБ'];
  let value = bytes; let unit = 0;
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
  return `${value.toFixed(unit < 2 ? 0 : 1)} ${units[unit]}`;
}
function formatUptime(seconds) {
  if (!seconds) return '—';
  const days = Math.floor(seconds / 86400);
  const hours = Math.floor((seconds % 86400) / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  return [days ? `${days} д` : '', hours ? `${hours} ч` : '', `${minutes} мин`].filter(Boolean).join(' ');
}
function percent(used, total) { return total ? Math.min(100, Math.max(0, used / total * 100)) : 0; }

let systemBusy = false;
async function refreshSystem() {
  if (systemBusy || document.hidden) return;
  systemBusy = true;
  try {
    const data = await request('/api/system');
    const cpu = data.cpuPercent || 0;
    const ram = percent(data.memoryUsedBytes, data.memoryTotalBytes);
    const disk = percent(data.diskUsedBytes, data.diskTotalBytes);
    if (data.supported) animateNumber($('#system-cpu'), cpu, {decimals: 1, suffix: '%'}); else animateValue($('#system-cpu'), '—');
    if (data.memoryTotalBytes) animateNumber($('#system-ram'), ram, {decimals: 1, suffix: '%'}); else animateValue($('#system-ram'), '—');
    if (data.diskTotalBytes) animateNumber($('#system-disk'), disk, {decimals: 1, suffix: '%'}); else animateValue($('#system-disk'), '—');
    $('#system-cpu-bar').style.width = `${cpu}%`;
    $('#system-ram-bar').style.width = `${ram}%`;
    $('#system-disk-bar').style.width = `${disk}%`;
    pulseText($('#system-cpu-meta'), data.supported ? `${data.cpuCores} логических CPU` : 'Метрики доступны в Linux/WSL');
    pulseText($('#system-ram-meta'), `${formatBytes(data.memoryUsedBytes)} из ${formatBytes(data.memoryTotalBytes)} · свободно ${formatBytes(data.memoryAvailableBytes)}`);
    pulseText($('#system-disk-meta'), `${formatBytes(data.diskUsedBytes)} из ${formatBytes(data.diskTotalBytes)} · свободно ${formatBytes(data.diskFreeBytes)}`);
    for (const period of [1, 5, 15]) {
      if (data.supported) animateNumber($(`#system-load-${period}`), Number(data[`load${period}`] || 0), {decimals: 2});
      else animateValue($(`#system-load-${period}`), '—');
    }
    $('#system-host').textContent = data.hostname || '—';
    $('#system-platform').textContent = `${data.os}/${data.arch}`;
    $('#system-kernel').textContent = data.kernel || (data.supported ? '—' : 'Метрики доступны в Linux/WSL');
    pulseText($('#system-uptime'), formatUptime(data.uptimeSeconds));
    pulseText($('#system-sampled'), data.supported ? `Обновлено ${new Date(data.sampledAt).toLocaleTimeString('ru-RU')}` : 'Метрики доступны в Linux/WSL');
  } catch (error) {
    $('#system-sampled').textContent = `Ошибка: ${error.message}`;
  } finally { systemBusy = false; }
}

$('#system-refresh').addEventListener('click', refreshSystem);
async function refreshPanelVersion() {
  try {
    const data = await request('/api/version');
    window.panelVersion = data.version || '—';
    if (lastStatus) $('#version').textContent = `Панель ${window.panelVersion} · бот ${lastStatus.version || 'не определён'}`;
  } catch (_) { /* Bot status remains available independently. */ }
}

async function refreshIncidents() {
  if (incidentsBusy) return;
  incidentsBusy = true;
  const container = $('#incident-list');
  try {
    const data = await request('/api/incidents');
    const signature = JSON.stringify(data.incidents || []);
    if (signature === incidentsSignature) return;
    incidentsSignature = signature;
    container.replaceChildren();
    if (!data.incidents?.length) { const empty = document.createElement('p'); empty.className = 'empty-state'; empty.textContent = 'В доступной части журнала происшествий нет.'; container.append(empty); return; }
    for (const incident of data.incidents) {
      const card = document.createElement('button'); card.className = 'incident-card'; card.type = 'button';
      const meta = document.createElement('span'); meta.className = 'incident-meta'; meta.textContent = `${incident.level} · ${incident.module} · ${incident.count} событий · ${incident.start}${incident.restarts ? ` · перезапусков рядом: ${incident.restarts}` : ''}`;
      const title = document.createElement('strong'); title.textContent = incident.title || 'Ошибка без описания';
      const context = document.createElement('small'); context.textContent = incident.context ? `Перед ошибкой: ${incident.context}` : 'Предшествующей строки нет';
      const action = document.createElement('em'); action.textContent = 'Открыть этот интервал в журнале →';
      card.append(meta, title, context, action);
      card.addEventListener('click', () => {
        const start = new Date(incident.start.replace(' ', 'T')); const end = new Date(incident.end.replace(' ', 'T'));
        const localValue = (date) => `${date.getFullYear()}-${String(date.getMonth()+1).padStart(2,'0')}-${String(date.getDate()).padStart(2,'0')}T${String(date.getHours()).padStart(2,'0')}:${String(date.getMinutes()).padStart(2,'0')}`;
        $('#time-from').value = localValue(new Date(start.getTime() - 120000));
        $('#time-to').value = localValue(new Date(end.getTime() + 120000));
        $('#module-filter').value = incident.module;
        activeLevel = 'ALL'; bookmarksOnly = false; $('#bookmarks-only').setAttribute('aria-pressed', 'false');
        document.querySelectorAll('.filter-chip').forEach((item) => item.classList.toggle('active', item.dataset.level === 'ALL'));
        setView('logs'); renderLines();
        document.querySelector('.log-advanced').open = true;
        logEl.querySelector('.line[data-level="ERROR"], .line[data-level="CRITICAL"]')?.scrollIntoView({block: 'center', behavior: 'auto'});
      });
      container.append(card);
    }
  } catch (error) { container.textContent = `Не удалось загрузить происшествия: ${error.message}`; }
  finally { incidentsBusy = false; }
}
$('#incidents-refresh').addEventListener('click', refreshIncidents);

let historyBusy = false;
const historyColors = {cpu: 'var(--mint)', memory: 'var(--amber)', disk: '#8baeff'};
function resampleSeries(points, key, count = 300) {
  if (!points.length) return [];
  if (points.length === 1) return Array(count).fill(Number(points[0][key]) || 0);
  return Array.from({length: count}, (_, index) => {
    const position = index * (points.length - 1) / (count - 1);
    const left = Math.floor(position); const right = Math.min(points.length - 1, left + 1); const mix = position - left;
    return (Number(points[left][key]) || 0) * (1 - mix) + (Number(points[right][key]) || 0) * mix;
  });
}
function historyPath(values) {
  return values.map((value, index) => `${index ? 'L' : 'M'} ${(index / Math.max(1, values.length - 1) * 1000).toFixed(2)} ${(195 - Math.min(100, Math.max(0, value)) * 1.9).toFixed(2)}`).join(' ');
}
function ensureHistorySVG(chart) {
  let svg = chart.querySelector('svg');
  if (svg) return svg;
  chart.replaceChildren();
  const ns = 'http://www.w3.org/2000/svg';
  svg = document.createElementNS(ns, 'svg'); svg.setAttribute('viewBox', '0 0 1000 200'); svg.setAttribute('preserveAspectRatio', 'none');
  const markers = document.createElementNS(ns, 'g'); markers.classList.add('history-markers'); svg.append(markers);
  for (const [key, color] of Object.entries(historyColors)) {
    const path = document.createElementNS(ns, 'path'); path.dataset.series = key; path.setAttribute('fill', 'none'); path.setAttribute('stroke', color); path.setAttribute('stroke-width', '2'); path.setAttribute('vector-effect', 'non-scaling-stroke');
    const dot = document.createElementNS(ns, 'circle'); dot.dataset.dot = key; dot.setAttribute('r', '3.5'); dot.setAttribute('fill', color); dot.setAttribute('vector-effect', 'non-scaling-stroke');
    path.style.color = color; dot.style.color = color;
    svg.append(path, dot);
  }
  chart.append(svg); return svg;
}
function morphHistorySeries(path, dot, next) {
  if (path.motionFrame) cancelAnimationFrame(path.motionFrame);
  const previous = path.motionValues?.length === next.length ? path.motionValues : next;
  const reduced = window.prefersReducedMotion?.() || document.hidden;
  const started = performance.now(); const duration = 820;
  const draw = (now) => {
    const progress = reduced ? 1 : Math.min(1, (now - started) / duration);
    const eased = 1 - Math.pow(1 - progress, 3);
    const values = next.map((value, index) => previous[index] + (value - previous[index]) * eased);
    path.setAttribute('d', historyPath(values));
    dot.setAttribute('cx', '1000'); dot.setAttribute('cy', String(195 - Math.min(100, Math.max(0, values.at(-1))) * 1.9));
    if (progress < 1) path.motionFrame = requestAnimationFrame(draw);
    else { path.motionValues = next; path.motionFrame = 0; }
  };
  path.motionFrame = requestAnimationFrame(draw);
}
async function refreshHistory() {
  if (historyBusy || document.hidden) return;
  historyBusy = true;
  try {
    const data = await request(`/api/system/history?range=${historyRange}`);
    const points = data.points || [];
    const chart = $('#history-chart');
    $('#history-count').textContent = `${Number(data.sampleCount ?? points.length).toLocaleString('ru-RU')} замеров · шаг 1 секунда`;
    if (points.length < 2) { chart.textContent = 'История появится после второго замера (около 1 секунды).'; return; }
    const ns = 'http://www.w3.org/2000/svg';
    const svg = ensureHistorySVG(chart);
    const start = new Date(points[0].at).getTime(); const span = Math.max(1, new Date(points.at(-1).at).getTime() - start);
    const x = (point) => (new Date(point.at).getTime() - start) / span * 1000;
    for (const key of Object.keys(historyColors)) morphHistorySeries(svg.querySelector(`[data-series="${key}"]`), svg.querySelector(`[data-dot="${key}"]`), resampleSeries(points, key));
    const markerGroup = svg.querySelector('.history-markers'); markerGroup.replaceChildren();
    for (let i = 1; i < points.length; i++) if (points[i].pid && points[i-1].pid && points[i].pid !== points[i-1].pid) {
      const marker = document.createElementNS(ns, 'line'); marker.setAttribute('x1', x(points[i])); marker.setAttribute('x2', x(points[i])); marker.setAttribute('y1', '0'); marker.setAttribute('y2', '200'); marker.setAttribute('stroke', 'var(--red)'); marker.setAttribute('stroke-dasharray', '5 5');
      markerGroup.append(marker);
    }
  } catch (error) { $('#history-chart').textContent = `История недоступна: ${error.message}`; }
  finally { historyBusy = false; }
}
document.querySelectorAll('[data-history-range]').forEach((button) => button.addEventListener('click', () => {
  historyRange = button.dataset.historyRange;
  document.querySelectorAll('[data-history-range]').forEach((item) => item.classList.toggle('active', item === button));
  $('#history-chart').replaceChildren();
  refreshHistory();
}));
$('#bookmarks-only').addEventListener('click', (event) => { bookmarksOnly = !bookmarksOnly; event.currentTarget.setAttribute('aria-pressed', String(bookmarksOnly)); renderLines(); });
$('#export-logs').addEventListener('click', () => {
  const query = filterInput.value.trim().toLowerCase();
  const lines = allLines.filter((line) => matchesLevel(line) && matchesAdvanced(line) && (!query || line.toLowerCase().includes(query)) && (!bookmarksOnly || bookmarks.has(lineKey(line))));
  const blob = new Blob([lines.join('\n') + '\n'], {type: 'text/plain;charset=utf-8'});
  const link = document.createElement('a'); link.href = URL.createObjectURL(blob); link.download = `heroku-log-${new Date().toISOString().slice(0, 10)}.txt`; link.click();
  setTimeout(() => URL.revokeObjectURL(link.href), 1000);
});

for (const selector of ['#module-filter', '#time-from', '#time-to']) document.querySelector(selector).addEventListener('change', renderLines);
$('#preset-save').addEventListener('click', () => {
  const name = $('#preset-name').value.trim();
  if (!name) { showNotice('Введите имя фильтра', 'error'); return; }
  savedFilters[name] = {query: filterInput.value, level: activeLevel, module: $('#module-filter').value, from: $('#time-from').value, to: $('#time-to').value};
  localStorage.setItem('hkc-log-presets', JSON.stringify(savedFilters));
  refreshPresetOptions(); $('#preset-select').value = name; $('#preset-delete').disabled = false; $('#preset-name').value = '';
  showNotice(`Фильтр «${name}» сохранён`);
});
$('#preset-select').addEventListener('change', () => {
  $('#preset-delete').disabled = !$('#preset-select').value;
  const preset = savedFilters[$('#preset-select').value]; if (!preset) return;
  filterInput.value = preset.query || ''; activeLevel = preset.level || 'ALL';
  $('#module-filter').value = preset.module || ''; $('#time-from').value = preset.from || ''; $('#time-to').value = preset.to || '';
  document.querySelectorAll('.filter-chip').forEach((item) => item.classList.toggle('active', item.dataset.level === activeLevel));
  renderLines();
});
$('#preset-delete').addEventListener('click', () => {
  const name = $('#preset-select').value; if (!name) return;
  delete savedFilters[name]; localStorage.setItem('hkc-log-presets', JSON.stringify(savedFilters)); refreshPresetOptions();
  showNotice(`Фильтр «${name}» удалён`);
});

const commands = [
  {name: 'Открыть журнал', run: () => setView('logs')},
  {name: 'Открыть происшествия', run: () => setView('incidents')},
  {name: 'Открыть состояние системы', run: () => setView('system')},
  {name: 'Найти в журнале', run: () => { setView('logs'); filterInput.focus(); }},
  {name: 'Открыть админку', run: () => { location.href = '/admin/'; }},
  {name: 'Сменить тему', run: () => $('#theme-toggle').click()},
];
let commandIndex = 0;
function renderCommands() {
  const query = $('#command-query').value.trim().toLowerCase();
  const matches = commands.filter((command) => command.name.toLowerCase().includes(query));
  commandIndex = Math.min(commandIndex, Math.max(matches.length - 1, 0));
  const results = $('#command-results'); results.replaceChildren();
  for (const [index, command] of matches.entries()) {
    const button = document.createElement('button'); button.className = `command-item ${index === commandIndex ? 'selected' : ''}`; button.textContent = command.name;
    button.addEventListener('click', () => { $('#command-dialog').close(); command.run(); }); results.append(button);
  }
  if (!matches.length) results.textContent = 'Ничего не найдено';
}
function openCommands() { $('#command-query').value = ''; commandIndex = 0; renderCommands(); $('#command-dialog').showModal(); $('#command-query').focus(); }
$('#command-open').addEventListener('click', openCommands);
$('#command-query').addEventListener('input', () => { commandIndex = 0; renderCommands(); });
$('#command-query').addEventListener('keydown', (event) => {
  const items = [...document.querySelectorAll('.command-item')];
  if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); commandIndex = (commandIndex + (event.key === 'ArrowDown' ? 1 : -1) + items.length) % (items.length || 1); renderCommands(); }
  if (event.key === 'Enter' && items[commandIndex]) { event.preventDefault(); items[commandIndex].click(); }
});

async function loadHistory() {
  const data = await request('/api/logs?limit=800');
  logClearedByUser = false;
  allLines = data.lines || [];
  refreshModuleOptions();
  renderLines();
}

function connectStream() {
  if (stream) stream.close();
  stream = new EventSource('/api/events');
  stream.onopen = () => {
    $('#stream-state').innerHTML = '<i></i> поток подключён';
    $('#stream-state').classList.add('online');
  };
  stream.onmessage = (event) => {
    appendLiveLine(JSON.parse(event.data));
  };
  stream.onerror = () => {
    $('#stream-state').innerHTML = '<i></i> переподключение…';
    $('#stream-state').classList.remove('online');
  };
}

document.querySelectorAll('[data-action]').forEach((button) => {
  button.addEventListener('click', async () => {
    const action = button.dataset.action;
    button.disabled = true;
    button.classList.add('loading');
    try {
      const result = await request(`/api/bot/${action}`, {method: 'POST'});
      showNotice(result.message);
    } catch (error) {
      showNotice(error.message, 'error');
    } finally {
      button.classList.remove('loading');
      setTimeout(refreshStatus, 500);
    }
  });
});

$('#clear').addEventListener('click', () => {
  allLines = [];
  pausedLines = [];
  logClearedByUser = true;
  if (streamPaused) $('#pause-stream').innerHTML = '<span>▶</span> Продолжить';
  renderLines();
  showNotice('Экран журнала очищен');
});
filterInput.addEventListener('input', renderLines);
timestamps.addEventListener('change', renderLines);
function openAuth(mode = authMode) {
  authMode = mode;
  const registering = mode === 'register';
  $('#auth-title').textContent = registering ? 'Регистрация по инвайту' : 'Вход';
  $('#auth-note').textContent = registering ? 'Инвайт одноразовый. Придумайте собственные логин и пароль.' : 'Войдите в аккаунт, созданный по инвайту.';
  $('#invite').hidden = !registering;
  $('#auth-password-confirm').hidden = !registering;
  $('#auth-password-confirm').required = registering;
  $('#auth-password').autocomplete = registering ? 'new-password' : 'current-password';
  $('#auth-switch').textContent = registering ? 'У меня уже есть аккаунт' : 'У меня есть инвайт';
  $('#auth-error').textContent = '';
  if (registering && initialInvite && !$('#invite').value) $('#invite').value = initialInvite;
  if (!authDialog.open) authDialog.showModal();
}

$('#auth-switch').addEventListener('click', () => openAuth(authMode === 'login' ? 'register' : 'login'));
$('#auth-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const payload = {
    username: $('#auth-username').value.trim(),
    password: $('#auth-password').value,
  };
  if (authMode === 'register') {
    payload.invite = $('#invite').value.trim();
    if (payload.password !== $('#auth-password-confirm').value) {
      $('#auth-error').textContent = 'пароли не совпадают';
      return;
    }
  }
  try {
    const response = await fetch(`/api/auth/${authMode}`, {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(payload),
    });
    const result = await response.json();
    if (!response.ok) throw new Error(result.message || 'ошибка авторизации');
    authDialog.close();
    await bootstrap();
  } catch (error) {
    $('#auth-error').textContent = error.message;
  }
});

$('#logout').addEventListener('click', async () => {
  await request('/api/auth/logout', {method: 'POST'});
  authenticated = false;
  if (stream) stream.close();
  openAuth('login');
});

$('#copy-console-link').addEventListener('click', async (event) => {
  const button = event.currentTarget;
  try {
    await navigator.clipboard.writeText(location.origin + '/');
    button.textContent = 'Ссылка скопирована';
    setTimeout(() => { button.textContent = 'Скопировать ссылку'; }, 2000);
  } catch (_) {
    showNotice('Не удалось скопировать ссылку', 'error');
  }
});

async function bootstrap() {
  try {
    const me = await request('/api/auth/me');
    authenticated = true;
    $('#username').textContent = me.username;
    const viewer = me.role === 'viewer';
    document.querySelectorAll('[data-action]').forEach((button) => { if (viewer) { button.hidden = true; } });
    await Promise.all([refreshPanelVersion(), refreshStatus(), loadHistory(), refreshSystem()]);
    if (currentView === 'incidents') refreshIncidents();
    if (currentView === 'system') refreshHistory();
    connectStream();
  } catch (error) {
    if (!authDialog.open) showNotice(error.message, 'error');
  }
}

bootstrap();
setInterval(() => {
  if (authenticated && !document.hidden) refreshStatus();
}, 1000);
setInterval(() => { if (authenticated && currentView === 'system') { refreshSystem(); refreshHistory(); } }, 1000);
setInterval(() => { if (authenticated && currentView === 'incidents' && !document.hidden) refreshIncidents(); }, 1000);
if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
