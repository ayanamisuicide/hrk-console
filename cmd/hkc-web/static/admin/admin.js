const $ = (selector) => document.querySelector(selector);
const authDialog = $('#admin-auth');
document.documentElement.dataset.theme = localStorage.getItem('hkc-theme') || 'dark';
$('#admin-theme').addEventListener('click', () => {
  const theme = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark';
  document.documentElement.dataset.theme = theme;
  localStorage.setItem('hkc-theme', theme);
});
let adminToken = sessionStorage.getItem('hkc-admin-token') || '';
let refreshTimer;

function confirmAction(title, message) {
  const dialog = $('#confirm-dialog');
  $('#confirm-title').textContent = title;
  $('#confirm-message').textContent = message;
  dialog.showModal();
  return new Promise((resolve) => {
    dialog.addEventListener('close', () => resolve(dialog.returnValue === 'confirm'), {once: true});
  });
}

async function adminRequest(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set('Authorization', `Bearer ${adminToken}`);
  if (options.body) headers.set('Content-Type', 'application/json');
  const response = await fetch(path, {...options, headers});
  const body = await response.json();
  if (response.status === 401) {
    sessionStorage.removeItem('hkc-admin-token');
    adminToken = '';
    openAuth('Неверный или изменившийся административный токен');
    throw new Error(body.message || 'требуется административный токен');
  }
  if (!response.ok) throw new Error(body.message || `HTTP ${response.status}`);
  return body;
}

function openAuth(message = '') {
  $('#admin-auth-error').textContent = message;
  if (!authDialog.open) authDialog.showModal();
}

function formatDate(value) {
  if (!value) return 'никогда';
  return new Intl.DateTimeFormat('ru-RU', {dateStyle: 'medium', timeStyle: 'short'}).format(new Date(value));
}

function showNotice(message, kind = 'ok') {
  const node = $('#admin-notice');
  node.textContent = message;
  node.className = `notice ${kind}`;
  node.hidden = false;
  clearTimeout(showNotice.timer);
  showNotice.timer = setTimeout(() => { node.hidden = true; }, 5000);
}

function renderUsers(users) {
  const body = $('#users-body');
  body.replaceChildren();
  $('#users-empty').hidden = users.length !== 0;
  for (const user of users) {
    const row = document.createElement('tr');
    const name = document.createElement('td');
    name.innerHTML = `<strong></strong><small></small>`;
    name.querySelector('strong').textContent = user.username;
    name.querySelector('small').textContent = `${user.activeSessions} активных сессий`;

    const roleCell = document.createElement('td');
    const role = document.createElement('select');
    role.className = 'role-select';
    for (const [value, label] of [['operator', 'Оператор'], ['viewer', 'Наблюдатель']]) {
      const option = document.createElement('option'); option.value = value; option.textContent = label; role.append(option);
    }
    role.value = user.role || 'operator';
    role.addEventListener('change', async () => {
      role.disabled = true;
      try {
        await adminRequest(`/api/admin/users/${encodeURIComponent(user.username)}/role`, {method: 'PATCH', body: JSON.stringify({role: role.value})});
        showNotice(`Роль ${user.username} изменена`);
        await refresh();
      } catch (error) { role.value = user.role || 'operator'; showNotice(error.message, 'error'); }
      role.disabled = false;
    });
    roleCell.append(role);

    const status = document.createElement('td');
    const badge = document.createElement('span');
    badge.className = `presence ${user.online ? 'online' : ''}`;
    badge.textContent = user.online ? '● онлайн' : '○ офлайн';
    status.append(badge);

    const created = document.createElement('td');
    created.textContent = formatDate(user.createdAt);
    const seen = document.createElement('td');
    seen.textContent = formatDate(user.lastSeen);
    const actions = document.createElement('td');
    const remove = document.createElement('button');
    remove.className = 'danger compact';
    remove.textContent = 'Удалить';
    remove.addEventListener('click', () => deleteUser(user.username));
    actions.append(remove);
    row.append(name, roleCell, status, created, seen, actions);
    body.append(row);
  }
}

function renderInvites(invites) {
  const grid = $('#invites-grid');
  grid.replaceChildren();
  $('#invites-empty').hidden = invites.length !== 0;
  for (const invite of invites) {
    const card = document.createElement('article');
    card.className = 'invite-card';
    const token = document.createElement('code');
    token.textContent = invite.token;
    const expiry = document.createElement('p');
    expiry.textContent = `${invite.role === 'viewer' ? 'Наблюдатель' : 'Оператор'} · действует до ${formatDate(invite.expiresAt)}`;
    const actions = document.createElement('div');
    const copy = document.createElement('button');
    copy.className = 'compact';
    copy.textContent = 'Копировать ссылку';
    copy.addEventListener('click', async () => {
      await navigator.clipboard.writeText(invite.registrationUrl);
      showNotice('Ссылка регистрации скопирована');
    });
    const revoke = document.createElement('button');
    revoke.className = 'danger compact';
    revoke.textContent = 'Отозвать';
    revoke.addEventListener('click', () => revokeInvite(invite.token));
    actions.append(copy, revoke);
    card.append(token, expiry, actions);
    grid.append(card);
  }
}

function renderBot(bot) {
  $('#admin-bot-status').textContent = bot.running ? 'Heroku работает' : 'Heroku остановлен';
  $('#admin-bot-meta').textContent = bot.running ? `PID ${bot.pid} · ${bot.uptime} · версия ${bot.version || '—'}` : bot.herokuDir;
  $('#bot-orbit-dot').classList.toggle('online', bot.running);
  document.querySelector('[data-bot-action="start"]').disabled = bot.running;
  document.querySelector('[data-bot-action="restart"]').disabled = !bot.running;
  document.querySelector('[data-bot-action="stop"]').disabled = !bot.running;
}

function renderAudit(events) {
  const body = $('#audit-body'); body.replaceChildren();
  $('#audit-empty').hidden = events.length !== 0;
  for (const event of events) {
    const row = document.createElement('tr');
    for (const value of [formatDate(event.time), event.actor, event.action, event.detail, event.ip]) {
      const cell = document.createElement('td'); cell.textContent = value || '—'; row.append(cell);
    }
    body.append(row);
  }
}

function renderBackups(backups) {
  const list = $('#backup-list'); list.replaceChildren();
  $('#backup-empty').hidden = backups.length !== 0;
  for (const backup of backups) {
    const row = document.createElement('div'); row.className = 'backup-row';
    const info = document.createElement('div');
    const title = document.createElement('strong'); title.textContent = formatDate(backup.createdAt);
    const detail = document.createElement('small'); detail.textContent = `${backup.name} · ${(backup.size / 1024).toFixed(1)} КБ`;
    info.append(title, detail);
    const restore = document.createElement('button'); restore.className = 'compact'; restore.textContent = 'Восстановить';
    restore.addEventListener('click', async () => {
      if (!await confirmAction('Восстановить базу доступа?', `Будут восстановлены пользователи и инвайты из копии ${backup.name}. Все пользовательские сессии завершатся.`)) return;
      try {
        const result = await adminRequest(`/api/admin/backups/${encodeURIComponent(backup.name)}/restore`, {method: 'POST'});
        showNotice(result.message); await refresh();
      } catch (error) { showNotice(error.message, 'error'); }
    });
    row.append(info, restore); list.append(row);
  }
}

function renderConfig(config) {
  $('#config-path').textContent = config.path;
  for (const [key, configured] of Object.entries(config.configured || {})) {
    const status = document.querySelector(`[data-config-state="${key}"]`);
    if (status) { status.textContent = configured ? '● задано' : '○ не задано'; status.classList.toggle('configured', configured); }
    const remove = document.querySelector(`[data-delete-config="${key}"]`);
    if (remove) remove.hidden = !configured;
  }
}

function renderTokens(tokens) {
  const list = $('#token-list'); list.replaceChildren();
  $('#tokens-empty').hidden = tokens.length !== 0;
  for (const token of tokens) {
    const row = document.createElement('div'); row.className = 'token-row';
    const info = document.createElement('div');
    const title = document.createElement('strong'); title.textContent = token.label;
    const meta = document.createElement('small'); meta.textContent = `${token.scope === 'control' ? 'Управление' : 'Чтение'} · создан ${formatDate(token.createdAt)} · использован ${formatDate(token.lastUsed)}`;
    info.append(title, meta);
    const revoke = document.createElement('button'); revoke.className = 'danger compact'; revoke.textContent = 'Отозвать';
    revoke.addEventListener('click', async () => {
      if (!await confirmAction('Отозвать API-токен?', `Интеграция «${token.label}» сразу потеряет доступ.`)) return;
      try { await adminRequest(`/api/admin/tokens/${encodeURIComponent(token.id)}`, {method: 'DELETE'}); showNotice('Токен отозван'); await refresh(); }
      catch (error) { showNotice(error.message, 'error'); }
    });
    row.append(info, revoke); list.append(row);
  }
}

async function refresh() {
  if (!adminToken) {
    openAuth();
    return;
  }
  try {
    const [data, audit, backups, config, tokens] = await Promise.all([adminRequest('/api/admin/overview'), adminRequest('/api/admin/audit'), adminRequest('/api/admin/backups'), adminRequest('/api/admin/config'), adminRequest('/api/admin/tokens')]);
    $('#users-count').textContent = data.users.length;
    $('#online-count').textContent = data.users.filter((user) => user.online).length;
    $('#invites-count').textContent = data.invites.length;
    renderBot(data.bot);
    renderUsers(data.users);
    renderInvites(data.invites);
    renderAudit(audit.events || []);
    renderBackups(backups.backups || []);
    renderConfig(config);
    renderTokens(tokens.tokens || []);
  } catch (error) {
    if (adminToken) showNotice(error.message, 'error');
  }
}

document.querySelectorAll('[data-bot-action]').forEach((button) => {
  button.addEventListener('click', async () => {
    const action = button.dataset.botAction;
    button.disabled = true;
    try {
      const result = await adminRequest(`/api/admin/bot/${action}`, {method: 'POST'});
      showNotice(result.message);
      setTimeout(refresh, 500);
    } catch (error) {
      showNotice(error.message, 'error');
      await refresh();
    }
  });
});

async function deleteUser(username) {
  if (!await confirmAction('Удалить пользователя?', `${username} потеряет доступ, а все активные сессии завершатся.`)) return;
  try {
    await adminRequest(`/api/admin/users/${encodeURIComponent(username)}`, {method: 'DELETE'});
    showNotice(`Пользователь ${username} удалён`);
    await refresh();
  } catch (error) {
    showNotice(error.message, 'error');
  }
}

async function revokeInvite(token) {
  if (!await confirmAction('Отозвать инвайт?', 'Ссылка регистрации сразу перестанет работать.')) return;
  try {
    await adminRequest(`/api/admin/invites/${encodeURIComponent(token)}`, {method: 'DELETE'});
    showNotice('Инвайт отозван');
    await refresh();
  } catch (error) {
    showNotice(error.message, 'error');
  }
}

$('#admin-auth-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  adminToken = $('#admin-token').value.trim();
  try {
    await adminRequest('/api/admin/overview');
    sessionStorage.setItem('hkc-admin-token', adminToken);
    authDialog.close();
    await refresh();
  } catch (error) {
    $('#admin-auth-error').textContent = error.message;
  }
});

$('#invite-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const expiresHours = Number($('#invite-hours').value);
  const role = $('#invite-role').value;
  try {
    const invite = await adminRequest('/api/admin/invites', {
      method: 'POST', body: JSON.stringify({expiresHours, role}),
    });
    showNotice(`Инвайт создан до ${formatDate(invite.expiresAt)}`);
    await refresh();
  } catch (error) {
    showNotice(error.message, 'error');
  }
});

$('#admin-logout').addEventListener('click', () => {
  sessionStorage.removeItem('hkc-admin-token');
  adminToken = '';
  clearInterval(refreshTimer);
  openAuth();
});

$('#admin-refresh').addEventListener('click', async (event) => {
  const button = event.currentTarget;
  button.disabled = true;
  button.textContent = 'Обновление…';
  await refresh();
  button.textContent = '↻ Обновить';
  button.disabled = false;
});

$('#create-backup').addEventListener('click', async (event) => {
  const button = event.currentTarget; button.disabled = true;
  try { await adminRequest('/api/admin/backups', {method: 'POST'}); showNotice('Резервная копия создана'); await refresh(); }
  catch (error) { showNotice(error.message, 'error'); }
  button.disabled = false;
});

$('#config-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const form = event.currentTarget;
  const values = {};
  for (const input of form.querySelectorAll('input[name]')) if (input.value.trim()) values[input.name] = input.value.trim();
  if (!Object.keys(values).length) { showNotice('Введите хотя бы одно новое значение', 'error'); return; }
  const submit = form.querySelector('[type="submit"]'); submit.disabled = true;
  try {
    const result = await adminRequest('/api/admin/config', {method: 'PATCH', body: JSON.stringify(values)});
    form.reset(); showNotice(result.message); await refresh();
  } catch (error) { showNotice(error.message, 'error'); }
  submit.disabled = false;
});

document.querySelectorAll('[data-delete-config]').forEach((button) => button.addEventListener('click', async () => {
  const key = button.dataset.deleteConfig;
  if (!await confirmAction('Удалить параметр?', `Параметр ${key} будет удалён из config.json. Для применения потребуется перезапуск бота.`)) return;
  button.disabled = true;
  try {
    const result = await adminRequest(`/api/admin/config/${encodeURIComponent(key)}`, {method: 'DELETE'});
    showNotice(result.message); await refresh();
  } catch (error) { showNotice(error.message, 'error'); }
  button.disabled = false;
}));

document.querySelectorAll('[data-diagnostic]').forEach((button) => button.addEventListener('click', async () => {
  button.disabled = true; $('#diagnostic-output').textContent = 'Выполняется проверка…';
  try {
    const result = await adminRequest(`/api/admin/diagnostics/${button.dataset.diagnostic}`, {method: 'POST'});
    $('#diagnostic-output').textContent = result.output || 'Команда не вернула данные.';
  } catch (error) { $('#diagnostic-output').textContent = error.message; }
  button.disabled = false;
}));

$('#token-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const button = event.currentTarget.querySelector('[type="submit"]'); button.disabled = true;
  try {
    const result = await adminRequest('/api/admin/tokens', {method: 'POST', body: JSON.stringify({label: $('#token-label').value.trim(), scope: $('#token-scope').value})});
    $('#token-form').reset(); $('#new-api-token').textContent = result.token; $('#token-dialog').showModal(); await refresh();
  } catch (error) { showNotice(error.message, 'error'); }
  button.disabled = false;
});
$('#copy-api-token').addEventListener('click', async () => {
  try { await navigator.clipboard.writeText($('#new-api-token').textContent); showNotice('API-токен скопирован'); }
  catch (error) { showNotice('Не удалось скопировать токен', 'error'); }
});
$('#close-api-token').addEventListener('click', () => $('#token-dialog').close());
$('#token-dialog').addEventListener('close', () => { $('#new-api-token').textContent = ''; });

refresh();
refreshTimer = setInterval(refresh, 10000);
