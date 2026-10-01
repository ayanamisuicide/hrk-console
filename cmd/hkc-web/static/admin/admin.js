const $ = (selector) => document.querySelector(selector);
const authDialog = $('#admin-auth');
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
    row.append(name, status, created, seen, actions);
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
    expiry.textContent = `действует до ${formatDate(invite.expiresAt)}`;
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

async function refresh() {
  if (!adminToken) {
    openAuth();
    return;
  }
  try {
    const data = await adminRequest('/api/admin/overview');
    $('#users-count').textContent = data.users.length;
    $('#online-count').textContent = data.users.filter((user) => user.online).length;
    $('#invites-count').textContent = data.invites.length;
    renderBot(data.bot);
    renderUsers(data.users);
    renderInvites(data.invites);
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
  try {
    const invite = await adminRequest('/api/admin/invites', {
      method: 'POST', body: JSON.stringify({expiresHours}),
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

refresh();
refreshTimer = setInterval(refresh, 10000);
