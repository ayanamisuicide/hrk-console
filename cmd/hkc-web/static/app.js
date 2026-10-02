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
let currentView = 'overview';
let lastStatus = null;
let lastInsights = null;
let savedFilters = {};
function animateValue(element, value) {
  const next = String(value);
  if (!element || element.textContent === next) return;
  element.textContent = next;
  if (window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return;
  element.classList.remove('value-change');
  void element.offsetWidth;
  element.classList.add('value-change');
  element.addEventListener('animationend', () => element.classList.remove('value-change'), {once: true});
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
}
refreshPresetOptions();

function setView(view) {
  if (!['overview', 'logs', 'monitor'].includes(view)) return;
  currentView = view;
  document.querySelectorAll('.workspace-view').forEach((panel) => { panel.hidden = panel.id !== `${view}-view`; });
  document.querySelectorAll('[data-view]').forEach((item) => item.classList.toggle('active', item.dataset.view === view));
  animateValue($('#view-title'), {overview: 'Обзор системы', logs: 'Журнал событий', monitor: 'Мониторинг'}[view]);
  localStorage.setItem('hkc-view', view);
}

document.querySelectorAll('[data-view], [data-jump]').forEach((item) => item.addEventListener('click', () => setView(item.dataset.view || item.dataset.jump)));
setView(localStorage.getItem('hkc-view') || 'overview');

function setTheme(theme) {
  document.documentElement.dataset.theme = theme;
  localStorage.setItem('hkc-theme', theme);
  $('#theme-toggle span').textContent = theme === 'light' ? 'Тёмная тема' : 'Светлая тема';
}
setTheme(localStorage.getItem('hkc-theme') || 'dark');
$('#theme-toggle').addEventListener('click', () => setTheme(document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark'));

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
  const visible = allLines.filter((line) => matchesLevel(line) && matchesAdvanced(line) && (!query || line.toLowerCase().includes(query)));
  const fragment = document.createDocumentFragment();
  for (const raw of visible) fragment.append(createLine(raw));
  logEl.replaceChildren(fragment);
  animateValue($('#line-count'), visible.length);
  if (autoscroll.checked) logEl.scrollTop = logEl.scrollHeight;
  renderRecentEvents();
}

function renderRecentEvents() {
  const container = $('#recent-events');
  const recent = allLines.filter((line) => /\[(INFO|WARNING|ERROR|CRITICAL)\]/.test(line)).slice(-6).reverse();
  container.replaceChildren();
  if (!recent.length) { const empty = document.createElement('p'); empty.className = 'empty-state'; empty.textContent = 'События появятся после запуска бота.'; container.append(empty); return; }
  for (const raw of recent) {
    const row = document.createElement('div'); row.className = 'recent-row';
    const level = document.createElement('span'); level.className = `recent-level ${lineLevel(raw).toLowerCase()}`; level.textContent = lineLevel(raw);
    const message = document.createElement('span'); message.textContent = raw.replace(/^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2} \[[A-Z]+\] /, '');
    row.append(level, message); container.append(row);
  }
}

function createLine(raw, live = false) {
  const row = document.createElement('div');
  const animateArrival = live && !window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;
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
  row.append(levelEl, moduleEl, messageEl);
  return row;
}

function appendLiveLine(raw) {
  if (streamPaused) {
    pausedLines.push(raw);
    $('#pause-stream').innerHTML = `<span>▶</span> Продолжить · ${pausedLines.length}`;
    return;
  }
  allLines.push(raw);
  const module = lineModule(raw);
  if (module && ![...$('#module-filter').options].some((option) => option.value === module)) $('#module-filter').add(new Option(module, module));
  renderRecentEvents();
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
  if (!matchesLevel(raw) || !matchesAdvanced(raw) || (query && !raw.toLowerCase().includes(query))) return;
  logEl.append(createLine(raw, true));
  animateValue($('#line-count'), logEl.childElementCount);
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
  try {
    const status = await request('/api/status');
    lastStatus = status;
    $('#status-dot').classList.toggle('online', status.running);
    $('#status-card').classList.toggle('online', status.running);
    animateValue($('#status-label'), status.running ? 'бот запущен' : 'бот остановлен');
    $('#status-meta').textContent = status.running ? `PID ${status.pid} · ${status.uptime}` : 'процесс не найден';
    $('#version').textContent = status.version ? `версия ${status.version}` : 'версия не определена';
    $('#heroku-dir').textContent = status.herokuDir;
    document.querySelector('[data-action="start"]').disabled = status.running;
    document.querySelector('[data-action="stop"]').disabled = !status.running;
    document.querySelector('[data-action="restart"]').disabled = !status.running;
    if (!status.running && status.startupLog) $('#status-meta').title = status.startupLog;
    animateValue($('#metric-process'), status.running ? 'Работает' : 'Остановлен');
    $('#metric-process-meta').textContent = status.running ? `PID ${status.pid} · ${status.uptime}` : 'Можно запустить из панели';
    $('#service-description').textContent = status.running ? `Процесс активен ${status.uptime}` : 'Бот сейчас не запущен';
    $('#service-version').textContent = status.version || 'Не определена';
    $('#service-pid').textContent = status.pid || '—';
    $('#service-log').textContent = status.logReady ? 'Доступен' : 'Не найден';
  } catch (error) {
    $('#status-label').textContent = 'сервер недоступен';
    $('#status-meta').textContent = error.message;
  }
}

function formatMemory(bytes) { return bytes ? `${(bytes / 1048576).toFixed(1)} МБ` : '—'; }

function renderChart(points) {
  const values = points.filter((point) => point.rssBytes > 0).slice(-120);
  $('#chart-empty').hidden = values.length > 1;
  if (values.length < 2) { $('#memory-area').setAttribute('d', ''); $('#memory-line').setAttribute('d', ''); return; }
  const max = Math.max(...values.map((point) => point.rssBytes), 1) * 1.15;
  const coordinates = values.map((point, index) => `${(index / (values.length - 1) * 800).toFixed(1)},${(205 - point.rssBytes / max * 180).toFixed(1)}`);
  $('#memory-line').setAttribute('d', `M${coordinates.join(' L')}`);
  $('#memory-area').setAttribute('d', `M${coordinates.join(' L')} L800,220 L0,220 Z`);
}

let metricsBusy = false;
async function refreshMetrics() {
  if (metricsBusy || document.hidden) return;
  metricsBusy = true;
  try {
    const data = await request('/api/metrics');
    const value = formatMemory(data.rssBytes);
    $('#metric-memory').textContent = value;
    $('#monitor-memory').textContent = value;
    renderChart(data.points || []);
  } catch (_) {
    $('#metric-memory').textContent = '—';
    $('#monitor-memory').textContent = '—';
  } finally { metricsBusy = false; }
}

async function refreshInsights() {
  try {
    const data = await request('/api/insights');
    lastInsights = data;
    animateValue($('#metric-errors'), data.logCounts.error);
    animateValue($('#metric-warnings'), data.logCounts.warning);
    animateValue($('#monitor-status'), data.running ? 'Работает' : 'Остановлен');
    animateValue($('#monitor-errors'), data.logCounts.error);
    animateValue($('#monitor-lines'), data.sampledLines);
    $('#overview-updated').textContent = `Обновлено ${new Intl.DateTimeFormat('ru-RU', {timeStyle: 'medium'}).format(new Date())}`;
  } catch (error) { $('#overview-updated').textContent = `Нет данных: ${error.message}`; }
}

async function refreshDiagnostics() {
  try {
    const data = await request('/api/diagnostics');
    const container = $('#diagnostic-checks'); container.replaceChildren();
    for (const check of data.checks || []) {
      const item = document.createElement('div'); item.className = `diagnostic-item ${check.ok ? 'ok' : 'missing'}`;
      const symbol = document.createElement('span'); symbol.textContent = check.ok ? '✓' : '!';
      const copy = document.createElement('div');
      const title = document.createElement('strong'); title.textContent = check.name;
      const detail = document.createElement('small'); detail.textContent = check.detail;
      copy.append(title, detail); item.append(symbol, copy); container.append(item);
    }
  } catch (error) { $('#diagnostic-checks').textContent = `Проверка недоступна: ${error.message}`; }
}
$('#diagnostics-refresh').addEventListener('click', refreshDiagnostics);

$('#metrics-refresh').addEventListener('click', () => { refreshMetrics(); refreshInsights(); });
$('#export-logs').addEventListener('click', () => {
  const query = filterInput.value.trim().toLowerCase();
  const lines = allLines.filter((line) => matchesLevel(line) && matchesAdvanced(line) && (!query || line.toLowerCase().includes(query)));
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
  refreshPresetOptions(); $('#preset-select').value = name; $('#preset-name').value = '';
  showNotice(`Фильтр «${name}» сохранён`);
});
$('#preset-select').addEventListener('change', () => {
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
  {name: 'Открыть обзор', run: () => setView('overview')},
  {name: 'Открыть журнал', run: () => setView('logs')},
  {name: 'Открыть мониторинг', run: () => setView('monitor')},
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
  renderLines();
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
    await Promise.all([refreshStatus(), loadHistory(), refreshInsights(), refreshMetrics(), refreshDiagnostics()]);
    connectStream();
  } catch (error) {
    if (!authDialog.open) showNotice(error.message, 'error');
  }
}

bootstrap();
setInterval(() => {
  if (authenticated) refreshStatus();
}, 1500);
setInterval(() => { if (authenticated) refreshInsights(); }, 10000);
setInterval(() => { if (authenticated) refreshMetrics(); }, 1000);
if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js').catch(() => {});
