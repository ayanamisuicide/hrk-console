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
  if (event.key === '/' && document.activeElement !== filterInput && !authDialog.open) {
    event.preventDefault();
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
  const visible = query ? allLines.filter((line) => line.toLowerCase().includes(query)) : allLines;
  const fragment = document.createDocumentFragment();
  for (const raw of visible) fragment.append(createLine(raw));
  logEl.replaceChildren(fragment);
  $('#line-count').textContent = `${visible.length} строк`;
  if (autoscroll.checked) logEl.scrollTop = logEl.scrollHeight;
}

function createLine(raw, live = false) {
  const row = document.createElement('div');
  row.className = `line${live ? ' live' : ''}`;
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
  allLines.push(raw);
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
  if (query && !raw.toLowerCase().includes(query)) return;
  logEl.append(createLine(raw, true));
  $('#line-count').textContent = `${logEl.childElementCount} строк`;
  if (autoscroll.checked) logEl.scrollTop = logEl.scrollHeight;
}

async function refreshStatus() {
  try {
    const status = await request('/api/status');
    $('#status-dot').classList.toggle('online', status.running);
    $('#status-card').classList.toggle('online', status.running);
    $('#status-label').textContent = status.running ? 'бот запущен' : 'бот остановлен';
    $('#status-meta').textContent = status.running ? `PID ${status.pid} · ${status.uptime}` : 'процесс не найден';
    $('#version').textContent = status.version ? `версия ${status.version}` : 'версия не определена';
    $('#heroku-dir').textContent = status.herokuDir;
    document.querySelector('[data-action="start"]').disabled = status.running;
    document.querySelector('[data-action="stop"]').disabled = !status.running;
    document.querySelector('[data-action="restart"]').disabled = !status.running;
    if (!status.running && status.startupLog) $('#status-meta').title = status.startupLog;
  } catch (error) {
    $('#status-label').textContent = 'сервер недоступен';
    $('#status-meta').textContent = error.message;
  }
}

async function loadHistory() {
  const data = await request('/api/logs?limit=800');
  allLines = data.lines || [];
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

async function bootstrap() {
  try {
    const me = await request('/api/auth/me');
    authenticated = true;
    $('#username').textContent = me.username;
    await Promise.all([refreshStatus(), loadHistory()]);
    connectStream();
  } catch (error) {
    if (!authDialog.open) showNotice(error.message, 'error');
  }
}

bootstrap();
setInterval(() => {
  if (authenticated) refreshStatus();
}, 1500);
